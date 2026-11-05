package main

// nlu_overlay.go: the owner's forge overlay — examples `jazz forge` accepted
// through the held-out gate (forge.go), merged into the embedded grammar at
// load. The embedded nlu/intents.json stays the source of truth: the overlay
// only ADDS examples (never rules, lexicon, negatives or intents), only to
// intents that exist, deduplicated by the text the classifier sees, capped,
// and appended after the embedded ones (Examples[0] stays the sample phrase
// the miss menu shows).
//
// File: dataDir()/forge/intents.overlay.json
//
//	{"version":1, "base_sha256":"<sha of the embedded intents.json it was
//	 forged against>", "examples":{"<intent>":["texto", ...]},
//	 "provenance":[{"text":..,"intent":..,"source":"correction","count":2,"ts":..}]}
//
// A missing file is the normal case; an invalid one is ignored (debug note)
// and the embedded grammar runs as shipped. Off with JAZZ_FORGE=off, in
// --fresh, during `jazz eval offline` and inside test binaries (tests never
// read the owner's data dir; the overlay tests apply files explicitly).
// Cost: with no file, one failed open at the first nuGet. With a file forged
// against this very grammar (base_sha256 = the embedded intents.json), the
// forge already deduplicated it against the grammar, so the load only
// canonicalizes the overlay's own texts and the ~100 negatives — never the
// ~900 embedded examples. After a grammar upgrade (sha differs) the full
// index runs once per process to drop what the grammar learned meanwhile.
// The naive Bayes trains lazily (classify) either way.
//
// Mutating intents (fgIsMutating: push, commit, delete, kill, restart…)
// never take overlay examples, not even from a hand-edited file: the forge
// classifies, it never moves a reading toward a mutation.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	nuOverlayVersion   = 1
	nuOverlayMaxBytes  = 1 << 20 // the file is read through a LimitReader
	nuOverlayCap       = 2000    // examples, all intents
	nuOverlayPerIntent = 200
	nuOverlayMaxText   = 200 // runes per example (the level-0 gates stop at 200 bytes too)
)

type nuOverlay struct {
	Version    int                 `json:"version"`
	BaseSHA256 string              `json:"base_sha256,omitempty"`
	Examples   map[string][]string `json:"examples"`
	Provenance []nuOverlayProv     `json:"provenance,omitempty"`
}

// nuOverlayProv is where an accepted example came from (audit, not input).
type nuOverlayProv struct {
	Text   string `json:"text"`
	Intent string `json:"intent"`
	Source string `json:"source"`
	Count  int    `json:"count"`
	Ts     int64  `json:"ts"`
	// audit (spec 0.14b item 10): distinct sessions behind it and the
	// distinguishing words the anti-trigger check passed
	Sessions int      `json:"sessions,omitempty"`
	Words    []string `json:"words,omitempty"`
}

// nuOverlayOff turns the overlay off for the process (the offline eval
// measures the embedded grammar). nuOverlayTestOn lets a test binary load it.
var (
	nuOverlayOff    atomic.Bool
	nuOverlayTestOn atomic.Bool
)

func nuOverlayPath() string { return filepath.Join(dataDir(), "forge", "intents.overlay.json") }

func nuForgeOn() bool {
	if nuOverlayOff.Load() || olOffWord(os.Getenv("JAZZ_FORGE")) || pfFresh() {
		return false
	}
	return !jzUnderTest() || nuOverlayTestOn.Load()
}

// nuEmbeddedSHA is the sha256 of the shipped intents.json (the overlay
// records what it was forged against; a newer grammar still accepts it —
// the dedup below drops what the grammar learned meanwhile).
func nuEmbeddedSHA() string {
	nuShaOnce.Do(func() {
		ds, _ := nuFS.ReadFile("nlu/intents.json")
		h := sha256.Sum256(ds)
		nuSha = hex.EncodeToString(h[:])
	})
	return nuSha
}

var (
	nuShaOnce sync.Once
	nuSha     string
)

