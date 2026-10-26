package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func dgIDs(fs []dgFinding) string {
	var ids []string
	for _, f := range fs {
		ids = append(ids, f.Rule)
	}
	return strings.Join(ids, ",")
}

func dgHas(fs []dgFinding, rule string) bool {
	for _, f := range fs {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func dgScanOne(t *testing.T, e *dgEngine, path, old, new string) []dgFinding {
	t.Helper()
	return e.scan(dgFile{Path: path, Old: old, New: new})
}

func TestDiffGuardBuiltinRules(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	e := dgLoadEngine("")
	if len(e.rules) < 9 {
		t.Fatalf("rules: %d", len(e.rules))
	}
	if e.packs[0].Note != "" {
		t.Fatalf("builtin pack warnings: %s", e.packs[0].Note)
	}
	for _, r := range e.rules {
		if r.MessagePT == "" || r.Why == "" || dgSev[r.Severity] == 0 {
			t.Errorf("rule %s incomplete", r.ID)
		}
	}
}

func TestDiffGuardGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"package.json", "package.json", true},
		{"package.json", "web/package.json", true},
		{"package.json", "web/package.json.bak", false},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{".github/workflows/**", ".github/workflows/a/b.yml", true},
		{".github/workflows/**", "docs/.github/workflows/ci.yml", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "a/b/c.md", true},
		{"*.md", "a/b/c.md", true},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "src/x/a.go", false},
		{"requirements*.txt", "requirements-dev.txt", true},
		{"**/.zshrc", "/Users/me/.zshrc", true},
	}
	for _, c := range cases {
		re, err := dgGlob(c.glob)
		if err != nil {
			t.Fatal(err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("glob %q on %q = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestDiffGuardRuleFixtures(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	e := dgLoadEngine("")
	key := "sk-ant-" + "TESTKEY" + strings.Repeat("0", 24)
	cases := []struct {
		name, rule, path, old, new string
		want                       bool
	}{
		// (a) hidden unicode
		{"unicode zero-width", "jz-hidden-unicode", "app.py", "x = 1\n", "x = 1\ny = 2 \u200b# note\n", true},
		{"unicode bidi", "jz-hidden-unicode", "auth.js", "", "if (a) { \u202e// }\n", true},
		{"unicode plain accents", "jz-hidden-unicode", "app.py", "", "nome = \"João e Maria, ação\"\n", false},
		{"unicode emoji zwj", "jz-hidden-unicode", "app.py", "", "e = \"👨‍👩‍👧\"\n", false},
		{"unicode already there", "jz-hidden-unicode", "app.py", "a = 1 \u200b\n", "a = 1 \u200b\nb = 2\n", false},
		// (b) dependencies
		{"dep git url", "jz-dep-url", "package.json", "{\n \"dependencies\": {\n }\n}\n", "{\n \"dependencies\": {\n  \"lib\": \"git+https://example.com/x/lib.git\"\n }\n}\n", true},
		{"dep tarball", "jz-dep-url", "package.json", "", "  \"lib\": \"https://example.com/lib-1.0.0.tgz\",\n", true},
		{"dep pip git", "jz-dep-url", "requirements.txt", "flask==3.0\n", "flask==3.0\nlib @ git+https://example.com/lib.git\n", true},
		{"dep cargo git", "jz-dep-url", "Cargo.toml", "", "lib = { git = \"https://example.com/lib\" }\n", true},
		{"dep normal npm", "jz-dep-url", "package.json", "", "  \"homepage\": \"https://example.com\",\n  \"left-pad\": \"^1.3.0\",\n", false},
		{"dep normal pip", "jz-dep-url", "requirements.txt", "", "requests==2.31.0\n", false},
		{"registry unknown", "jz-dep-registry", ".npmrc", "", "registry=https://npm.evil.example/\n", true},
		{"registry pip extra", "jz-dep-registry", "requirements.txt", "", "--extra-index-url https://pypi.evil.example/simple\n", true},
		{"registry official", "jz-dep-registry", ".npmrc", "", "registry=https://registry.npmjs.org/\n", false},
		{"typosquat npm", "jz-dep-typosquat", "package.json", "", "    \"expresss\": \"^4.18.0\",\n", true},
		{"typosquat pip", "jz-dep-typosquat", "requirements.txt", "", "reqeusts==2.31.0\n", true},
		{"typosquat popular ok", "jz-dep-typosquat", "package.json", "", "    \"express\": \"^4.18.0\",\n", false},
		{"typosquat engines ok", "jz-dep-typosquat", "package.json", "", "    \"node\": \">=18\",\n", false},
		// (c) lifecycle scripts
		{"lifecycle postinstall", "jz-lifecycle-script", "package.json", "{\n \"scripts\": {\n  \"test\": \"jest\"\n }\n}\n", "{\n \"scripts\": {\n  \"test\": \"jest\",\n  \"postinstall\": \"node setup.js\"\n }\n}\n", true},
		{"lifecycle prepare", "jz-lifecycle-script", "package.json", "", "  \"prepare\": \"husky\",\n", true},
		{"lifecycle cmdclass", "jz-lifecycle-script", "setup.py", "", "    cmdclass={'install': PostInstall},\n", true},
		{"lifecycle normal scripts", "jz-lifecycle-script", "package.json", "", "  \"build\": \"tsc\",\n  \"prepare-release\": \"node r.js\",\n", false},
		// (d) CI/CD
		{"ci github", "jz-ci-change", ".github/workflows/ci.yml", "name: ci\n", "name: ci\non: push\n", true},
		{"ci gitlab", "jz-ci-change", ".gitlab-ci.yml", "", "stages: [test]\n", true},
		{"ci jenkins", "jz-ci-change", "Jenkinsfile", "", "pipeline {}\n", true},
		{"ci azure", "jz-ci-change", "azure-pipelines.yml", "", "trigger: [main]\n", true},
		{"ci not ci", "jz-ci-change", ".github/CODEOWNERS", "", "* @me\n", false},
		{"ci untouched", "jz-ci-change", ".github/workflows/ci.yml", "a\n", "a\n", false},
		// (e) TLS off
		{"tls verify False", "jz-tls-off", "client.py", "", "r = requests.get(url, verify=False)\n", true},
		{"tls go", "jz-tls-off", "main.go", "", "tr := &tls.Config{InsecureSkipVerify: true}\n", true},
		{"tls node", "jz-tls-off", "api.js", "", "const agent = new https.Agent({ rejectUnauthorized: false })\n", true},
		{"tls node env", "jz-tls-off", "run.sh", "", "export NODE_TLS_REJECT_UNAUTHORIZED=0\n", true},
		{"tls curl -k", "jz-tls-off", "deploy.sh", "", "curl -sSLk https://example.com/x.sh -o x.sh\n", true},
		{"tls curl --insecure", "jz-tls-off", "deploy.sh", "", "curl --insecure https://example.com\n", true},
		{"tls ssh host", "jz-tls-off", "deploy.sh", "", "ssh -o StrictHostKeyChecking=no host ls\n", true},
		{"tls verify True", "jz-tls-off", "client.py", "", "r = requests.get(url, verify=True)\nverify = CA_BUNDLE\n", false},
		{"tls curl normal", "jz-tls-off", "deploy.sh", "", "curl -fsSL -H 'X-Token: a' https://example.com/x.sh\ncurl --keepalive-time 5 https://example.com\n", false},
		{"tls curl -K file", "jz-tls-off", "deploy.sh", "", "curl -K curl.cfg https://example.com\n", false},
		{"tls docs ignored", "jz-tls-off", "README.md", "", "Never use verify=False.\n", false},
		// (f) secrets
		{"secret token", "jz-secret-literal", "config.py", "", "API_KEY = \"" + key + "\"\n", true},
		{"secret aws", "jz-secret-literal", "cfg.js", "", "const k = 'AKIA" + "ABCDEFGHIJKLMNOP';\n", true},
		{"secret password", "jz-secret-literal", "db.py", "", "password = \"hunter2hunter2\"\n", true},
		{"secret env read", "jz-secret-literal", "config.py", "", "API_KEY = os.environ[\"API_KEY\"]\npassword = getpass()\n", false},
		{"secret example file", "jz-secret-literal", ".env.example", "", "API_KEY=" + key + "\n", false},
		// (g) writes outside the project
		{"outside zshrc", "jz-persist-outside", "/Users/me/.zshrc", "", "alias ll='ls -l'\n", true},
		{"outside crontab", "jz-persist-outside", "/var/spool/cron/me", "", "* * * * * /tmp/x\n", true},
		{"outside launchagent", "jz-persist-outside", "/Users/me/Library/LaunchAgents/a.plist", "", "<plist/>\n", true},
		{"outside other", "jz-persist-outside", "/Users/me/notes.txt", "", "hi\n", false},
	}
	for _, c := range cases {
		outside := strings.HasPrefix(c.path, "/")
		got := e.scan(dgFile{Path: c.path, Outside: outside, Old: c.old, New: c.new})
		if dgHas(got, c.rule) != c.want {
			t.Errorf("%s: rule %s want %v, got findings [%s]", c.name, c.rule, c.want, dgIDs(got))
		}
	}
	// scope: a .zshrc inside the project (a dotfiles repo) is not "outside"
	if got := e.scan(dgFile{Path: ".zshrc", New: "a=1\n"}); dgHas(got, "jz-persist-outside") {
		t.Errorf("project .zshrc flagged as outside")
	}
	// severities
	sev := map[string]string{"jz-secret-literal": "CRITICAL", "jz-tls-off": "HIGH", "jz-ci-change": "HIGH", "jz-lifecycle-script": "HIGH", "jz-hidden-unicode": "MEDIUM"}
	for _, r := range e.rules {
		if w, ok := sev[r.ID]; ok && r.Severity != w {
			t.Errorf("%s severity %s, want %s", r.ID, r.Severity, w)
		}
	}
}

func TestDiffGuardFindingHidesSecretAndEscapes(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	e := dgLoadEngine("")
	key := "sk-ant-" + "TESTKEY" + strings.Repeat("0", 24)
	got := e.scan(dgFile{Path: "c.py", New: "K = \"" + key + "\"\n"})
	if len(got) == 0 || strings.Contains(got[0].Text, key) || strings.Contains(strings.Join(got[0].Hunk, ""), key) {
		t.Fatalf("secret not redacted: %+v", got)
	}
	got = e.scan(dgFile{Path: "a.py", New: "x = 1 \u200b # ok \u202e\n"})
	if len(got) == 0 || strings.ContainsAny(got[0].Text, "\u200b\u202e") {
		t.Fatalf("invisible characters must be shown, not kept: %+v", got)
	}
}

func dgWritePack(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiffGuardPacksPrecedence(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	user := filepath.Join(home, "diffrules.d")
	proj := filepath.Join(root, ".jazz", "diffrules.d")
	dgWritePack(t, user, "a.json", `{"rules":[
	  {"id":"ext-todo","severity":"MEDIUM","globs":["*.py"],"regex":"FIXME-SEC","message_pt":"pendência de segurança"},
	  {"id":"jz-tls-off","severity":"MEDIUM","message_pt":"user override","regex":"verify=False"}]}`)
	// a bare array works too, and a broken rule does not take the pack down
	dgWritePack(t, user, "b.json", `[{"id":"bad","severity":"HIGH","message_pt":"x","regex":"("},{"id":"ext-two","severity":"LOW","message_pt":"dois","regex":"two"}]`)
	dgWritePack(t, proj, "p.json", `{"rules":[{"id":"jz-tls-off","severity":"CRITICAL","message_pt":"project override","regex":"verify=False"},{"id":"jz-hidden-unicode","disabled":true}]}`)

	e := dgLoadEngine(root) // the project pack is not trusted yet
	got := dgScanOne(t, e, "a.py", "", "r(verify=False) # FIXME-SEC\n")
	if !dgHas(got, "ext-todo") || got[len(got)-1].Message != "user override" && !strings.Contains(dgIDs(got), "jz-tls-off") {
		t.Fatalf("user pack not applied: %+v", got)
	}
	for _, f := range got {
		if f.Rule == "jz-tls-off" && (f.Severity != "MEDIUM" || f.Message != "user override") {
			t.Errorf("user pack must override the embedded rule: %+v", f)
		}
	}
	if dgHas(dgScanOne(t, e, "a.py", "", "x \u200b\n"), "jz-hidden-unicode") == false {
		t.Errorf("untrusted project pack must not disable a rule")
	}
	var sawIgnored, sawWarn bool
	for _, p := range e.packs {
		if p.Origin == "project" && strings.Contains(p.Note, "não confiável") {
			sawIgnored = true
		}
		if strings.HasSuffix(p.Name, "b.json") && strings.Contains(p.Note, "regex inválida") && p.Rules == 1 {
			sawWarn = true
		}
	}
	if !sawIgnored || !sawWarn {
		t.Fatalf("packs: %+v", e.packs)
	}
	// the trust gate collects the pack as kind "diffrules"
	st := pfTrustCollect(root, false)
	found := false
	for _, k := range st.kinds {
		found = found || k == dgTrustKind
	}
	if !found || !strings.Contains(st.summary, "p.json") {
		t.Fatalf("trust gate misses the pack: kinds=%v summary=%q", st.kinds, st.summary)
	}
	if err := pfTrustStore(root, st); err != nil {
		t.Fatal(err)
	}
	e = dgLoadEngine(root)
	got = dgScanOne(t, e, "a.py", "", "r(verify=False)\nx \u200b\n")
	for _, f := range got {
		if f.Rule == "jz-tls-off" && (f.Severity != "CRITICAL" || f.Message != "project override") {
			t.Errorf("project pack must beat the user pack: %+v", f)
		}
		if f.Rule == "jz-hidden-unicode" {
			t.Errorf("disabled rule still fired")
		}
	}
	// editing the pack after trust makes it untrusted again
	dgWritePack(t, proj, "p.json", `{"rules":[{"id":"jz-tls-off","severity":"LOW","message_pt":"weak","regex":"verify=False"}]}`)
	e = dgLoadEngine(root)
	for _, f := range dgScanOne(t, e, "a.py", "", "r(verify=False)\n") {
		if f.Rule == "jz-tls-off" && f.Severity == "LOW" {
			t.Errorf("an edited project pack must not load")
		}
	}
	// agent tools cannot write the pack or the allowlist
	for _, p := range []string{".jazz/diffrules.d/x.json", ".jazz/scan-allow.json"} {
		if !protectedPath(filepath.Join(root, p), root) {
			t.Errorf("%s must be a protected path", p)
		}
	}
}

func TestDiffGuardAllowlist(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	e := dgLoadEngine("")
	f := dgFile{Path: "c.py", New: "r(verify=False)\n"}
	fs := e.scan(f)
	if len(fs) != 1 {
		t.Fatal(fs)
	}
	st := newAPStoreFile(root, dgAllowFile, dgAllowKind)
	if err := st.Add("diffguard", fs[0].Hash, "jz-tls-off c.py"); err != nil {
		t.Fatal(err)
	}
	st2 := newAPStoreFile(root, dgAllowFile, dgAllowKind)
	found, allowed := e.scanAll([]dgFile{f}, st2)
	if len(found) != 0 || allowed != 1 {
		t.Fatalf("allowed line still flagged: %v %d", found, allowed)
	}
	// the same line at another position is still allowed; another line is not
	found, _ = e.scanAll([]dgFile{{Path: "c.py", New: "x=1\n\nr(verify=False)\n"}}, st2)
	if len(found) != 0 {
		t.Errorf("hash must not depend on the line number")
	}
	found, _ = e.scanAll([]dgFile{{Path: "c.py", New: "s.get(u, verify=False)\n"}}, st2)
	if len(found) != 1 {
		t.Errorf("another line needs a new answer")
	}
	// a file edited outside jazz (a repo shipping its own allowlist) is ignored
	p := filepath.Join(root, ".jazz", dgAllowFile)
	if err := os.WriteFile(p, []byte(`{"version":1,"approvals":[{"key":"`+apKey("diffguard", fs[0].Hash)+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st3 := newAPStoreFile(root, dgAllowFile, dgAllowKind)
	if st3.Has("diffguard", fs[0].Hash) {
		t.Errorf("untrusted allowlist must be ignored")
	}
	// separate from approvals.json
	if newAPStoreFile(root, "", "").Has("diffguard", fs[0].Hash) {
		t.Errorf("stores must not share entries")
	}
}

func TestDiffGuardMode(t *testing.T) {
	t.Setenv("JAZZ_DIFF_GUARD", "")
	if dgMode(&Config{}) != "on" || dgMode(&Config{DiffGuard: "off"}) != "off" || dgMode(&Config{DiffGuard: "strict"}) != "strict" {
		t.Fatal("config")
	}
	if dgMode(&Config{DiffGuard: "off", DiffGuardRun: "strict"}) != "strict" {
		t.Fatal("flag must win over config")
	}
	t.Setenv("JAZZ_DIFF_GUARD", "off")
	if dgMode(&Config{DiffGuardRun: "strict"}) != "off" {
		t.Fatal("env must win")
	}
}

// ---------- the turn-end hook ----------

func dgWriteCall(id, path, content string) fakeCall {
	return fakeCall{ID: id, Name: "write", Args: jsonArgs(map[string]any{"path": path, "content": content})}
}

const dgTLSOff = "import requests\n\ndef get(u):\n    return requests.get(u, verify=False)\n"

func dgAuditText(t *testing.T) string {
	b, _ := os.ReadFile(auPath())
	return string(b)
}

func TestDiffGuardHookApprove(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "client.py", dgTLSOff)}},
		fakeResp{Text: "Pronto, desliguei a verificação."},
	), nil)
	h.ui.answer = Allow
	stop, err := h.ag.Run(t.Context(), "conserte o erro de SSL")
	if err != nil || stop != StopDone {
		t.Fatalf("stop %s err %v", stop, err)
	}
	var rec *approvalRec
	for i := range h.ui.approvals {
		if h.ui.approvals[i].Tool == "diffguard" {
			rec = &h.ui.approvals[i]
		}
	}
	if rec == nil || !strings.Contains(rec.Detail, "jz-tls-off") || !strings.Contains(rec.Detail, "client.py:4") {
		t.Fatalf("no diff review approval: %+v", h.ui.approvals)
	}
	if _, err := os.Stat(filepath.Join(h.root, "client.py")); err != nil {
		t.Fatalf("approved change must stay: %v", err)
	}
	au := dgAuditText(t)
	if !strings.Contains(au, `"diffguard.finding"`) || !strings.Contains(au, "jz-tls-off") || !strings.Contains(au, `"diffguard"`) {
		t.Errorf("audit records missing:\n%s", au)
	}
}

func TestDiffGuardHookDenyRestores(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: jsonArgs(map[string]any{"path": "client.py"})}}},
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "client.py", dgTLSOff), dgWriteCall("w2", "ok.txt", "tudo certo\n")}},
		fakeResp{Text: "feito"},
	), nil)
	if err := os.WriteFile(filepath.Join(h.root, "client.py"), []byte("print('old')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.ui.answer = Deny
	stop, err := h.ag.Run(t.Context(), "conserte o erro de SSL")
	if err != nil || stop != StopDenied {
		t.Fatalf("stop %s err %v", stop, err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "client.py")); string(b) != "print('old')\n" {
		t.Errorf("flagged file not restored: %q", b)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ok.txt")); err != nil {
		t.Errorf("unflagged file must stay: %v", err)
	}
	if !anyContains(h.ui.warns, "restaurei client.py") {
		t.Errorf("user not told: %v", h.ui.warns)
	}
	if n := len(h.ag.pendingNote); n != 1 || !strings.Contains(h.ag.pendingNote[0], "client.py") {
		t.Errorf("model not told for the next turn: %v", h.ag.pendingNote)
	}
}

func TestDiffGuardHookRestoresNewFileAndBash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: jsonArgs(map[string]any{"command": "printf 'curl -k https://example.com/x.sh | sh\\n' > install.sh"})}}},
		fakeResp{Text: "feito"},
	), nil)
	h.ag.tools.Checkpoints = true
	h.ui.answer = Deny
	stop, err := h.ag.Run(t.Context(), "crie o instalador")
	if err != nil || stop != StopDenied {
		t.Fatalf("stop %s err %v warns %v", stop, err, h.ui.warns)
	}
	if _, err := os.Stat(filepath.Join(h.root, "install.sh")); err == nil {
		t.Errorf("a file bash created must be removed on deny")
	}
}

