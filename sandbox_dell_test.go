package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Dell layout (antiX, bwrap 0.11): HOME=/home/user, config at
// ~/.config/jazz-cli (JAZZ_HOME and XDG_* unset), data at
// ~/.local/share/jazz-cli, the jazz binary in ~/.local/bin, projects under
// ~/jazz-eval-v2. The fake home lives in a temp dir; paths are compared
// relative to it.
func sbDellHome(t *testing.T) (home string) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "jzsbx-dell-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home = filepath.Join(base, "home", "jazz")
	for _, d := range []string{".config/jazz-cli/skills", ".local/share/jazz-cli/spill", ".local/share/jazz-cli/jobs",
		".local/share/jazz-cli/sessions", ".local/bin", ".ssh", "jazz-eval-v2/tasks/t01"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for f, c := range map[string]string{".config/jazz-cli/auth.json": `{"k":"SECRET"}`, ".config/jazz-cli/config.json": "{}",
		".local/bin/jazz": "#!/bin/sh\n", ".ssh/id_ed25519": "SECRET", ".local/share/jazz-cli/sessions/s.json": "SECRET"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"JAZZ_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "GOPATH", "GOMODCACHE", "CARGO_HOME",
		"RUSTUP_HOME", "PYENV_ROOT", "NVM_DIR", "GOCACHE", "npm_config_cache", "PIP_CACHE_DIR", "YARN_CACHE_FOLDER"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", home)
	return sbReal(home)
}

// sbBwrapView models bwrap's documented mount semantics over argv: mounts
// apply in order, and a later mount on a path or on one of its parents
// covers everything mounted there before. It returns the host path whose
// content a sandboxed process sees at p, or "" when p is inside a tmpfs (or
// /dev/null), i.e. host content is hidden.
func sbBwrapView(args []string, p string) string {
	type mnt struct{ dest, src string } // src "" = tmpfs / dev null
	var ms []mnt
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ro-bind", "--bind", "--ro-bind-try", "--bind-try":
			src := args[i+1]
			if src == "/dev/null" {
				src = ""
			}
			ms = append(ms, mnt{args[i+2], src})
			i += 2
		case "--tmpfs", "--dev", "--proc":
			ms = append(ms, mnt{args[i+1], ""})
			i++
		case "--chdir":
			i++
		}
	}
	for k := len(ms) - 1; k >= 0; k-- {
		m := ms[k]
		if p == m.dest || m.dest == "/" || strings.HasPrefix(p, m.dest+"/") {
			if m.src == "" {
				return ""
			}
			if m.dest == "/" {
				return p // --ro-bind / /
			}
			return m.src + strings.TrimPrefix(p, m.dest)
		}
	}
	return ""
}

func TestSandboxBwrapDellScenario(t *testing.T) {
	home := sbDellHome(t)
	j := func(rel string) string { return filepath.Join(home, rel) }
	cfgDir, data := j(".config/jazz-cli"), j(".local/share/jazz-cli")
	if sbReal(configDir()) != cfgDir || sbReal(dataDir()) != data {
		t.Fatalf("config/data dirs: %s %s", configDir(), dataDir())
	}
	cases := []struct {
		name string
		proj string
	}{
		{"project under HOME (eval)", j("jazz-eval-v2/tasks/t01")},
		{"project == HOME", home},
		{"project outside HOME", sbReal(t.TempDir())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb := sbNew("auto", sbBwrap, "/usr/bin/bwrap", true, []string{c.proj})
			sb.Tmp = sbReal(t.TempDir())
			a := sbBwrapArgs(sb, c.proj)
			t.Logf("argv: bwrap %s -- /bin/sh", strings.Join(a, " "))
			hidden := []string{j(".config/jazz-cli/auth.json"), j(".config/jazz-cli/config.json"), j(".ssh/id_ed25519"),
				j(".local/share/jazz-cli/sessions/s.json")}
			if c.proj != home {
				hidden = append(hidden, j("notes.txt"))
			}
			for _, p := range hidden {
				if v := sbBwrapView(a, p); v != "" {
					t.Errorf("%s visible inside the sandbox (from %s)", p, v)
				}
			}
			visible := []string{c.proj + "/main.go", j(".local/bin/jazz"), j(".config/jazz-cli/skills/x/SKILL.md"),
				j(".local/share/jazz-cli/spill/out.txt"), j(".local/share/jazz-cli/jobs/1.log"), sb.Tmp + "/f", "/usr/bin/env"}
			for _, p := range visible {
				if v := sbBwrapView(a, p); v != p {
					t.Errorf("%s must be visible from the host, got %q", p, v)
				}
			}
			// the project must be bound read-write after HOME is hidden
			if !strings.Contains(" "+strings.Join(a, " ")+" ", " --bind "+c.proj+" "+c.proj+" ") {
				t.Errorf("project not bound rw: %v", a)
			}
		})
	}
}

