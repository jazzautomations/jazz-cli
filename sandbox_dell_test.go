package main

import (
	"os"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "windows" {
		t.Skip("sandbox-exec/bwrap policy: Unix-only (needs /tmp, /usr/bin)")
	}
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

// sbBwrapWritable models whether a sandboxed process (no capabilities left)
// can create a file at p: the topmost mount covering p decides; --bind and
// --tmpfs are writable, --ro-bind is not, and --remount-ro DEST makes the
// mount then on top at DEST read-only (not recursive: binds nested under DEST
// keep their flags).
func sbBwrapWritable(args []string, p string) bool {
	type mnt struct {
		dest string
		rw   bool
	}
	var ms []mnt
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--bind", "--bind-try":
			ms = append(ms, mnt{args[i+2], args[i+1] != "/dev/null"})
			i += 2
		case "--ro-bind", "--ro-bind-try":
			ms = append(ms, mnt{args[i+2], false})
			i += 2
		case "--tmpfs":
			ms = append(ms, mnt{args[i+1], true})
			i++
		case "--dev", "--proc":
			ms = append(ms, mnt{args[i+1], false})
			i++
		case "--remount-ro":
			for k := len(ms) - 1; k >= 0; k-- {
				if ms[k].dest == args[i+1] {
					ms[k].rw = false
					break
				}
			}
			i++
		case "--chdir":
			i++
		}
	}
	for k := len(ms) - 1; k >= 0; k-- {
		m := ms[k]
		if p == m.dest || m.dest == "/" || strings.HasPrefix(p, m.dest+"/") {
			return m.rw
		}
	}
	return false
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
			// writes: only the project, the private temp dir and the dirs
			// given back; HOME, /tmp and the hidden dirs are read-only (they
			// used to be writable tmpfs: the write "worked" and vanished)
			writable := []string{c.proj + "/main.go", sb.Tmp + "/f", j(".local/share/jazz-cli/spill/out.txt"), j(".local/share/jazz-cli/jobs/1.log")}
			for _, p := range writable {
				if !sbBwrapWritable(a, p) {
					t.Errorf("%s must be writable inside the sandbox", p)
				}
			}
			readonly := []string{"/tmp/planted", "/tmp/" + filepath.Base(filepath.Dir(sb.Tmp)) + "/x", "/usr/bin/planted", j(".config/jazz-cli/planted"),
				j(".config/jazz-cli/skills/x/SKILL.md"), j(".local/share/jazz-cli/planted"), j(".ssh/authorized_keys")}
			if c.proj != home {
				readonly = append(readonly, j("planted"), j(".bashrc"), j(".local/bin/jazz"))
			}
			for _, p := range readonly {
				if sbBwrapWritable(a, p) {
					t.Errorf("%s must NOT be writable inside the sandbox", p)
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
	// O binário de teste cruzado no Dell não termina em ".test"; sem o stub
	// ele entraria no argv como --ro-bind-try de si mesmo.
	oldExe := sbSelfExe
	sbSelfExe = func() string { return "" }
	defer func() { sbSelfExe = oldExe }()
	home := sbDellHome(t)
	proj := filepath.Join(home, "jazz-eval-v2/tasks/t01")
	sb := sbNew("strict", sbBwrap, "/usr/bin/bwrap", false, []string{proj})
	sb.Tmp = "/tmp/jazz-sbx-1000/42-x"
	a := sbBwrapArgs(sb, proj)
	got := strings.ReplaceAll(strings.Join(a, " "), home, "~")
	if _, err := os.Stat("/run/user/" + itoa(os.Getuid())); err == nil {
		got = strings.Replace(got, " --tmpfs /run/user/"+itoa(os.Getuid()), "", 1)
		got = strings.Replace(got, " --remount-ro /run/user/"+itoa(os.Getuid()), "", 1)
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
		" --remount-ro /tmp --remount-ro ~ --remount-ro ~/.ssh --remount-ro ~/.config/jazz-cli --remount-ro ~/.local/share/jazz-cli" +
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
	var sawAuth, sawCanary, sawControl, sawWriteOK, sawWriteDenied bool
	for _, it := range items {
		sawWriteOK = sawWriteOK || it.Write && it.Want
		sawWriteDenied = sawWriteDenied || it.Write && !it.Want
		t.Logf("%v want=%v got=%v %s (%s)", it.Label, it.Want, it.Got, it.Path, it.Note)
		sawAuth = sawAuth || strings.HasSuffix(it.Path, "auth.json")
		sawCanary = sawCanary || strings.Contains(it.Path, ".sbx-canary-")
		sawControl = sawControl || it.Want
	}
	if !sawWriteOK || !sawWriteDenied {
		t.Errorf("probe must try a write in the project and one outside it: %+v", items)
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
	for _, it := range items {
		if it.Write && !it.Want {
			if !it.Got {
				t.Errorf("passthrough: write outside the project not flagged: %+v", it)
			}
			if _, err := os.Lstat(it.Path); err == nil {
				t.Errorf("probe file left on the host: %s", it.Path)
			}
		}
	}
	if v := sbProbeVerdict(sbOnlyWrites(items), nil); !strings.Contains(v, "escrita fora do projeto") {
		t.Errorf("write leak verdict: %q", v)
	}
}

// A tmpfs covered by a later mount on it or on a parent is not remounted
// (bwrap would remount the mount on top): XDG_RUNTIME_DIR under the hidden
// HOME, a project that is /tmp itself. Synthetic paths (no host lookups).
func TestSandboxBwrapRemountSkipsCovered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bwrap argv: Unix paths")
	}
	home := "/nonexistent-jzsbx/home/u"
	t.Setenv("XDG_RUNTIME_DIR", home+"/.run")
	sb := &Sandbox{Backend: sbBwrap, Net: true, Home: home, HideHome: true, Roots: []string{"/tmp"}, Tmp: "/tmp/jazz-sbx-1000/42-x"}
	args := sbBwrapArgs(sb, "/tmp")
	a := " " + strings.Join(args, " ") + " "
	if strings.Contains(a, " --remount-ro "+home+"/.run ") || strings.Contains(a, " --remount-ro /tmp ") {
		t.Errorf("covered tmpfs remounted: %s", a)
	}
	if !strings.Contains(a, " --remount-ro "+home+" ") {
		t.Errorf("hidden HOME not remounted read-only: %s", a)
	}
	if !sbBwrapWritable(args, "/tmp/x") || sbBwrapWritable(args, home+"/x") || sbBwrapWritable(args, home+"/.run/x") {
		t.Errorf("writability: project /tmp rw, HOME and its runtime dir ro: %s", a)
	}
}

// A cache that does not exist yet on the host goes to the private temp dir
// (the hidden HOME is read-only now; --bind-try skipped the missing source).
func TestSandboxBwrapMissingCachesGoToTmp(t *testing.T) {
	home := sbDellHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".cache", "pip"), 0o700); err != nil {
		t.Fatal(err)
	}
	sb := sbNew("auto", sbBwrap, "/usr/bin/bwrap", true, []string{filepath.Join(home, "jazz-eval-v2")})
	sb.Tmp = "/tmp/x"
	env := map[string]string{}
	got := strings.Join(sb.bwrapCacheEnv(func(k string) string { return env[k] }), " ")
	for _, want := range []string{"GOCACHE=/tmp/x/go-build", "GOMODCACHE=/tmp/x/gomod", "npm_config_cache=/tmp/x/npm", "YARN_CACHE_FOLDER=/tmp/x/yarn"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %q", want, got)
		}
	}
	if strings.Contains(got, "PIP_CACHE_DIR") {
		t.Errorf("existing pip cache must stay the host one: %q", got)
	}
	env["GOPATH"], env["XDG_CACHE_HOME"] = "/opt/go", "/var/cache/u"
	got = strings.Join(sb.bwrapCacheEnv(func(k string) string { return env[k] }), " ")
	if strings.Contains(got, "GOMODCACHE") || strings.Contains(got, "GOCACHE") || strings.Contains(got, "YARN") {
		t.Errorf("caches moved by env must be left alone: %q", got)
	}
	sb.Backend = sbSeatbelt
	if sb.bwrapCacheEnv(func(string) string { return "" }) != nil {
		t.Error("seatbelt allows writing missing caches: no redirect")
	}
}

