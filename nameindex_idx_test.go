package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unsafe"
)

func niIdxOn(t testing.TB) {
	t.Helper()
	home := t.TempDir()
	if tt, ok := t.(*testing.T); ok {
		tt.Setenv("JAZZ_HOME", home)
	} else if b, ok := t.(*testing.B); ok {
		b.Setenv("JAZZ_HOME", home)
	}
	niPersist = true
	t.Cleanup(func() { niPersist = false })
}

// niSameAsFresh: ix holds exactly what a from-scratch walk would build.
func niSameAsFresh(t *testing.T, ix *niIndex, what string) {
	t.Helper()
	fresh := niFromListing(ix.root, niListWalk(ix.root), nil)
	if !reflect.DeepEqual(ix.files, fresh.files) {
		var a, b []string
		for _, f := range ix.files {
			a = append(a, f.Rel)
		}
		for _, f := range fresh.files {
			b = append(b, f.Rel)
		}
		t.Fatalf("%s: index differs from a fresh build\n got: %q\nwant: %q", what, a, b)
	}
	if !reflect.DeepEqual(ix.fast, fresh.fast) || ix.dominant != fresh.dominant || !reflect.DeepEqual(ix.exts, fresh.exts) {
		t.Fatalf("%s: derived data differs", what)
	}
	for _, f := range fresh.files {
		if _, ok := ix.relIndex(f.Rel); !ok || !ix.hasBase(f.Base) {
			t.Fatalf("%s: lookup of %s fails", what, f.Rel)
		}
	}
}

func niAge(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chtimes(p, old, old)
		}
		return nil
	})
}

func TestNiIdxPersistAndRefresh(t *testing.T) {
	niIdxOn(t)
	root := t.TempDir()
	giWrite(t, root, "main.go", "pkg/calc.go", "pkg/calc_test.go", "docs/Guia.MD", "web/app.tsx", "web/comp/Botao.tsx", "tests/test_x.py", "ação/olá.go")
	niAge(t, root)
	a := niBuild(root)
	niSameAsFresh(t, a, "cold")
	if _, err := os.Stat(niIdxPath(root)); err != nil {
		t.Fatal("no .idx written")
	}
	// second process: loaded as is (no directory changed, none racy)
	b := niLoadIdx(root)
	if b == nil {
		t.Fatal(".idx not loadable")
	}
	if d := b.dirtyDirs(); len(d) != 0 {
		t.Fatalf("dirty after load: %v", d)
	}
	niSameAsFresh(t, b, "loaded")
	c := niBuild(root)
	niSameAsFresh(t, c, "build from .idx")
	// changes: new file, removed file, new dir tree, renamed dir
	giWrite(t, root, "pkg/novo.go", "novo/x/y.go")
	os.Remove(filepath.Join(root, "main.go"))
	os.Rename(filepath.Join(root, "web", "comp"), filepath.Join(root, "web", "componentes"))
	d := niBuild(root)
	niSameAsFresh(t, d, "after changes")
	if _, ok := d.relIndex("web/componentes/Botao.tsx"); !ok {
		t.Fatal("renamed dir not seen")
	}
	// removed directory
	os.RemoveAll(filepath.Join(root, "novo"))
	niAge(t, root)
	niSameAsFresh(t, niBuild(root), "dir removed")
	// a corrupt .idx is ignored: a flipped byte, then a truncated file
	p := niIdxPath(root)
	raw, _ := os.ReadFile(p)
	raw[len(raw)-5] ^= 0x20
	os.WriteFile(p, raw, 0o600)
	if niLoadIdx(root) != nil {
		t.Fatal("corrupt .idx accepted")
	}
	raw[200] ^= 0xff
	raw[len(raw)-1] ^= 0xff
	os.WriteFile(p, raw[:len(raw)-3], 0o600)
	if niLoadIdx(root) != nil {
		t.Fatal("truncated .idx accepted")
	}
	niSameAsFresh(t, niBuild(root), "after corrupt .idx")
	// another root never reads this one
	if niLoadIdx(t.TempDir()) != nil {
		t.Fatal("foreign .idx")
	}
}

// TestNiIdxClosesGitGap: the untracked file the .git/index listing cannot
// see (TestGitIndexKnownGap) is found by the next build.
func TestNiIdxClosesGitGap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	niIdxOn(t)
	root := t.TempDir()
	giGit(t, root, "init", "-q")
	giWrite(t, root, "a/x.go", "b.go")
	giGit(t, root, "add", "-A")
	giWrite(t, root, "a/untracked.go")
	niAge(t, root)
	os.Chtimes(filepath.Join(root, ".git", "index"), time.Now(), time.Now())
	first := niBuild(root)
	if _, ok := first.relIndex("a/untracked.go"); ok {
		t.Skip("gap closed by the filesystem")
	}
	second := niBuild(root)
	if _, ok := second.relIndex("a/untracked.go"); !ok {
		t.Fatal("second build still misses the untracked file")
	}
	niSameAsFresh(t, second, "second build")
}

func TestNiIdxNoAllocPerFileOnLoad(t *testing.T) {
	niIdxOn(t)
	root := pbRepo5k(t)
	niAge(t, root)
	niBuild(root)
	allocs := testing.AllocsPerRun(5, func() {
		if niLoadIdx(root) == nil {
			t.Fatal("no idx")
		}
	})
	t.Logf("load of %d files: %.0f allocs", len(niBuild(root).files), allocs)
	if allocs > 1000 {
		t.Errorf("load allocates %v times for 4.9k files", allocs)
	}
}

// BenchmarkNiBuildIdx5k: niBuild when a valid .idx exists (the common
// case of every jazz -p after the first) vs a cold build.
func BenchmarkNiBuildIdx5k(b *testing.B) {
	niIdxOn(b)
	root := pbRepo5k(b)
	old := time.Now().Add(-time.Hour)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	niBuild(root)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		niBuild(root)
	}
}

// TestNiIdxLayoutSizes: the on-disk records have the same size on 32- and
// 64-bit targets (a 386 and an amd64 jazz may share one data dir).
func TestNiIdxLayoutSizes(t *testing.T) {
	for name, got := range map[string]uintptr{"hdr": unsafe.Sizeof(niIdxHdr{}), "rec": unsafe.Sizeof(niRec{}),
		"fast": unsafe.Sizeof(niFast{}), "dir": unsafe.Sizeof(niDirRec{})} {
		want := map[string]uintptr{"hdr": 64, "rec": 36, "fast": 12, "dir": 24}[name]
		if got != want {
			t.Errorf("sizeof %s = %d, want %d", name, got, want)
		}
	}
}
