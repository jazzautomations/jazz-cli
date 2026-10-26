package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func giGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func giWrite(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func giSame(t *testing.T, root, what string) niListing {
	t.Helper()
	g, ok := niListGit(root)
	if !ok {
		t.Fatalf("%s: index not usable", what)
	}
	w := niListWalk(root)
	if !slices.Equal(g.files, w.files) {
		t.Fatalf("%s: listing from .git/index differs from the walk\n git: %q\nwalk: %q", what, g.files, w.files)
	}
	return g
}

// TestGitIndexRealRepo: on this repository (a real index, possibly a linked
// worktree whose .git is a file) the listing equals the walk.
func TestGitIndexRealRepo(t *testing.T) {
	root, _ := os.Getwd()
	if giGitDir(root) == "" {
		t.Skip("not a git work tree")
	}
	if _, err := os.Stat(filepath.Join(giGitDir(root), "index")); err != nil {
		t.Skip("no index")
	}
	// a developer tree has untracked files of any age: those the index
	// cannot see must sit in directories marked unverified (Read=false)
	g, ok := niListGit(root)
	if !ok {
		t.Fatal("index not usable")
	}
	w := niListWalk(root)
	have := map[string]bool{}
	for _, f := range g.files {
		have[f] = true
	}
	read := map[string]bool{}
	for _, d := range g.dirs {
		read[d.Rel] = d.Read
	}
	inWalk := map[string]bool{}
	for _, f := range w.files {
		inWalk[f] = true
		if !have[f] && read[niParent(f)] {
			t.Errorf("%s missing although its directory was read", f)
		}
	}
	for _, f := range g.files {
		if !inWalk[f] {
			t.Errorf("%s listed but not on disk", f)
		}
	}
	l := g
	if len(l.files) < 100 {
		t.Fatalf("only %d files", len(l.files))
	}
	t.Logf("%d files, %d dirs (%d read)", len(l.files), len(l.dirs), giCountRead(l.dirs))
}

// giAgeDirs backdates every directory (not .git) to an hour ago: a work
// tree untouched since the index was written.
func giAgeDirs(t testing.TB, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	var dirs []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			dirs = append(dirs, p)
		}
		return nil
	})
	for _, d := range dirs {
		os.Chtimes(d, old, old)
	}
}

func giCountRead(d []niDirState) int {
	n := 0
	for _, x := range d {
		if x.Read {
			n++
		}
	}
	return n
}

func TestGitIndexScenarios(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, ver := range []string{"2", "3", "4"} {
		t.Run("v"+ver, func(t *testing.T) {
			root := t.TempDir()
			giGit(t, root, "init", "-q")
			giWrite(t, root, "README.md", "a.go", "a/b.go", "a/c/d_test.go", "a.b/x.txt", "node_modules/m/i.js",
				".github/workflows/ci.yml", ".hidden/h.txt", ".env.example", "docs/guia.md", "zz/del.go", "tests/t1.py")
			giGit(t, root, "add", "-A", "-f")
			giGit(t, root, "commit", "-q", "-m", "x")
			if ver == "3" { // extended flags: intent-to-add
				giWrite(t, root, "ita.go")
				giGit(t, root, "add", "-N", "ita.go")
			}
			giGit(t, root, "update-index", "--index-version", ver)
			giAgeDirs(t, root)
			// clean tree: only the root and directories with no tracked file
			// of their own are read
			if l := giSame(t, root, "clean v"+ver); giCountRead(l.dirs) > 3 || giCountRead(l.dirs) >= len(l.dirs) {
				t.Errorf("clean tree: %d of %d directories read", giCountRead(l.dirs), len(l.dirs))
			}
			// untracked file, new untracked tree, deletion, symlink
			time.Sleep(10 * time.Millisecond)
			giWrite(t, root, "a/novo.go", "novo/dir/n.py", "novo/top.md")
			os.Remove(filepath.Join(root, "zz", "del.go"))
			os.Symlink("a.go", filepath.Join(root, "link.go"))
			giSame(t, root, "dirty v"+ver)
		})
	}
}

func TestGitIndexSubmoduleAndSha256(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	giGit(t, root, "init", "-q")
	giWrite(t, root, "main.go", "pkg/p.go")
	sub := filepath.Join(root, "sub")
	os.MkdirAll(sub, 0o755)
	giGit(t, sub, "init", "-q")
	giWrite(t, sub, "s.go", "inner/i.go")
	giGit(t, sub, "add", "-A")
	giGit(t, sub, "commit", "-q", "-m", "s")
	giGit(t, root, "add", "-A")
	giGit(t, root, "commit", "-q", "-m", "x")
	giAgeDirs(t, root)
	l := giSame(t, root, "gitlink")
	if !slices.Contains(l.files, "sub/inner/i.go") {
		t.Errorf("submodule files missing: %q", l.files)
	}
	r2 := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--object-format=sha256")
	cmd.Dir = r2
	if cmd.Run() != nil {
		t.Skip("git without sha256 support")
	}
	giWrite(t, r2, "x.go", "y/z.go")
	giGit(t, r2, "add", "-A")
	giSame(t, r2, "sha256")
}

// TestGitIndexKnownGap documents the one case the index cannot see: an
// untracked file older than the index in a directory not changed since.
// The directory is recorded as unverified (Read=false) so the persisted
// index reads it on the next build.
func TestGitIndexKnownGap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	giGit(t, root, "init", "-q")
	giWrite(t, root, "a/x.go", "b.go")
	giGit(t, root, "add", "-A")
	giWrite(t, root, "a/untracked.go")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "a"), old, old)
	os.Chtimes(filepath.Join(root), old, old)
	g, ok := niListGit(root)
	if !ok {
		t.Fatal("no index")
	}
	if slices.Contains(g.files, "a/untracked.go") {
		t.Skip("gap closed by the filesystem; nothing to document")
	}
	for _, d := range g.dirs {
		if d.Rel == "a" && d.Read {
			t.Fatal("directory a marked as read")
		}
	}
}

func TestNiWalkLess(t *testing.T) {
	in := []string{"a.go", "a/b", "a-b", "a/c/d", "a/b.go", "b", "A", "a/b/c"}
	got := slices.Clone(in)
	slices.SortFunc(got, func(x, y string) int {
		if niWalkLess(x, y) {
			return -1
		}
		if niWalkLess(y, x) {
			return 1
		}
		return 0
	})
	want := "A a/b a/b/c a/b.go a/c/d a-b a.go b"
	if strings.Join(got, " ") != want {
		t.Errorf("walk order = %q, want %q", strings.Join(got, " "), want)
	}
}

// BenchmarkNiList5k: the file list of the 5k tree from the walk and from
// .git/index (directories untouched since the index was written).
func BenchmarkNiList5k(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("no git")
	}
	root := pbRepo5k(b)
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	giAgeDirs(b, root)
	b.Run("walk", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			niListWalk(root)
		}
	})
	b.Run("gitindex", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := niListGit(root); !ok {
				b.Fatal("index not usable")
			}
		}
	})
}