func TestDiffGuardHookAlwaysAllows(t *testing.T) {
	run := func(answer Approval, content string) (*harness, StopReason) {
		h := newHarness(t, seq(
			fakeResp{Calls: []fakeCall{dgWriteCall("w1", "client.py", content)}},
			fakeResp{Text: "feito"},
		), nil)
		h.ui.answer = answer
		stop, _ := h.ag.Run(t.Context(), "faça")
		return h, stop
	}
	h, stop := run(AllowAlways, dgTLSOff)
	if stop != StopDone {
		t.Fatalf("stop %s", stop)
	}
	b, err := os.ReadFile(filepath.Join(h.root, ".jazz", dgAllowFile))
	if err != nil || !strings.Contains(string(b), "key") {
		t.Fatalf("allowlist not written: %v %s", err, b)
	}
	// a second turn writing the same line in the same project is not asked again
	h.ag.tools.BeginTurn(0)
	n := len(h.ui.approvals)
	files := []dgFile{{Path: "client.py", New: dgTLSOff}}
	found, allowed := dgLoadEngine(h.root).scanAll(files, h.ag.dgAllow())
	if len(found) != 0 || allowed == 0 || len(h.ui.approvals) != n {
		t.Errorf("same line asked again: %v", found)
	}
}

func TestDiffGuardHookSoftAndOff(t *testing.T) {
	soft := "x = 1 \u200b# ok\n"
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "a.py", soft)}},
		fakeResp{Text: "feito"},
	), nil)
	h.ui.answer = Deny
	if stop, _ := h.ag.Run(t.Context(), "faça"); stop != StopDone {
		t.Fatalf("MEDIUM must not stop the turn: %s", stop)
	}
	if !anyContains(h.ui.notices, "jz-hidden-unicode") {
		t.Errorf("MEDIUM should be a notice: %v", h.ui.notices)
	}
	// strict asks
	h = newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "a.py", soft)}},
		fakeResp{Text: "feito"},
	), func(c *Config) { c.DiffGuard = "strict" })
	h.ui.answer = Deny
	if stop, _ := h.ag.Run(t.Context(), "faça"); stop != StopDenied {
		t.Fatalf("strict must ask for MEDIUM: %s", stop)
	}
	// off: nothing asked, nothing audited
	h = newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "client.py", dgTLSOff)}},
		fakeResp{Text: "feito"},
	), func(c *Config) { c.DiffGuard = "off" })
	h.ui.answer = Deny
	if stop, _ := h.ag.Run(t.Context(), "faça"); stop != StopDone {
		t.Fatalf("off: %s", stop)
	}
	for _, a := range h.ui.approvals {
		if a.Tool == "diffguard" {
			t.Errorf("off must not ask")
		}
	}
}

