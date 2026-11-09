package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A text-kind slot gathers the words no rule explained and marks them taken;
// when a quoted word then wins the whole value, the gathered words must be
// released — otherwise the next slot (the file) silently stays empty.
// ("conta ocorrências f.txt \"ana\"": pattern=ana AND target=f.txt)
func TestNLUQuotedReleasesHeldWords(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"v5_text_count.txt", "v5_text_extract.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := nuGet()
	w := nuRuntimeWorld(root, nil)
	for _, c := range []struct {
		text, wantT, wantP string
	}{
		{`conta ocorrências v5_text_count.txt "ana"`, "v5_text_count.txt", "ana"},
		{`extrai padrões v5_text_extract.txt "email"`, "v5_text_extract.txt", "email"},
	} {
		in := m.Parse(c.text, w)
		if in.Slots["file"] != c.wantT || in.Slots["pattern"] != c.wantP {
			t.Errorf("%q slots=%v, want file=%q pattern=%q", c.text, in.Slots, c.wantT, c.wantP)
		}
	}
}