// nuReadOverlay reads and validates an overlay file. A missing file is
// (nil, nil).
func nuReadOverlay(path string) (*nuOverlay, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, nuOverlayMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > nuOverlayMaxBytes {
		return nil, errors.New("overlay maior que 1 MiB")
	}
	var ov nuOverlay
	if err := json.Unmarshal(b, &ov); err != nil {
		return nil, err
	}
	if ov.Version != nuOverlayVersion {
		return nil, errors.New("versão do overlay desconhecida")
	}
	return &ov, nil
}

// canonOf is the text the classifier trains on and sees (the dedup key).
func (m *nuModel) canonOf(text string) string {
	return nuCanonText(m.normalize(nuTokenize(text)))
}

// nuOverlayIndex is what a model already knows: canon of every example →
// its intent, and every (intent, canon) negative.
type nuOverlayIndex struct {
	ex  map[string]string
	neg map[string]bool
}

func nuIndexOf(m *nuModel) nuOverlayIndex {
	return nuIndexWith(m, true)
}

// nuIndexWith builds the index; examples=false indexes only the negatives
// (the cheap load path of an overlay forged against this grammar).
func nuIndexWith(m *nuModel, examples bool) nuOverlayIndex {
	ix := nuOverlayIndex{ex: map[string]string{}, neg: map[string]bool{}}
	for _, it := range m.data.Intents {
		for _, e := range it.Examples {
			if !examples {
				break
			}
			if c := m.canonOf(e); c != "" {
				if _, ok := ix.ex[c]; !ok {
					ix.ex[c] = it.Name
				}
			}
		}
		for _, n := range it.Negatives {
			if c := m.canonOf(n); c != "" {
				ix.neg[it.Name+"\x00"+c] = true
			}
		}
	}
	return ix
}

// nuExampleText trims an example and checks its size.
func nuExampleText(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= nuOverlayMaxText && !strings.ContainsAny(s, "\n\r")
}

// nuApplyOverlay merges ov into m (before the classifier trains). Intent
// names go in sorted order, so the result never depends on map order.
// Returns how many examples went in and why the rest did not. m is a model
// of the embedded grammar (the only one the runtime and the forge compile):
// an overlay stamped with its sha skips re-indexing the embedded examples.
func nuApplyOverlay(m *nuModel, ov *nuOverlay) (int, map[string]int) {
	skipped := map[string]int{}
	if m == nil || ov == nil || m.loadErr != nil {
		return 0, skipped
	}
	ix := nuIndexWith(m, ov.BaseSHA256 == "" || ov.BaseSHA256 != nuEmbeddedSHA())
	names := make([]string, 0, len(ov.Examples))
	for k := range ov.Examples {
		names = append(names, k)
	}
	sort.Strings(names)
	added := 0
	for _, name := range names {
		ii, ok := m.byName[name]
		if !ok {
			skipped["intent"] += len(ov.Examples[name])
			continue
		}
		if fgIsMutating(name) {
			skipped["mutating"] += len(ov.Examples[name])
			continue
		}
		per := 0
		for _, raw := range ov.Examples[name] {
			text, ok := nuExampleText(raw)
			c := ""
			if ok {
				c = m.canonOf(text)
			}
			switch {
			case !ok || c == "":
				skipped["invalid"]++
			case ix.neg[name+"\x00"+c]:
				skipped["negative"]++ // the grammar says explicitly it is not this intent
			case ix.ex[c] == name:
				skipped["duplicate"]++
			case ix.ex[c] != "":
				skipped["conflict"]++ // an example of another intent: never train both
			case added >= nuOverlayCap || per >= nuOverlayPerIntent:
				skipped["cap"]++
			default:
				m.data.Intents[ii].Examples = append(m.data.Intents[ii].Examples, text)
				ix.ex[c] = name
				added++
				per++
			}
		}
	}
	return added, skipped
}

// nuLoadOverlay applies the owner's overlay file to m, if any.
func nuLoadOverlay(m *nuModel) {
	p := nuOverlayPath()
	ov, err := nuReadOverlay(p)
	if err != nil {
		dbg("forge", map[string]any{"overlay": "ignorado", "path": p, "err": err.Error()})
		return
	}
	if ov == nil {
		return
	}
	n, skipped := nuApplyOverlay(m, ov)
	dbg("forge", map[string]any{"overlay": "carregado", "added": n, "skipped": skipped})
}