func TestDiffGuardHeadlessExit5(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "client.py", dgTLSOff)}},
		fakeResp{Text: "feito"},
	), nil)
	h.ag.Headless = true
	stop, err := h.ag.Run(t.Context(), "conserte o erro de SSL")
	if err != nil || stop != StopDenied || stop.ExitCode() != 5 {
		t.Fatalf("stop %s exit %d err %v", stop, stop.ExitCode(), err)
	}
	for _, a := range h.ui.approvals {
		if a.Tool == "diffguard" {
			t.Errorf("headless must not ask")
		}
	}
	b, err := os.ReadFile(filepath.Join(h.root, ".jazz", dgFindFile))
	if err != nil {
		t.Fatal(err)
	}
	var ff dgFindingsFile
	if err := json.Unmarshal(b, &ff); err != nil || len(ff.Findings) != 1 || ff.Findings[0].Rule != "jz-tls-off" || ff.Findings[0].Line != 4 || ff.Findings[0].Hash == "" {
		t.Fatalf("findings file: %v %s", err, b)
	}
	if len(ff.Restored) != 1 {
		t.Errorf("nobody approved: the file must be restored: %+v", ff)
	}
	if _, err := os.Stat(filepath.Join(h.root, "client.py")); err == nil {
		t.Errorf("unapproved risky file left in the tree")
	}
}

