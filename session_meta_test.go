package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// smWrite writes a session file with n user/assistant exchanges.
func smWrite(tb testing.TB, dir, id, title string, n int, mod time.Time) string {
	tb.Helper()
	var b strings.Builder
	b.WriteString(`{"t":"meta","v":2,"model":"m1","cwd":"/proj"}` + "\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"t":"msg","msg":{"role":"user","content":"%s %d"},"usage":{"in":10,"out":1}}`+"\n", title, i)
		fmt.Fprintf(&b, `{"t":"msg","msg":{"role":"assistant","content":"resposta %d %s"}}`+"\n", i, strings.Repeat("x", 200))
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		tb.Fatal(err)
	}
	os.Chtimes(p, mod, mod)
	return p
}

func TestSessMetaCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Add(-time.Hour).Truncate(time.Second)
	p1 := smWrite(t, dir, "s1", "primeira", 2, now)
	smWrite(t, dir, "s2", "segunda", 3, now.Add(time.Minute))
	os.WriteFile(filepath.Join(dir, "empty.jsonl"), []byte(`{"t":"meta"}`+"\n"), 0o600)
	os.Chtimes(filepath.Join(dir, "empty.jsonl"), now.Add(time.Hour), now.Add(time.Hour)) // newest, but no messages

	list := sessListDir(dir)
	if len(list) != 2 || list[0].ID != "s2" || list[1].ID != "s1" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Msgs != 6 || list[0].Model != "m1" || list[0].Cwd != "/proj" || list[0].Usage.In != 30 {
		t.Fatalf("summary = %+v", list[0])
	}
	if _, err := os.Stat(sessMetaPath(p1)); err != nil {
		t.Fatal("no .meta written")
	}
	// the second listing comes from the side files: same answer
	again := sessListDir(dir)
	for i := range list {
		if list[i] != again[i] {
			t.Errorf("cached %d differs:\n%+v\n%+v", i, list[i], again[i])
		}
	}
	// a current side file is used as is (proved by a planted title)...
	m := sessMetaPath(p1)
	b, _ := os.ReadFile(m)
	os.WriteFile(m, []byte(strings.Replace(string(b), list[1].Title, "plantado", 1)), 0o600)
	if got := sessListDir(dir)[1].Title; got != "plantado" {
		t.Errorf("side file not used: %q", got)
	}
	// ...and a stale one (the session grew) is ignored and rewritten
	f, _ := os.OpenFile(p1, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"t":"msg","msg":{"role":"user","content":"mais"}}` + "\n")
	f.Close()
	os.Chtimes(p1, now.Add(2*time.Minute), now.Add(2*time.Minute))
	l3 := sessListDir(dir)
	if l3[0].ID != "s1" || l3[0].Msgs != 5 || l3[0].Title == "plantado" {
		t.Errorf("stale side file used: %+v", l3[0])
	}
	// newest-only: skips the message-less file, agrees with the list
	if si, ok := sessNewest(dir); !ok || si != l3[0] {
		t.Errorf("sessNewest = %+v %v, want %+v", si, ok, l3[0])
	}
	if _, ok := sessNewest(t.TempDir()); ok {
		t.Error("empty dir has a newest session")
	}
}

// BenchmarkSessContinue300: `jazz -c` with 300 sessions in the folder.
func BenchmarkSessContinue300(b *testing.B) {
	dir := b.TempDir()
	t0 := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 300; i++ {
		smWrite(b, dir, fmt.Sprintf("s%03d", i), "pedido", 40, t0.Add(time.Duration(i)*time.Minute))
	}
	b.Run("newest", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sessNewest(dir)
		}
	})
	b.Run("list-cached", func(b *testing.B) {
		sessListDir(dir)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sessListDir(dir)
		}
	})
	b.Run("list-parse", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, c := range sessFiles(dir) {
				info := SessionInfo{}
				sessParseSummary(c.path, &info)
			}
		}
	})
}
