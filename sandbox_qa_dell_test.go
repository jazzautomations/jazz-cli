package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sqPassBwrap installs a sandbox whose "bwrap" just runs the command after
// "--" (no isolation): what the scripts and the brain see of the sandbox is
// the policy (Home/HideHome/Roots), the real masking is bwrap's job.
func sqPassBwrap(t *testing.T, home, root string) *Sandbox {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh shim")
	}
	bin := filepath.Join(t.TempDir(), "bwrap")
	shim := "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &Sandbox{Mode: "auto", Backend: sbBwrap, Bin: bin, Home: home, HideHome: true, Roots: []string{root}, Tmp: t.TempDir()}
	prev := sbActive.Load()
	t.Cleanup(func() { sbActive.Store(prev) })
	sbActive.Store(sb)
	return sb
}

func TestSandboxMasksHome(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	tools := filepath.Join(home, ".local", "bin")
	sb := &Sandbox{Backend: sbBwrap, Home: home, HideHome: true, Roots: []string{proj}, CacheRO: []string{tools}}
	// fsRoot is "/" on Unix and "C:\" on Windows: the tree that contains the
	// home ("/" alone means nothing on Windows — it is not a parent of C:\…).
	fsRoot := filepath.VolumeName(home) + string(filepath.Separator)
	for p, want := range map[string]bool{
		home:                             true,
		fsRoot:                           true,
		filepath.Dir(home):               true,
		filepath.Join(home, "Downloads"): true,
		filepath.Join(home, ".config"):   true,
		proj:                             false,
		filepath.Join(proj, "src"):       false,
		tools:                            false,
		"/usr":                           false,
	} {
		if got := sb.MasksHome(p); got != want {
			t.Errorf("MasksHome(%s) = %v, want %v", p, got, want)
		}
	}
	sb.HideHome = false
	if sb.MasksHome(home) {
		t.Error("home not hidden (project = home): view is complete")
	}
	var none *Sandbox
	if none.MasksHome(home) {
		t.Error("no sandbox masks nothing")
	}
}

// (1)+(2) unit: the mark lowers confidence, flags partial and always says
// the script ran inside the sandbox.
func TestScSandboxMark(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "proj")
	_ = os.MkdirAll(root, 0o755)
	sqPassBwrap(t, home, root)
	res := func() map[string]any {
		return map[string]any{"ok": true, "confidence": 1.0, "data": map[string]any{}, "warnings": []any{"time budget reached"}}
	}
	for _, p := range []string{home, "~", "~/Downloads", "/"} {
		v := res()
		note := scSandboxMark(v, map[string]any{"path": p}, root)
		if note != scHomeMaskedNote || v["partial"] != true || v["confidence"].(float64) > scHomeMaskedConf || v["sandboxed"] != true {
			t.Errorf("path %s: note=%q v=%v", p, note, v)
		}
		w := v["warnings"].([]any)
		if len(w) != 2 || !strings.Contains(w[0].(string), "home mascarada pelo sandbox") || !strings.Contains(w[0].(string), "sandbox:false") {
			t.Errorf("path %s: warnings %v", p, w)
		}
	}
	for _, p := range []string{"", ".", "src", "/usr"} {
		v := res()
		if note := scSandboxMark(v, map[string]any{"path": p}, root); note != "" || v["partial"] != nil || v["confidence"] != 1.0 {
			t.Errorf("path %q must stay complete: %v", p, v)
		}
		if v["sandboxed"] != true || !strings.Contains(v["sandbox_note"].(string), "inside jazz's OS sandbox") {
			t.Errorf("path %q: no inside-the-sandbox mark: %v", p, v)
		}
	}
	sbActive.Store(nil)
	v := res()
	if scSandboxMark(v, map[string]any{"path": home}, root) != "" || v["sandboxed"] != nil {
		t.Errorf("no sandbox: no marks, got %v", v)
	}
}