func TestDiffGuardCleanTurnAsksNothing(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "util.py", "def add(a, b):\n    return a + b\n")}},
		fakeResp{Text: "feito"},
	), nil)
	h.ui.answer = Deny
	if stop, _ := h.ag.Run(t.Context(), "faça"); stop != StopDone {
		t.Fatalf("stop %s", stop)
	}
	if len(h.ui.approvals) != 0 || len(h.ui.warns) != 0 {
		t.Errorf("clean diff: %v %v", h.ui.approvals, h.ui.warns)
	}
}

// ---------- semgrep ----------

const dgSemgrepFixture = `{"results":[
 {"check_id":"python.lang.security.audit.dangerous-system-call","path":"app.py","start":{"line":2},"extra":{"severity":"ERROR","message":"danger","lines":"requires login"}},
 {"check_id":"python.flask.debug-enabled","path":"app.py","start":{"line":3},"extra":{"severity":"WARNING","message":"debug on","lines":"app.run(debug=True)"}},
 {"check_id":"generic.info","path":"app.py","start":{"line":4},"extra":{"severity":"INFO","message":"note","lines":"x"}},
 {"check_id":"old.finding","path":"app.py","start":{"line":1},"extra":{"severity":"ERROR","message":"was already there","lines":"import os"}}
],"errors":[]}`

