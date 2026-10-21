package main

// approvals.go: the "a" (sempre) answer to a confirmation that carries a risk
// reason (fence, egress, supply chain, destructive git, curl|sh from a host).
// Plain "always" answers to prompts without a reason keep saving permission
// rules (perms.go); those cannot express "this host" or "this outside dir".
//
// Each approval is a (class, target) pair: egress/<host>, fence/<dir>,
// supply/<command>, git/<command>, pipe-shell/<host>. The store keeps a hash
// of the pair (so the same question is never asked twice in a project) and a
// redacted label for /approvals. It lives in memory and in
// <project>/.jazz/approvals.json. A repository could ship that file with
// approvals already in it, so the file only counts when its exact content was
// written by jazz (trust store kind "approvals", trust.go); the agent's own
// write/edit tools treat it as a protected path (safety.go).
//
// Secret reads, jazz's own config, protected files, suspected prompt
// injection, MCP rug-pulls and the catastrophic danger class (disk, home or
// root deletes, privilege escalation …) are never stored: they ask every time.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// apEntry is one remembered approval.
type apEntry struct {
	Key   string    `json:"key"`   // sha256(class \x00 target), 24 hex chars
	Class string    `json:"class"` // egress | fence | supply | git | pipe-shell
	Label string    `json:"label"` // redacted, for /approvals
	At    time.Time `json:"at"`
}

type apFile struct {
	Version   int       `json:"version"`
	Approvals []apEntry `json:"approvals"`
}

// apStore is the per-project approval store. A nil *apStore is a no-op.
type apStore struct {
	mu        sync.Mutex
	root      string
	loaded    bool
	ignored   bool // the file exists but jazz did not write it (or it was edited since)
	m         map[string]apEntry
	persisted bool // false: memory only (no project root)
}

const apTrustKind = "approvals"

func newAPStore(root string) *apStore {
	return &apStore{root: root, persisted: root != ""}
}

func apKey(class, target string) string {
	h := sha256.Sum256([]byte(class + "\x00" + target))
	return hex.EncodeToString(h[:12])
}

func (s *apStore) file() string { return filepath.Join(s.root, ".jazz", "approvals.json") }

// loadLocked reads the file once; an untrusted file is ignored.
func (s *apStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.m = map[string]apEntry{}
	if !s.persisted {
		return
	}
	b, err := os.ReadFile(s.file())
	if err != nil || len(b) == 0 {
		return
	}
	if !IsTrusted(s.root, apTrustKind, b) {
		s.ignored = true
		return
	}
	var f apFile
	if json.Unmarshal(b, &f) != nil {
		s.ignored = true
		return
	}
	for _, e := range f.Approvals {
		if e.Key != "" {
			s.m[e.Key] = e
		}
	}
}

// Has reports whether (class, target) was approved with "sempre".
func (s *apStore) Has(class, target string) bool {
	if s == nil || class == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	_, ok := s.m[apKey(class, target)]
	return ok
}

// Add remembers (class, target) for this project.
func (s *apStore) Add(class, target, label string) error {
	if s == nil || class == "" {
		return errors.New("aprovação sem classe")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	k := apKey(class, target)
	s.m[k] = apEntry{Key: k, Class: class, Label: oneLine(redactSecrets(label), 160), At: time.Now().UTC().Truncate(time.Second)}
	return s.saveLocked()
}

// List returns the approvals, oldest first.
func (s *apStore) List() (list []apEntry, ignored bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	for _, e := range s.m {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].At.Equal(list[j].At) {
			return list[i].At.Before(list[j].At)
		}
		return list[i].Key < list[j].Key
	})
	return list, s.ignored
}

// Clear forgets every approval (memory and file).
func (s *apStore) Clear() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.ignored, s.m = true, false, map[string]apEntry{}
	if !s.persisted {
		return nil
	}
	err := os.Remove(s.file())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return Trust(s.root, apTrustKind, nil)
}

func (s *apStore) saveLocked() error {
	if !s.persisted {
		return nil
	}
	f := apFile{Version: 1}
	for _, e := range s.m {
		f.Approvals = append(f.Approvals, e)
	}
	sort.Slice(f.Approvals, func(i, j int) bool { return f.Approvals[i].Key < f.Approvals[j].Key })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(s.file()), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(s.file(), b, 0o644); err != nil {
		return err
	}
	s.ignored = false
	return Trust(s.root, apTrustKind, b)
}

// approvals returns the agent's store (created on first use).
func (a *Agent) approvals() *apStore {
	a.apMu.Lock()
	defer a.apMu.Unlock()
	if a.ap == nil {
		root := ""
		if a.tools != nil {
			root = a.tools.Root
		}
		a.ap = newAPStore(root)
	}
	return a.ap
}
