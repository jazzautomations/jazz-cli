package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessContents(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+":"+m.Content)
	}
	return out
}

// Every entry has an id and its parent; a rewind starts a branch and keeps
// the abandoned messages in the file; linear replay and tree walk agree.
func TestSessionTreeRewindBranches(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	s, err := NewSession(t.TempDir(), "fake/m1", "SYS")
	if err != nil {
		t.Fatal(err)
	}
	s.Append(Message{Role: "user", Content: "a"})
	s.AppendMeta(Message{Role: "assistant", Content: "A"}, &sessMsgMeta{Model: "fake/m1", Provider: "fake", Stop: "stop", Usage: Usage{In: 10, Out: 2}, LatencyMS: 5})
	s.Append(Message{Role: "user", Content: "b"})
	s.Append(Message{Role: "assistant", Content: "B"})
	s.Rewind(2)
	s.Append(Message{Role: "user", Content: "c"})
	s.Close()
	ents, _ := sessReadEntries(s.Path)
	seen := map[string]bool{}
	for i, e := range ents {
		if e.ID == "" || seen[e.ID] {
			t.Fatalf("entry %d: bad id %q", i, e.ID)
		}
		seen[e.ID] = true
		if i > 0 && !seen[e.ParentID] {
			t.Fatalf("entry %d: parent %q not before it", i, e.ParentID)
		}
	}
	if ents[2].Usage == nil || ents[2].Usage.In != 10 || ents[2].Provider != "fake" || ents[2].Stop != "stop" {
		t.Errorf("assistant metadata not recorded: %+v", ents[2])
	}
	// the rewind entry hangs off the 2nd message, not off "B"
	rw := ents[5]
	if rw.T != "rewind" || rw.ParentID != ents[2].ID {
		t.Fatalf("rewind parent = %q, want %q", rw.ParentID, ents[2].ID)
	}
	s1, msgs, _, _, err := LoadSession(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close() // Windows cannot remove a file with an open handle
	if got := strings.Join(sessContents(msgs), ","); got != "user:a,assistant:A,user:c" {
		t.Fatalf("tree walk = %s", got)
	}
	// branch back to "B" (the abandoned branch) and continue there
	s2, msgs2, _, _, err := LoadSessionAt(s.Path, ents[4].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sessContents(msgs2), ","); got != "user:a,assistant:A,user:b,assistant:B" {
		t.Fatalf("branch = %s", got)
	}
	s2.Append(Message{Role: "user", Content: "d"})
	s2.Close()
	s3, msgs3, _, _, _ := LoadSession(s.Path)
	s3.Close()
	if got := strings.Join(sessContents(msgs3), ","); got != "user:a,assistant:A,user:b,assistant:B,user:d" {
		t.Fatalf("after branch = %s", got)
	}
	lines, err := sessTreeLines(s.Path)
	if err != nil || len(lines) != 6 {
		t.Fatalf("tree: %v\n%s", err, strings.Join(lines, "\n"))
	}
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "○ "+ents[6].ID) || !strings.Contains(all, "← atual") {
		t.Errorf("tree marks:\n%s", all)
	}
	if _, _, _, _, err := LoadSessionAt(s.Path, "nope1234"); err == nil {
		t.Error("unknown entry must fail")
	}
}

// v1 files (no ids) are migrated on read with the old linear semantics,
// and new entries appended to them chain onto the migrated ids.
func TestSessionTreeMigratesV1(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "old.jsonl")
	lines := []string{
		jsonArgs(sessEntry{T: "meta", Model: "fake/m1", System: "SYS"}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "user", Content: "a"}}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "assistant", Content: "A"}}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "user", Content: "b"}}),
		jsonArgs(sessEntry{T: "rewind", N: 2}),
		jsonArgs(sessEntry{T: "compact", Summary: "sum"}),
		jsonArgs(sessEntry{T: "msg", Msg: &Message{Role: "user", Content: "<summary>sum</summary>"}}),
		`{"t":"msg","msg":{"role":"user","con`, // torn
	}
	os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o600)
	s, msgs, model, sys, err := LoadSession(p)
	if err != nil || model != "fake/m1" || sys != "SYS" {
		t.Fatal(err, model, sys)
	}
	if got := strings.Join(sessContents(msgs), ","); got != "user:<summary>sum</summary>" {
		t.Fatalf("v1 replay = %s", got)
	}
	s.Append(Message{Role: "assistant", Content: "next"})
	s.Close()
	b, _ := os.ReadFile(p)
	if !bytes.Contains(b, []byte(`"parentId":"L7"`)) {
		t.Errorf("new entry must chain onto the migrated id L7:\n%s", b)
	}
	s2, msgs, _, _, _ := LoadSession(p)
	s2.Close() // Windows cannot delete the TempDir with the file open
	if len(msgs) != 2 || msgs[1].Content != "next" {
		t.Fatalf("after append: %v (the torn line must not swallow it)", sessContents(msgs))
	}
}