func dgFakeSemgrep(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake semgrep is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "semgrep"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDiffGuardSemgrepMapping(t *testing.T) {
	dgFakeSemgrep(t, "echo \"$@\" > \"$0.args\"\ncat <<'EOF'\n"+dgSemgrepFixture+"\nEOF\n")
	old := "import os\n"
	nw := "import os\nos.system(cmd)\napp.run(debug=True)\nx\n"
	got, err := dgRunSemgrep(context.Background(), dgSemgrepPath(), t.TempDir(), []dgFile{{Path: "app.py", Old: old, New: nw}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{2: "CRITICAL", 3: "HIGH", 4: "MEDIUM"}
	if len(got) != 3 {
		t.Fatalf("findings: %+v", got)
	}
	for _, f := range got {
		if want[f.Line] != f.Severity || !strings.HasPrefix(f.Rule, "semgrep:") || f.Hash == "" {
			t.Errorf("bad mapping: %+v", f)
		}
	}
	if got[0].Text != "os.system(cmd)" {
		t.Errorf("'requires login' must fall back to the file line: %q", got[0].Text)
	}
	args, _ := os.ReadFile(dgSemgrepPath() + ".args")
	for _, w := range []string{"p/security-audit", "p/secrets", "--json", "app.py"} {
		if !strings.Contains(string(args), w) {
			t.Errorf("semgrep args miss %s: %s", w, args)
		}
	}
}

func TestDiffGuardSemgrepHookAndOffline(t *testing.T) {
	dgFakeSemgrep(t, "cat <<'EOF'\n"+dgSemgrepFixture+"\nEOF\n")
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "app.py", "import os\nos.system(cmd)\napp.run(debug=True)\nx\n")}},
		fakeResp{Text: "feito"},
	), nil)
	h.ui.answer = Deny
	if stop, _ := h.ag.Run(t.Context(), "faça"); stop != StopDenied {
		t.Fatalf("a semgrep ERROR must gate: %s", stop)
	}
	if len(h.ui.approvals) == 0 || !strings.Contains(h.ui.approvals[0].Detail, "semgrep:") {
		t.Fatalf("approvals: %+v", h.ui.approvals)
	}
	// offline: semgrep fails to fetch the packs; one notice, the turn goes on
	dgFakeSemgrep(t, "echo 'Failed to download config p/secrets' >&2\nexit 2\n")
	h = newHarness(t, seq(
		fakeResp{Calls: []fakeCall{dgWriteCall("w1", "a.py", "x = 1\n")}},
		fakeResp{Text: "feito"},
	), nil)
	h.ui.answer = Deny
	if stop, _ := h.ag.Run(t.Context(), "faça"); stop != StopDone {
		t.Fatalf("offline semgrep must not stop the turn: %s", stop)
	}
}