func sbOnlyWrites(items []sbProbeItem) (out []sbProbeItem) {
	for _, it := range items {
		if it.Write {
			out = append(out, it)
		}
	}
	return
}

// A command whose cwd is under /tmp but outside the project (the offline
// eval fixtures) is bound on top of the /tmp tmpfs, and /tmp stays read-only.
func TestSandboxBwrapBindsCwdUnderTmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only")
	}
	sb, _, _, _ := sbTestPolicy(t, "auto", sbBwrap, false)
	sb.Tmp = sbReal(t.TempDir())
	fix := filepath.Join(sbDellHome(t), "..", "..", "x", "fixture")
	if err := os.MkdirAll(fix, 0o755); err != nil {
		t.Fatal(err)
	}
	fix = sbReal(fix)
	// make it look like it is under /tmp: the temp dir of sbDellHome is /tmp/jzsbx-dell-*
	if !strings.HasPrefix(fix, "/tmp/") {
		t.Skip("temp dir is not under /tmp")
	}
	a := strings.Join(sbBwrapArgs(sb, fix), " ")
	tm, bind := strings.Index(a, "--tmpfs /tmp "), strings.Index(a, "--bind "+fix+" "+fix)
	if tm < 0 || bind < tm {
		t.Fatalf("cwd must be bound after the /tmp tmpfs: %s", a)
	}
	if !strings.Contains(a, "--remount-ro /tmp") {
		t.Errorf("/tmp must stay remounted read-only: %s", a)
	}
	if b := strings.Join(sbBwrapArgs(sb, "/tmp"), " "); strings.Contains(b, "--bind /tmp /tmp") {
		t.Error("/tmp itself must never be bound")
	}
	if b := strings.Join(sbBwrapArgs(sb, "/usr/lib"), " "); strings.Contains(b, "--bind /usr/lib") {
		t.Error("a visible cwd needs no bind")
	}
}

// The jazz binary is given back read-only wherever it lives.
func TestSandboxBwrapBindsSelfExe(t *testing.T) {
	sb, _, _, _ := sbTestPolicy(t, "auto", sbBwrap, false)
	sb.Tmp = sbReal(t.TempDir())
	old := sbSelfExe
	sbSelfExe = func() string { return "/home/u/jazz-09" }
	defer func() { sbSelfExe = old }()
	if a := strings.Join(sbBwrapArgs(sb, ""), " "); !strings.Contains(a, "--ro-bind-try /home/u/jazz-09 /home/u/jazz-09") {
		t.Fatalf("exe not bound: %s", a)
	}
}
