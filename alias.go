package main

// alias.go: layer 1 of the forge design — the owner's PERSONAL shortcuts.
// An alias maps one whole request to an intent, instantly: "quando eu falar
// X é Y", [l] at the confirm box, or `jazz forge --add X Y`. It lives at
// dataDir()/forge/aliases.json, never ships, and is the opposite of the
// gated overlay (forge.go): no review, no gate, this machine only.
// `jazz forge --list` shows them, `--forget` removes.
//
// An alias classifies, never authorizes: a mutating target still hits the
// permission guardrail at execution. And it never feeds the forge — the
// gated grammar only learns from menu picks with provenance.
//
// File: {"version":1, "aliases":{"<canon>":{"text":..,"intent":..,"ts":..}}}

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	nuAliasVersion  = 1
	nuAliasMaxBytes = 256 << 10
	nuAliasCap      = 500 // the owner's list, not a training set
)

type nuAlias struct {
	Text   string `json:"text"`
	Intent string `json:"intent"`
	Ts     int64  `json:"ts"`
}

type nuAliasFile struct {
	Version int                `json:"version"`
	Aliases map[string]nuAlias `json:"aliases"` // canon -> alias
}

func nuAliasPath() string { return filepath.Join(dataDir(), "forge", "aliases.json") }

// nuReadAliases reads the file; a missing one is (empty, nil), an invalid
// one is an error the caller reports (never silently dropped).
func nuReadAliases() (*nuAliasFile, error) {
	p := nuAliasPath()
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &nuAliasFile{Version: nuAliasVersion, Aliases: map[string]nuAlias{}}, nil
		}
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, nuAliasMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > nuAliasMaxBytes {
		return nil, errors.New("aliases.json maior que 256 KiB")
	}
	var af nuAliasFile
	if err := json.Unmarshal(b, &af); err != nil {
		return nil, err
	}
	if af.Version != nuAliasVersion {
		return nil, errors.New("versão de aliases.json desconhecida")
	}
	if af.Aliases == nil {
		af.Aliases = map[string]nuAlias{}
	}
	return &af, nil
}

// nuWriteAliases writes atomically (temp + rename), mode 0600.
func nuWriteAliases(af *nuAliasFile) error {
	af.Version = nuAliasVersion
	b, err := json.MarshalIndent(af, "", " ")
	if err != nil {
		return err
	}
	p := nuAliasPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// nuAliasAdd records "when I say <text> I mean <intent>". The target is an
// intent name or — the spoken form — a phrase whose reading is an intent
// ("quando eu falar X é lista os arquivos" reads Y and takes list_files).
// The text must canonicalize to something — the alias is keyed on the
// canon the classifier sees.
func nuAliasAdd(text, intent string) (nuAlias, error) {
	m := nuGet()
	var al nuAlias
	if _, ok := m.byName[intent]; !ok || intent == "none" {
		if in := m.Parse(intent, nuWorldFromDef(m.data.World)); fgActs(in) {
			intent = in.Name // Y veio como frase: lê e usa a intenção dela
		} else {
			return al, fmt.Errorf("não conheço a intenção %q (jazz forge --list mostra o que sei)", intent)
		}
	}
	text = strings.TrimSpace(text)
	canon := m.canonOf(text)
	if canon == "" || len(text) > nuOverlayMaxText {
		return al, errors.New("apelido vazio, multilinha ou longo demais")
	}
	af, err := nuReadAliases()
	if err != nil {
		return al, err
	}
	if len(af.Aliases) >= nuAliasCap {
		return al, errors.New("limite de apelidos cheio — esqueça alguns com jazz forge --forget")
	}
	al = nuAlias{Text: text, Intent: intent, Ts: time.Now().Unix()}
	af.Aliases[canon] = al
	if err := nuWriteAliases(af); err != nil {
		return nuAlias{}, err
	}
	// instant: the live model takes it now, no restart
	if m.aliases == nil {
		m.aliases = map[string]string{}
	}
	m.aliases[canon] = intent
	return al, nil
}

// nuAliasForget removes an alias by its canon or exact text.
func nuAliasForget(q string) (nuAlias, error) {
	m := nuGet()
	af, err := nuReadAliases()
	if err != nil {
		return nuAlias{}, err
	}
	canon := m.canonOf(q)
	al, ok := af.Aliases[canon]
	if !ok {
		for c, a := range af.Aliases {
			if strings.EqualFold(strings.TrimSpace(q), a.Text) {
				canon, al, ok = c, a, true
				break
			}
		}
	}
	if !ok {
		return nuAlias{}, fmt.Errorf("nenhum apelido %q (jazz forge --list mostra os seus)", q)
	}
	delete(af.Aliases, canon)
	delete(m.aliases, canon)
	return al, nuWriteAliases(af)
}

// nuLoadAliases fills m.aliases from the file (validated against the
// grammar: an alias to an intent that no longer exists is skipped).
func nuLoadAliases(m *nuModel) {
	af, err := nuReadAliases()
	if err != nil {
		dbg("forge", map[string]any{"aliases": "ignorado", "err": err.Error()})
		return
	}
	for canon, a := range af.Aliases {
		if _, ok := m.byName[a.Intent]; !ok || a.Intent == "none" || canon == "" {
			continue
		}
		if m.aliases == nil {
			m.aliases = map[string]string{}
		}
		m.aliases[canon] = a.Intent
	}
}

// nuAliasRe is "quando eu falar/disser X é Y" — the spoken form of --add.
var nuAliasRe = lazyRx(`(?i)^quando eu (?:falar|disser|escrever|pedir|mandar)\s+(.{1,120}?)\s+(?:é|eh|significa|quer dizer)\s+(\S.{0,58}?)[.!?]?$`)

// nuAliasRequest parses the spoken alias form; ok = the phrase matched
// (the intent still has to exist — the caller reports it).
func nuAliasRequest(text string) (phrase, intent string, ok bool) {
	m := nuAliasRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", "", false
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`), strings.TrimSpace(m[2]), true
}