// ---------- jazz scan ----------

func TestDiffGuardScanCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("PATH", os.Getenv("PATH")) // no semgrep assumed: scan prints findings of the built-in rules
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=a@b.c", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(root, "a.py"), []byte("print(1)\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "um")
	os.WriteFile(filepath.Join(root, "a.py"), []byte("print(1)\nrequests.get(u, verify=False)\n"), 0o644)
	os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755)
	os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644)
	var buf bytes.Buffer
	if code := cmdScan(root, nil, &buf); code != 5 {
		t.Fatalf("exit %d:\n%s", code, buf.String())
	}
	for _, w := range []string{"jz-tls-off", "a.py:2", "jz-ci-change", "ci.yml"} {
		if !strings.Contains(buf.String(), w) {
			t.Errorf("scan output misses %q:\n%s", w, buf.String())
		}
	}
	// --since: committed history is clean once the changes are committed
	git("add", ".")
	git("commit", "-q", "-m", "dois")
	buf.Reset()
	if code := cmdScan(root, nil, &buf); code != 0 {
		t.Fatalf("clean tree must exit 0, got %d:\n%s", code, buf.String())
	}
	buf.Reset()
	if code := cmdScan(root, []string{"--since", "HEAD~1"}, &buf); code != 5 || !strings.Contains(buf.String(), "jz-tls-off") {
		t.Fatalf("--since HEAD~1: %d\n%s", code, buf.String())
	}
	buf.Reset()
	if code := cmdScan(root, []string{"--since", "nope"}, &buf); code != 2 {
		t.Fatalf("unknown ref: %d", code)
	}
	// --rules lists packs and rules
	dgWritePack(t, filepath.Join(os.Getenv("JAZZ_HOME"), "diffrules.d"), "x.json", `{"rules":[{"id":"x-one","severity":"LOW","message_pt":"um","regex":"one"}]}`)
	buf.Reset()
	if code := cmdScan(root, []string{"--rules"}, &buf); code != 0 {
		t.Fatal(code)
	}
	for _, w := range []string{"embutido", "x.json", "x-one", "jz-secret-literal", "semgrep"} {
		if !strings.Contains(buf.String(), w) {
			t.Errorf("--rules misses %q:\n%s", w, buf.String())
		}
	}
}

