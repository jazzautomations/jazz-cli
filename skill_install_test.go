package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func siMkSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\nRun scripts/setup.py first.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "setup.py"), []byte("print('ok')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSiParse(t *testing.T) {
	cases := map[string]siSource{
		"https://github.com/jazzautomations/video-forge":                     {URL: "https://github.com/jazzautomations/video-forge.git"},
		"https://github.com/anthropics/skills/tree/main/skills/pdf":          {URL: "https://github.com/anthropics/skills.git", Ref: "main", Sub: "skills/pdf"},
		"https://github.com/anthropics/skills/blob/main/skills/pdf/SKILL.md": {URL: "https://github.com/anthropics/skills.git", Ref: "main", Sub: "skills/pdf"},
		"jazzautomations/video-forge":                                        {URL: "https://github.com/jazzautomations/video-forge.git"},
		"git@github.com:jazzautomations/video-forge.git":                     {URL: "git@github.com:jazzautomations/video-forge.git"},
	}
	for in, want := range cases {
		got, err := siParse(in)
		if err != nil || got != want {
			t.Errorf("siParse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := siParse("isso não é url"); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestSiInstallLocalRepoWithManySkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	src := t.TempDir()
	siMkSkill(t, filepath.Join(src, "skills", "pdf"), "pdf", "Work with PDFs")
	siMkSkill(t, filepath.Join(src, "skills", "evil"), "../../evil name", "Bad name")
	// a symlink inside a skill must not be copied (it could point anywhere)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "skills", "pdf", "link")); err != nil {
		t.Fatal(err)
	}
	var sum string
	found, err := siInstall(t.Context(), src, siUserDir(), false, func(s string) bool { sum = s; return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || !strings.Contains(sum, "scripts que ela pode mandar rodar: scripts/setup.py") {
		t.Fatalf("found %+v\nsummary:\n%s", found, sum)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "pdf", "SKILL.md")); err != nil {
		t.Error("pdf not installed")
	}
	if _, err := os.Lstat(filepath.Join(home, "skills", "pdf", "link")); err == nil {
		t.Error("symlink copied")
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "evil-name", "SKILL.md")); err != nil {
		t.Error("an unsafe name must be sanitized inside the skills dir")
	}
	if fi, err := os.Stat(filepath.Join(home, "skills", "pdf", "scripts", "setup.py")); err != nil || fi.Mode()&0o100 == 0 {
		t.Error("script must keep its exec bit")
	}
	// findSkills (the prompt list) sees it
	var names []string
	for _, s := range findSkills(t.TempDir()) {
		names = append(names, s.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "pdf") {
		t.Errorf("findSkills = %v", names)
	}
	// installed again without --force: refused; list shows it
	if _, err := siInstall(t.Context(), src, siUserDir(), false, nil); err == nil || !strings.Contains(err.Error(), "já está instalada") {
		t.Errorf("reinstall without force: %v", err)
	}
	var out bytes.Buffer
	if err := cmdSkill(t.TempDir(), []string{"list"}, strings.NewReader(""), &out); err != nil || !strings.Contains(out.String(), "pdf") {
		t.Errorf("list: %v %q", err, out.String())
	}
	if err := cmdSkill(t.TempDir(), []string{"rm", "pdf"}, strings.NewReader(""), &out); err != nil {
		t.Errorf("rm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "pdf")); err == nil {
		t.Error("rm left the skill")
	}
}

// A git URL (here a local bare repo via file://): cloned, origin recorded,
// found again by URL; a cancelled confirm installs nothing.
func TestSiInstallFromGitAndOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	repo := t.TempDir()
	siMkSkill(t, repo, "video-forge", "Create polished videos with code")
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x"}} {
		c := exec.Command("git", a...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	url := "file://" + repo
	if _, err := siInstall(t.Context(), url, siUserDir(), false, func(string) bool { return false }); err == nil {
		t.Error("a cancelled install must fail")
	}
	if _, err := os.Stat(filepath.Join(siUserDir(), "video-forge")); err == nil {
		t.Error("cancelled install wrote files")
	}
	if _, err := siInstall(t.Context(), url, siUserDir(), false, nil); err != nil {
		t.Fatal(err)
	}
	if d := siInstalledFrom(t.TempDir(), url); filepath.Base(d) != "video-forge" {
		t.Errorf("siInstalledFrom = %q", d)
	}
}

func TestSiChatURL(t *testing.T) {
	if u := siChatURL("você consegue ler essa skill e criar um video https://github.com/jazzautomations/video-forge."); u != "https://github.com/jazzautomations/video-forge" {
		t.Errorf("got %q", u)
	}
	if u := siChatURL("olha esse repo https://github.com/a/b"); u != "" {
		t.Errorf("no skill word, no offer: %q", u)
	}
}

// -p: "instala a skill <url>" installs (no prompt to answer) and points the model at it.
func TestSiHeadlessInstall(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	src := t.TempDir()
	siMkSkill(t, src, "notes", "Take notes")
	// a local folder is not a chat URL: exercise the installed-by-URL path via a fake origin
	found, err := siInstall(t.Context(), src, siUserDir(), false, nil)
	if err != nil || len(found) != 1 {
		t.Fatal(err)
	}
	rec := `{"source":"https://github.com/x/notes","url":"https://github.com/x/notes.git","installed":"now"}`
	if err := os.WriteFile(filepath.Join(found[0].Dir, siOrigin), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	var errw bytes.Buffer
	n := siHeadlessTurn(t.TempDir(), "usa a skill https://github.com/x/notes pra anotar isso", &errw)
	if !strings.Contains(n, filepath.Join(found[0].Dir, "SKILL.md")) {
		t.Errorf("installed skill must be pointed at: %q", n)
	}
	n = siHeadlessTurn(t.TempDir(), "olha a skill https://github.com/y/other", &errw)
	if !strings.Contains(n, "jazz skill install https://github.com/y/other") {
		t.Errorf("not installed, no install verb: %q", n)
	}
}

// siBareRepo: a bare repo (served via file://) with skills/pdf and a big
// unrelated folder; returns its file:// URL.
func siBareRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "r.git")
	siMkSkill(t, filepath.Join(work, "skills", "pdf"), "pdf", "Work with PDFs")
	if err := os.MkdirAll(filepath.Join(work, "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "media", "big.bin"), bytes.Repeat([]byte("x"), 1<<16), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, a ...string) {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	run(work, "init", "-q")
	run(work, "checkout", "-q", "-b", "main")
	run(work, "add", "-A")
	run(work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x")
	run(work, "clone", "-q", "--bare", work, bare)
	run(bare, "config", "uploadpack.allowFilter", "true")
	return "file://" + bare
}

// /tree/<ref>/<sub>: only the subfolder lands on disk (sparse checkout), the
// clone lives under jazz's data dir (not a tmpfs /tmp), progress says what is
// downloaded, and the temp clone is gone afterwards.
func TestSiFetchSparseSubfolder(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	url := siBareRepo(t)
	var said []string
	ctx := siWithProgress(t.Context(), func(m string) { said = append(said, m) })
	base, commit, cleanup, err := siFetch(ctx, siSource{URL: url, Ref: "main", Sub: "skills/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Dir(filepath.Dir(base))
	if _, err := os.Stat(filepath.Join(base, "SKILL.md")); err != nil {
		t.Errorf("subfolder not checked out: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "media")); err == nil {
		t.Error("sparse checkout wrote the unrelated folder")
	}
	if commit == "" {
		t.Error("commit not recorded")
	}
	if !pathIn(base, filepath.Join(dataDir(), "tmp")) {
		t.Errorf("clone must live under the data dir, got %s", base)
	}
	if len(said) != 1 || !strings.Contains(said[0], "baixando") || !strings.Contains(said[0], "(subpasta skills/pdf)") || strings.Contains(said[0], "file://") {
		t.Errorf("progress = %q", said)
	}
	cleanup()
	if ents, _ := os.ReadDir(filepath.Join(dataDir(), "tmp")); len(ents) != 0 {
		t.Errorf("temp clone left behind: %v", ents)
	}
	// a subfolder that isn't there: clear error, nothing left
	if _, _, _, err := siFetch(t.Context(), siSource{URL: url, Sub: "skills/nope"}); err == nil || !strings.Contains(err.Error(), "não existe") {
		t.Errorf("missing subfolder: %v", err)
	}
	if _, _, _, err := siFetch(t.Context(), siSource{URL: url, Sub: "../x"}); err == nil {
		t.Error("a subfolder with .. must be refused")
	}
	if ents, _ := os.ReadDir(filepath.Join(dataDir(), "tmp")); len(ents) != 0 {
		t.Errorf("temp clone left behind after errors: %v", ents)
	}
}

// a sparse fetch is what siFind and the install see
func TestSiInstallSparseFind(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	url := siBareRepo(t)
	base, commit, cleanup, err := siFetch(t.Context(), siSource{URL: url, Sub: "skills/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	found, err := siFind(base)
	if err != nil || len(found) != 1 || found[0].Name != "pdf" || commit == "" {
		t.Fatalf("found %+v %v", found, err)
	}
}

// a cancelled context (Ctrl+C in `jazz skill install`) stops git and leaves
// no temp clone
func TestSiFetchCancelledCleansUp(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	url := siBareRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _, err := siFetch(ctx, siSource{URL: url, Sub: "skills/pdf"})
	if err == nil || !strings.Contains(err.Error(), "cancelado") {
		t.Errorf("cancelled fetch: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(dataDir(), "tmp")); len(ents) != 0 {
		t.Errorf("temp clone left behind: %v", ents)
	}
}

// a bad repo is "não encontrado ou privado", not git's username prompt error
func TestSiBadRepoMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	missing := "file://" + filepath.Join(t.TempDir(), "nope.git")
	for _, sub := range []string{"", "skills/pdf"} {
		_, _, _, err := siFetch(t.Context(), siSource{URL: missing, Sub: sub})
		if err == nil || !strings.Contains(err.Error(), "repositório não encontrado ou privado") {
			t.Errorf("sub %q: %v", sub, err)
		}
	}
	src := siSource{URL: "https://github.com/x/nao-existe.git"}
	raw := errors.New("git clone: exit status 128: fatal: could not read Username for 'https://github.com': terminal prompts disabled")
	if got := siGitErr(raw, src).Error(); !strings.Contains(got, "repositório não encontrado ou privado: github.com/x/nao-existe") || strings.Contains(got, "Username") {
		t.Errorf("translated = %q", got)
	}
	raw = errors.New("git clone: exit status 128: warning: Could not find remote branch dev to clone. fatal: Remote branch dev not found in upstream origin")
	if got := siGitErr(raw, siSource{URL: src.URL, Ref: "dev"}).Error(); !strings.Contains(got, `"dev" não existe`) {
		t.Errorf("missing ref = %q", got)
	}
	if !siFatalClone(raw) {
		t.Error("a missing ref must not retry with a full clone")
	}
}

// a skill jazz installed now is not "already installed" for the model
func TestSiNoteFresh(t *testing.T) {
	f := []siFound{{Name: "video", Desc: "videos", Dir: "/x/video"}}
	if n := siNote(f, true); !strings.Contains(n, "just now, at the user's request") || !strings.Contains(n, "NOT installed before") {
		t.Errorf("fresh note = %q", n)
	}
	if n := siNote(f, false); strings.Contains(n, "just now") {
		t.Errorf("already-installed note = %q", n)
	}
}

// jazz skill install prints what it downloads
func TestCmdSkillInstallProgress(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	url := siBareRepo(t)
	var out bytes.Buffer
	if err := cmdSkill(t.TempDir(), []string{"install", url, "--yes"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "baixando ") || !strings.Contains(out.String(), "instalada: pdf") {
		t.Errorf("out = %q", out.String())
	}
}
