//go:build !windows

package main

// session2case_test.go: scripts/session2case.py (F0 do plano 0.14) — o
// conversor de sessão real em rascunho de caso de replay, com redação de
// segredo obrigatória.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- scripts/session2case.py ----------

// s2cRun runs the converter on a session file and returns stdout, stderr and
// the exit code.
func s2cRun(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("sem python3")
	}
	cmd := exec.Command(py, append([]string{"-I", filepath.Join("scripts", "session2case.py")}, args...)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func s2cWrite(t *testing.T, dir, id string, entries, events []map[string]any) string {
	t.Helper()
	write := func(p string, rows []map[string]any) {
		var b strings.Builder
		for _, r := range rows {
			j, _ := json.Marshal(r)
			b.Write(j)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, id+".jsonl")
	write(p, entries)
	write(filepath.Join(dir, id+obEventsSuffix), events)
	return p
}

// TestSession2Case: the converter keeps turns, model and the provider script
// of a real session, and no secret survives — every positive fixture of
// redact_test.go (mode on) plus the sudo password typed in chat. This is what
// keeps the Python patterns in step with redact.go.
func TestSession2Case(t *testing.T) {
	var secrets []string
	var texts []string
	for _, f := range rdFixtures() {
		if f.secret == "" || f.strict {
			continue
		}
		secrets = append(secrets, f.secret)
		texts = append(texts, f.text)
	}
	sudoPass := "Xq9" + rdT(70, 9, rdAlnum)
	rootPass := "s3nha-" + rdT(71, 6, rdAlnum)
	secrets = append(secrets, sudoPass, rootPass)
	blob := strings.Join(texts, "\n")
	call := map[string]any{"id": "c1", "name": "bash", "args": jsonArgs(map[string]any{"command": "cat /Users/fulano/proj/.config.yaml"})}
	entries := []map[string]any{
		{"t": "meta", "v": 2, "cwd": "/Users/fulano/proj", "model": "kilo/cohere/north-mini-code:free"},
		{"t": "msg", "msg": map[string]any{"role": "user", "content": "arruma o wifi, a senha do sudo é " + sudoPass + "\n\n<system-reminder>Lesson (hint): da última vez</system-reminder>"}},
		{"t": "msg", "msg": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{call}}},
		{"t": "msg", "msg": map[string]any{"role": "tool", "tool_call_id": "c1", "content": blob}},
		{"t": "msg", "msg": map[string]any{"role": "assistant", "content": "feito: " + blob}},
		{"t": "msg", "msg": map[string]any{"role": "user", "content": "root ana " + rootPass}},
		{"t": "msg", "msg": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c2", "name": "bash", "args": `{"command": "ls"`}}}},
	}
	events := []map[string]any{
		{"kind": "turn.start", "mode": "yolo", "model": "kilo/cohere/north-mini-code:free"},
		{"kind": "request.end", "model": "kilo/cohere/north-mini-code:free", "error": "HTTP 429", "status": 429,
			"payload": `{"error":{"code":"PROMOTION_MODEL_LIMIT_REACHED","message":"Sign up for free"}}`},
		{"kind": "request.end", "model": "kilo/cohere/north-mini-code:free", "tool_calls": 1},
		{"kind": "request.end", "model": "kilo/cohere/north-mini-code:free"},
		{"kind": "request.end", "model": "groq/openai/gpt-oss-120b", "error": "HTTP 0: Failed to parse tool call", "status": 0, "payload": ""},
		{"kind": "request.end", "model": "groq/openai/gpt-oss-120b", "tool_calls": 1},
	}
	sess := s2cWrite(t, t.TempDir(), "261111-100735-4515", entries, events)
	out, stderr, code := s2cRun(t, sess, "--name", "rascunho-teste", "--origin", "teste", "--stdout")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("segredo sobreviveu: %s…", s[:min(6, len(s))])
		}
	}
	for _, bad := range []string{"/Users/fulano", "system-reminder", "da última vez"} {
		if strings.Contains(out, bad) {
			t.Errorf("o rascunho levou %q", bad)
		}
	}
	var c rpCase
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("o rascunho não segue o esquema do replay: %v\n%s", err, out)
	}
	if len(c.Todo) == 0 || c.Name != "rascunho-teste" || c.Mode != "yolo" || c.Model != "kilo/cohere/north-mini-code:free" {
		t.Errorf("rascunho: todo=%v name=%q mode=%q model=%q", c.Todo, c.Name, c.Mode, c.Model)
	}
	if len(c.Turns) != 2 || !strings.HasPrefix(c.Turns[0], "arruma o wifi") {
		t.Errorf("turns = %q", c.Turns)
	}
	seq := c.Provider.Seq
	if len(seq) != 5 || seq[0].Status != 429 || !strings.Contains(seq[0].Body, "PROMOTION") ||
		len(seq[1].Calls) != 1 || seq[1].Calls[0].Name != "bash" || seq[3].ErrChunk == "" && seq[3].Status == 0 && seq[3].Text == "" {
		t.Errorf("provider.seq = %+v", seq)
	}
	// broken tool call JSON stays raw (a string), byte for byte
	if len(seq) == 5 && (len(seq[4].Calls) != 1 || string(seq[4].Calls[0].Args) != `"{\"command\": \"ls\""`) {
		t.Errorf("args quebrados = %+v", seq[4])
	}
}

// TestSession2CaseRefusesLeftovers: a random token no rule names makes the
// converter fail (exit 3, masked preview only) unless --mask-entropy.
func TestSession2CaseRefusesLeftovers(t *testing.T) {
	tok := rdT(80, 40, rdAlnum)
	entries := []map[string]any{
		{"t": "meta", "v": 2, "model": "fake/m1"},
		{"t": "msg", "msg": map[string]any{"role": "user", "content": "usa isso: " + tok}},
	}
	sess := s2cWrite(t, t.TempDir(), "s1", entries, nil)
	out, stderr, code := s2cRun(t, sess, "--stdout")
	if code != 3 || out != "" || strings.Contains(stderr, tok) || !strings.Contains(stderr, "RECUSADO") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	out, stderr, code = s2cRun(t, sess, "--stdout", "--mask-entropy")
	if code != 0 || strings.Contains(out, tok) {
		t.Fatalf("--mask-entropy: exit %d (%s)", code, stderr)
	}
}

// TestSession2CaseFindsByID: an id is looked up where jazz writes sessions
// (config.go dataDir: JAZZ_HOME/data/sessions/<projeto>/<id>.jsonl).
func TestSession2CaseFindsByID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	dir := filepath.Join(home, "data", "sessions", "proj-abc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s2cWrite(t, dir, "261112-101010-abcd", []map[string]any{
		{"t": "meta", "v": 2, "model": "fake/m1"},
		{"t": "msg", "msg": map[string]any{"role": "user", "content": "oi"}},
	}, nil)
	out, stderr, code := s2cRun(t, "261112-101010-abcd", "--stdout")
	if code != 0 || !strings.Contains(out, `"oi"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}