// File-level approvals must bind the actual content, including removal-only edits.
func TestDiffGuardFileApprovalContent(t *testing.T) {
	e := dgLoadEngine(t.TempDir())
	find := func(f dgFile) dgFinding {
		t.Helper()
		for _, hit := range e.scan(f) {
			if hit.Rule == "jz-ci-change" {
				return hit
			}
		}
		t.Fatal("CI change went unreviewed")
		return dgFinding{}
	}
	a := find(dgFile{Path: ".github/workflows/check.yml", New: "steps:\n  - run: go test ./...\n"})
	b := find(dgFile{Path: a.File, New: "steps:\n  - run: go vet ./...\n"})
	c := find(dgFile{Path: a.File, Old: "steps:\n  - run: go test ./...\n", New: "steps:\n"})
	if a.Hash == b.Hash || a.Hash == c.Hash || b.Hash == c.Hash {
		t.Fatal("different CI changes shared approval")
	}
}

func TestDiffGuardLongLine(t *testing.T) {
	e := dgLoadEngine(t.TempDir())
	// Fixture inerte: text is scanned, never executed.
	got := e.scan(dgFile{Path: "client.py", New: strings.Repeat(" ", 5000) + "requests.get(url, verify=False)"})
	for _, f := range got {
		if f.Rule == "jz-tls-off" {
			return
		}
	}
	t.Fatal("risk after byte 4000 went unreviewed")
}

func TestDiffGuardSemgrepOptionFilename(t *testing.T) {
	dgFakeSemgrep(t, "printf '%s\\n' \"$@\" > \"$0.args\"\n echo '{\"results\":[]}'\n")
	_, err := dgRunSemgrep(t.Context(), dgSemgrepPath(), t.TempDir(), []dgFile{{Path: "--config=other", New: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(dgSemgrepPath() + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--\n--config=other\n") {
		t.Fatalf("filename can become an option: %s", args)
	}
}