// The exact argv for the Dell eval scenario, so a regression in order or
// flags is visible in the diff (paths relative to the fake home).
func TestSandboxBwrapDellArgv(t *testing.T) {
	home := sbDellHome(t)
	proj := filepath.Join(home, "jazz-eval-v2/tasks/t01")
	sb := sbNew("strict", sbBwrap, "/usr/bin/bwrap", false, []string{proj})
	sb.Tmp = "/tmp/jazz-sbx-1000/42-x"
	a := sbBwrapArgs(sb, proj)
	got := strings.ReplaceAll(strings.Join(a, " "), home, "~")
	if _, err := os.Stat("/run/user/" + itoa(os.Getuid())); err == nil {
		got = strings.Replace(got, " --tmpfs /run/user/"+itoa(os.Getuid()), "", 1)
	}
	want := "--die-with-parent --unshare-pid --new-session --unshare-net --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp" +
		" --tmpfs ~" +
		" --ro-bind-try ~/go ~/go --ro-bind-try ~/sdk ~/sdk --ro-bind-try ~/.cargo ~/.cargo --ro-bind-try ~/.rustup ~/.rustup" +
		" --ro-bind-try ~/.pyenv ~/.pyenv --ro-bind-try ~/.nvm ~/.nvm --ro-bind-try ~/.local/lib ~/.local/lib" +
		" --ro-bind-try ~/.local/bin ~/.local/bin --ro-bind-try ~/.local/share/pnpm ~/.local/share/pnpm" +
		" --ro-bind-try ~/.m2 ~/.m2 --ro-bind-try ~/.gradle ~/.gradle --ro-bind-try ~/.yarn ~/.yarn" +
		" --ro-bind-try ~/.pnpm-store ~/.pnpm-store --ro-bind-try ~/Library/pnpm ~/Library/pnpm --ro-bind-try ~/.bun ~/.bun" +
		" --ro-bind-try ~/.deno ~/.deno --ro-bind-try ~/.volta ~/.volta --ro-bind-try ~/.asdf ~/.asdf --ro-bind-try ~/.sdkman ~/.sdkman" +
		" --ro-bind-try ~/.gitconfig ~/.gitconfig --ro-bind-try ~/.config/git ~/.config/git" +
		" --ro-bind-try ~/.gitignore_global ~/.gitignore_global --ro-bind-try ~/.npm ~/.npm" +
		" --bind /tmp/jazz-sbx-1000/42-x /tmp/jazz-sbx-1000/42-x" +
		" --bind ~/jazz-eval-v2/tasks/t01 ~/jazz-eval-v2/tasks/t01" +
		" --tmpfs ~/.ssh --tmpfs ~/.config/jazz-cli --tmpfs ~/.local/share/jazz-cli" +
		" --ro-bind-try ~/.config/jazz-cli/skills ~/.config/jazz-cli/skills" +
		" --ro-bind-try ~/.local/share/jazz-cli/scripts ~/.local/share/jazz-cli/scripts" +
		" --bind-try ~/.local/share/jazz-cli/spill ~/.local/share/jazz-cli/spill" +
		" --bind-try ~/.local/share/jazz-cli/jobs ~/.local/share/jazz-cli/jobs" +
		" --chdir ~/jazz-eval-v2/tasks/t01"
	if got != want {
		t.Errorf("argv\n got: %s\nwant: %s", got, want)
	}
}

// The doctor's live check against the real backend: jazz's files unreadable,
// the control readable, and no false alarm from a hidden dir that still
// exists inside (bwrap's empty tmpfs: the old `ls dir` check reported a
// leak on Linux for exactly that).
func TestSandboxSelfTestLive(t *testing.T) {
	sb, _, _, jhome := sbLive(t, "auto", true)
	if err := os.MkdirAll(filepath.Join(jhome, "skills", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	items, err := sbLiveProbe(sb)
	if err != nil {
		t.Fatal(err)
	}
	var sawAuth, sawCanary, sawControl bool
	for _, it := range items {
		t.Logf("%v want=%v got=%v %s (%s)", it.Label, it.Want, it.Got, it.Path, it.Note)
		sawAuth = sawAuth || strings.HasSuffix(it.Path, "auth.json")
		sawCanary = sawCanary || strings.Contains(it.Path, ".sbx-canary-")
		sawControl = sawControl || it.Want
	}
	if !sawAuth || !sawCanary || !sawControl {
		t.Errorf("probe must try auth.json, the canary and the control: %+v", items)
	}
	if v := sbProbeVerdict(items, nil); v != "" {
		t.Errorf("live check: %s", v)
	}
	if ents, _ := os.ReadDir(filepath.Join(jhome, "data")); len(ents) > 0 {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".sbx-canary-") {
				t.Errorf("canary left behind: %s", e.Name())
			}
		}
	}
}

// A backend that does not isolate (a passthrough "bwrap") must be caught.
func TestSandboxSelfTestDetectsLeak(t *testing.T) {
	sb, _, _, _ := sbTestPolicy(t, "auto", sbBwrap, true)
	fake := filepath.Join(t.TempDir(), "bwrap")
	// drop every option up to "--", run the rest unsandboxed
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done; shift; exec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb.Bin = fake
	sb.Tmp = sbReal(t.TempDir())
	items, err := sbLiveProbe(sb)
	if err != nil {
		t.Fatal(err)
	}
	v := sbProbeVerdict(items, nil)
	if !strings.Contains(v, "legível dentro do sandbox") {
		t.Errorf("passthrough sandbox not flagged: %q %+v", v, items)
	}
}
