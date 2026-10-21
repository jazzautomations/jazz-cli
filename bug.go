package main

// bug.go: `jazz bug` writes a tar.gz with what is needed to debug a problem
// (G6): doctor output, redacted config files, the newest debug log and crash
// file, and (only after asking) the newest session of this project. Owner: platform.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// pfRedactJSONValues masks values of header/env maps and anything secret-shaped.
var pfSecretKeyRe = regexp.MustCompile(`(?i)(key|token|secret|password|authorization|cookie)`)

func pfRedactConfigJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return []byte(redactSecrets(string(b)))
	}
	var walk func(x any, parentKey string) any
	walk = func(x any, parentKey string) any {
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				if s, ok := val.(string); ok && (pfSecretKeyRe.MatchString(k) && !strings.HasSuffix(strings.ToLower(k), "_env") || parentKey == "headers" || parentKey == "env") && s != "" {
					t[k] = "[REDACTED]"
					continue
				}
				t[k] = walk(val, strings.ToLower(k))
			}
			return t
		case []any:
			for i := range t {
				t[i] = walk(t[i], parentKey)
			}
			return t
		case string:
			return redactSecrets(t)
		}
		return x
	}
	out, _ := json.MarshalIndent(walk(v, ""), "", "  ")
	return out
}

func pfNewestFile(dir, prefix, suffix string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1])
}

func cmdBug(cfg *Config, root string) {
	out := fmt.Sprintf("jazz-bug-%s.tar.gz", time.Now().Format("20060102-150405"))
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		fatal(err.Error())
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()})
		_, _ = tw.Write(data)
	}
	var files []string
	if exe, err := os.Executable(); err == nil {
		cmd := exec.Command(exe, "doctor")
		cmd.Dir = root
		cmd.Env = append(scrubEnv(os.Environ()), "NO_COLOR=1", "TERM=dumb", "JAZZ_NO_UPDATE_CHECK=1")
		b, _ := cmd.CombinedOutput()
		add("doctor.txt", []byte(redactSecrets(stripANSI(string(b)))))
		files = append(files, "doctor.txt")
	}
	for _, p := range []struct{ src, name string }{
		{filepath.Join(configDir(), "config.json"), "config.json"},
		{filepath.Join(configDir(), "mcp.json"), "mcp.json"},
		{filepath.Join(root, ".mcp.json"), "project-mcp.json"},
		{filepath.Join(root, ".jazz", "settings.json"), "project-settings.json"},
	} {
		if b, err := os.ReadFile(p.src); err == nil {
			add(p.name, pfRedactConfigJSON(b))
			files = append(files, p.name)
		}
	}
	if p := pfLatestLog(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			if len(b) > 8<<20 {
				b = b[len(b)-8<<20:]
			}
			add("debug.log", []byte(redactSecrets(string(b))))
			files = append(files, "debug.log")
		}
	}
	if p := pfNewestFile(dataDir(), "crash-", ".txt"); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			add(filepath.Base(p), []byte(redactSecrets(string(b))))
			files = append(files, filepath.Base(p))
		}
	}
	if list := ListSessions(root); len(list) > 0 && isTTY(os.Stdin) {
		fmt.Printf("incluir a última sessão desta pasta (%s, %q)? Ela tem a conversa inteira, com segredos mascarados. [s/N] ", list[0].ID, oneLine(list[0].Title, 50))
		if a := strings.ToLower(strings.TrimSpace(pfReadLine(os.Stdin))); a == "s" || a == "sim" || a == "y" {
			if b, err := os.ReadFile(list[0].Path); err == nil {
				add("session.jsonl", []byte(redactSecrets(string(b))))
				files = append(files, "session.jsonl")
			}
		}
	}
	if err := tw.Close(); err != nil {
		fatal(err.Error())
	}
	if err := gz.Close(); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("✓ %s (%s)\n  confira o conteúdo antes de enviar: tar tzf %s\n", out, strings.Join(files, ", "), out)
}
