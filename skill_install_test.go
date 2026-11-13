package main

import (
	"bytes"
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