// (1)+(2) end to end: the script tool and fact disk_usage of the hidden home
// come back partial, low confidence, and say they ran inside the sandbox.
func TestDiskUsageMaskedHomeIsPartial(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	if h.ag.tools.scRegistry().python == "" {
		t.Skip("no python3")
	}
	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "big.bin"), make([]byte, 64<<10), 0o644)
	sqPassBwrap(t, home, h.root)

	b, _ := json.Marshal(map[string]any{"name": "disk_usage", "path": home, "top": float64(3)})
	out := h.ag.tools.script(context.Background(), map[string]any{"name": "disk_usage", "path": home, "top": float64(3)}, string(b))
	var v map[string]any
	if err := json.Unmarshal([]byte(out.Text), &v); err != nil {
		t.Fatalf("script: %v\n%s", err, out.Text)
	}
	if v["partial"] != true || v["confidence"].(float64) > scHomeMaskedConf || v["sandboxed"] != true ||
		!strings.Contains(out.Text, "home mascarada pelo sandbox") || !strings.Contains(out.Display.Summary, "home mascarada") {
		t.Fatalf("script disk_usage of the masked home not partial: %s / %s", out.Text, out.Display.Summary)
	}
	// the project itself is visible: complete, but still marked as sandboxed
	b, _ = json.Marshal(map[string]any{"name": "disk_usage"})
	out = h.ag.tools.script(context.Background(), map[string]any{"name": "disk_usage"}, string(b))
	if strings.Contains(out.Text, "home mascarada") || !strings.Contains(out.Text, `"sandboxed":true`) {
		t.Fatalf("project scan: %s", out.Text)
	}

	r, fo := factCall(t, h, "disk_usage", map[string]any{"path": home})
	if !r.Partial || !strings.Contains(r.Warning, "home mascarada pelo sandbox") || !strings.Contains(r.Sandbox, "inside jazz's OS sandbox") ||
		!strings.Contains(fo.Text, "inside the sandbox") || !strings.Contains(fo.Text, "conf 0.20") {
		t.Fatalf("fact disk_usage of the masked home: %+v\n%s", r, fo.Text)
	}
}

// (3) bash sandbox:false never reads secrets: not in yolo, not with
// --allow-unsandboxed, not with a yes — and the refusal says no flag helps.
func TestUnsandboxedSecretReadRefused(t *testing.T) {
	usFakeSandbox(t)
	defer func(v bool) { usAllow = v }(usAllow)
	cmds := []string{
		"cat ~/.ssh/id_ed25519; touch escaped.txt",
		"cat ~/.aws/credentials; touch escaped.txt",
		"cat ~/.config/jazz-cli/auth.json; touch escaped.txt",
		"security find-generic-password -s github -w; touch escaped.txt",
	}
	for _, allow := range []bool{false, true} {
		for _, mode := range []string{"yolo", "auto", "ask"} {
			for _, c := range cmds {
				usAllow = allow
				args, _ := json.Marshal(map[string]any{"command": c, "sandbox": false})
				h := newHarness(t, seq(fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: string(args)}}}, fakeResp{Text: "não li"}),
					func(cfg *Config) { cfg.Mode = mode })
				h.ui.answer = Allow
				if err := h.run(t, "mostra a chave"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(h.root, "escaped.txt")); err == nil {
					t.Fatalf("allow=%v mode=%s: %q ran outside the sandbox", allow, mode, c)
				}
				if len(h.ui.approvals) != 0 {
					t.Errorf("allow=%v mode=%s %q: a secret read outside the sandbox must not be a question: %+v", allow, mode, c, h.ui.approvals)
				}
				txt := ""
				for _, m := range h.ag.Msgs {
					if m.Role == "tool" {
						txt += m.Content
					}
				}
				if !strings.Contains(txt, "Blocked") && !strings.Contains(txt, "BLOCKED") {
					t.Errorf("allow=%v mode=%s %q: not blocked: %s", allow, mode, c, txt)
				}
				if !strings.Contains(strings.ToLower(txt), "no flag") {
					t.Errorf("allow=%v mode=%s %q: refusal must say no flag unlocks it: %s", allow, mode, c, txt)
				}
			}
		}
	}
}

// (3) the toolbox refuses on its own too (defense in depth behind permit).
func TestUnsandboxedSecretReadToolboxRefuses(t *testing.T) {
	usFakeSandbox(t)
	h := newHarness(t, nil, nil)
	m := map[string]any{"command": "cat ~/.ssh/id_rsa; touch escaped.txt", "sandbox": false}
	out := h.ag.tools.bash(context.Background(), m, jsonArgs(m))
	if !out.IsErr || !strings.Contains(out.Text, "No flag") || !strings.Contains(out.Display.Summary, "nenhuma flag libera") {
		t.Fatalf("toolbox: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(h.root, "escaped.txt")); err == nil {
		t.Fatal("ran outside the sandbox")
	}
	// a harmless command still leaves the sandbox (the escape itself works)
	if usSecretRead("npx remotion render") != "" || usSecretRead("crontab -l") != "" || usSecretRead("du -sh ~") != "" {
		t.Error("benign unsandboxed commands must not be flagged as secret reads")
	}
}
