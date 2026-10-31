package main

// evaloffline.go: `jazz eval offline` (docs/design/offline-brain.md §9). The
// suite in evals/offline/tasks.json runs with the model disabled: each task
// builds a small fixture project in a temp dir and goes through the offline
// brain (brain.go) exactly as `jazz -p --offline` would, with scripted
// answers for the questions; the "guard" tasks check the minimal
// intervention rule (caps.go) on a proposed command. Results go to
// results/offline-<time>.json: verified success, false success (must be 0),
// share answered without a model, questions asked vs needed, commands per
// task, latency p50/p95 and peak RSS.
//
// The environment is the real one (envprobe) plus three synthetic names so
// the imprecise-reference tasks are the same on every machine: the SSH host
// labbox and the toolchain tailscale (added only when absent).

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed evals/offline/tasks.json
var eoTasksJSON []byte

type eoExpect struct {
	Status      string   `json:"status,omitempty"`
	Intent      string   `json:"intent,omitempty"`
	Category    string   `json:"category,omitempty"`
	Contains    []string `json:"contains,omitempty"`
	ContainsAny []string `json:"contains_any,omitempty"` // at least one (answers that legitimately differ per machine)
	NotContains []string `json:"not_contains,omitempty"`
	Questions   *int     `json:"questions,omitempty"`
	Handled     *bool    `json:"handled,omitempty"`
	Blocked     *bool    `json:"blocked,omitempty"`
	MaxMS       float64  `json:"max_ms,omitempty"`   // latency budget inside jazz (no process start)
	Commands    *int     `json:"commands,omitempty"` // exact number of commands run
}

type eoTask struct {
	ID       string    `json:"id"`
	Cat      string    `json:"cat"`
	Doc      string    `json:"doc,omitempty"`
	Fixture  string    `json:"fixture,omitempty"`
	Prompt   string    `json:"prompt,omitempty"`
	Needs    string    `json:"needs,omitempty"`
	Answers  []int     `json:"answers,omitempty"` // 1-based menu answers, in order
	Texts    []string  `json:"texts,omitempty"`   // free answers
	GuardCmd string    `json:"guard_cmd,omitempty"`
	Goal     string    `json:"goal,omitempty"`
	Expect   eoExpect  `json:"expect"`
	Missing  *eoExpect `json:"expect_missing,omitempty"`
}

type eoGot struct {
	Handled   bool    `json:"handled"`
	Intent    string  `json:"intent,omitempty"`
	Conf      float64 `json:"confidence,omitempty"`
	Status    string  `json:"status,omitempty"`
	Method    string  `json:"method,omitempty"`
	Category  string  `json:"category,omitempty"`
	Questions int     `json:"questions"`
	Commands  int     `json:"commands"`
	MS        float64 `json:"ms"`
	Blocked   *bool   `json:"blocked,omitempty"`
	Why       string  `json:"why,omitempty"`
	Text      string  `json:"text,omitempty"`
}

type eoRow struct {
	ID           string   `json:"id"`
	Cat          string   `json:"cat"`
	Prompt       string   `json:"prompt"`
	Expect       eoExpect `json:"expect"`
	Got          eoGot    `json:"got"`
	Success      bool     `json:"success"`
	FalseSuccess bool     `json:"false_success"`
	Fail         []string `json:"fail,omitempty"`
	QNeeded      int      `json:"questions_needed"`
}

type eoSummary struct {
	N               int            `json:"n"`
	Success         int            `json:"success"`
	FalseSuccess    int            `json:"false_success"`
	NoLLM           int            `json:"answered_without_llm"`
	NoLLMRate       float64        `json:"no_llm_rate"`
	QuestionsAsked  int            `json:"questions_asked"`
	QuestionsNeeded int            `json:"questions_needed"`
	CmdsPerTask     float64        `json:"commands_per_task"`
	P50MS           float64        `json:"latency_p50_ms"`
	P95MS           float64        `json:"latency_p95_ms"`
	PeakRSSKB       int64          `json:"peak_rss_kb"`
	ByCat           map[string]int `json:"success_by_category"`
	CatN            map[string]int `json:"tasks_by_category"`
}

type eoReport struct {
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	Host    string    `json:"host"`
	Model   string    `json:"model"`
	Tasks   []eoRow   `json:"tasks"`
	Summary eoSummary `json:"summary"`
}

// cmdEval implements `jazz eval offline [--out DIR] [--only id,id] [-v]`.
func cmdEval(root string, args []string, w io.Writer) int {
	if len(args) == 0 || args[0] != "offline" {
		fmt.Fprintln(os.Stderr, "uso: jazz eval offline [--out DIR] [--only id,id] [-v]")
		return 2
	}
	out, only, verbose := filepath.Join(root, "results"), map[string]bool{}, false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--out", "-out":
			if i+1 < len(args) {
				out = args[i+1]
				i++
			}
		case "--only", "-only":
			if i+1 < len(args) {
				for _, x := range strings.Split(args[i+1], ",") {
					only[strings.TrimSpace(x)] = true
				}
				i++
			}
		case "-v", "--verbose":
			verbose = true
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz eval offline [--out DIR] [--only id,id] [-v]")
			return 2
		}
	}
	rep, err := eoRun(only, verbose, w)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	p := filepath.Join(out, "offline-"+rep.Started.Format("20060102-150405")+".json")
	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	fmt.Fprintln(w, "resultado:", p)
	if rep.Summary.FalseSuccess > 0 {
		return 1
	}
	return 0
}

// eoRun runs the suite (only = subset by id).
func eoRun(only map[string]bool, verbose bool, w io.Writer) (*eoReport, error) {
	var suite struct {
		Tasks []eoTask `json:"tasks"`
	}
	if err := json.Unmarshal(eoTasksJSON, &suite); err != nil {
		return nil, fmt.Errorf("evals/offline/tasks.json: %w", err)
	}
	rep := &eoReport{Version: version, Started: time.Now(), Host: runtime.GOOS + "/" + runtime.GOARCH + fmt.Sprintf(" %d cpu", runtime.NumCPU()), Model: "none"}
	base, err := eoFixtureBase(sbCurrent())
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(base)
	prof := eoProfile()
	prev := epInstall(prof)
	defer epInstall(prev)
	var lat []float64
	for _, tk := range suite.Tasks {
		if len(only) > 0 && !only[tk.ID] {
			continue
		}
		row := eoOne(tk, base, prof)
		rep.Tasks = append(rep.Tasks, row)
		lat = append(lat, row.Got.MS)
		mark := "ok "
		if !row.Success {
			mark = "ERR"
		}
		if row.FalseSuccess {
			mark = "FALSO"
		}
		fmt.Fprintf(w, "%-5s %-24s %-18s %-12s %6.0f ms  %s\n", mark, tk.ID, tk.Cat, orDash(row.Got.Status), row.Got.MS, strings.Join(row.Fail, "; "))
		if verbose && row.Got.Text != "" {
			for _, l := range strings.Split(row.Got.Text, "\n") {
				fmt.Fprintln(w, "      | "+l)
			}
		}
	}
	s := eoSummary{ByCat: map[string]int{}, CatN: map[string]int{}}
	cmds := 0
	for _, r := range rep.Tasks {
		s.N++
		s.CatN[r.Cat]++
		if r.Success {
			s.Success++
			s.ByCat[r.Cat]++
		}
		if r.FalseSuccess {
			s.FalseSuccess++
		}
		if r.Got.Handled || r.Got.Blocked != nil {
			s.NoLLM++
		}
		s.QuestionsAsked += r.Got.Questions
		s.QuestionsNeeded += r.QNeeded
		cmds += r.Got.Commands
	}
	if s.N > 0 {
		s.NoLLMRate = float64(s.NoLLM) / float64(s.N)
		s.CmdsPerTask = float64(cmds) / float64(s.N)
	}
	sort.Float64s(lat)
	s.P50MS, s.P95MS = eoPct(lat, 0.5), eoPct(lat, 0.95)
	s.PeakRSSKB = eoPeakRSSKB()
	rep.Summary = s
	fmt.Fprintf(w, "\n%d/%d com sucesso verificado · falso sucesso %d · sem LLM %d/%d · perguntas %d (necessárias %d) · %.2f comandos/tarefa · p50 %.0f ms p95 %.0f ms · RSS pico %d KB\n",
		s.Success, s.N, s.FalseSuccess, s.NoLLM, s.N, s.QuestionsAsked, s.QuestionsNeeded, s.CmdsPerTask, s.P50MS, s.P95MS, s.PeakRSSKB)
	return rep, nil
}

func eoPct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q*float64(len(sorted)-1) + 0.5)
	return sorted[i]
}

// eoProfile is the real profile plus the synthetic names the suite uses.
func eoProfile() *epProfile {
	real := *epGet(false)
	p := real
	p.SSHHosts = append([]epHost{{Name: "labbox", HostName: "198.51.100.7", User: "eval"}}, real.SSHHosts...)
	has := false
	for _, t := range p.Toolchains {
		has = has || t.Name == "tailscale"
	}
	if !has {
		p.Toolchains = append(append([]epTool{}, real.Toolchains...), epTool{Name: "tailscale", Path: "/usr/bin/tailscale", Version: "1.80.2"})
		bins := append(append([]string{}, real.PathBins...), "tailscale")
		sort.Strings(bins)
		p.PathBins = bins
	}
	return &p
}

// eoOne runs one task.
func eoOne(tk eoTask, base string, prof *epProfile) eoRow {
	row := eoRow{ID: tk.ID, Cat: tk.Cat, Prompt: tk.Prompt}
	exp := tk.Expect
	if tk.Needs != "" && !epHasBin(prof, tk.Needs) && tk.Missing != nil {
		exp = *tk.Missing
	}
	row.Expect = exp
	if exp.Questions != nil {
		row.QNeeded = *exp.Questions
	}
	t0 := time.Now()
	if tk.GuardCmd != "" {
		row.Prompt = tk.GuardCmd
		why := cbMinimalIntervention("bash", nil, tk.GuardCmd, tk.Goal)
		blocked := why != ""
		row.Got = eoGot{Handled: true, Blocked: &blocked, Why: why, MS: float64(time.Since(t0).Microseconds()) / 1000}
		if exp.Blocked != nil && *exp.Blocked != blocked {
			row.Fail = append(row.Fail, fmt.Sprintf("bloqueado=%v, esperado %v", blocked, *exp.Blocked))
		}
		row.Success = len(row.Fail) == 0
		row.FalseSuccess = exp.Blocked != nil && *exp.Blocked && !blocked // a dangerous command let through
		return row
	}
	root, err := eoFixture(base, tk.ID, tk.Fixture)
	if err != nil {
		row.Fail = append(row.Fail, "fixture: "+err.Error())
		return row
	}
	tb := NewToolbox(root)
	defer tb.Shell.Close()
	det := fpDetect(root)
	testCmd := det.Test
	answers := append([]int(nil), tk.Answers...)
	texts := append([]string(nil), tk.Texts...)
	env := &brEnv{Root: root, Shell: tb.Shell, Profile: prof, Scripts: tb.scRegistry(),
		BeginTurn: func() { tb.BeginTurn(0) }, AfterWrite: tb.cpAfterBash, Undo: tb.Undo,
		// what auto mode would run without asking: read-only commands and the
		// project's test command, never a critical one
		Permit: func(ctx context.Context, cmd string) (bool, string) {
			if why := cbMinimalIntervention("bash", nil, cmd, tk.Prompt); why != "" {
				return false, why
			}
			// what runs in a trusted session without asking: read-only
			// commands, the registry's exact allowlist (what read_cmd itself
			// gates on), and the commands the project detection declares —
			// the user approves those interactively
			if IsReadOnly(cmd) || cbReadCmd(cmd) || testCmd != "" && strings.HasPrefix(cmd, eoFirstWords(testCmd, 2)) ||
				cmd != "" && (cmd == det.Build || cmd == det.Lint || cmd == det.Fmt) ||
				// file_op synth commands: the cap already validated the path
				// inside Root — this simulates the user approving the write
				strings.HasPrefix(cmd, "write ") || strings.HasPrefix(cmd, "mv ") ||
				strings.HasPrefix(cmd, "mkdir ") || strings.HasPrefix(cmd, "rm ") {
				return true, ""
			}
			return false, "eval: só leitura, allowlist do registry e comandos do projeto"
		},
		Ask: func(q string, opts []string) int {
			if len(answers) == 0 {
				return -1
			}
			a := answers[0] - 1
			answers = answers[1:]
			return a
		},
		AskText: func(q string) (string, bool) {
			if len(texts) == 0 {
				return "", false
			}
			a := texts[0]
			texts = texts[1:]
			return a, true
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r := brRun(ctx, env, tk.Prompt)
	row.Got = eoGot{Handled: r.Handled, Intent: r.Intent.Name, Conf: r.Intent.Confidence, Status: string(r.Status), Method: r.Method, Category: string(r.Category),
		Questions: r.Questions, Commands: r.Commands, MS: float64(time.Since(t0).Microseconds()) / 1000, Text: oneLineKeep(r.Text, 4000)}
	fail := func(f string, a ...any) { row.Fail = append(row.Fail, fmt.Sprintf(f, a...)) }
	if exp.Handled != nil && *exp.Handled != r.Handled {
		fail("handled=%v, esperado %v (%s %.2f)", r.Handled, *exp.Handled, r.Intent.Name, r.Intent.Confidence)
	}
	if exp.Status != "" && string(r.Status) != exp.Status {
		fail("status %s, esperado %s", orDash(string(r.Status)), exp.Status)
	}
	if exp.Intent != "" && r.Intent.Name != exp.Intent {
		fail("intenção %s, esperada %s", r.Intent.Name, exp.Intent)
	}
	if exp.Category != "" && string(r.Category) != exp.Category {
		fail("categoria %s, esperada %s", orDash(string(r.Category)), exp.Category)
	}
	for _, c := range exp.Contains {
		if !strings.Contains(r.Text, c) {
			fail("falta %q", c)
		}
	}
	for _, c := range exp.NotContains {
		if strings.Contains(r.Text, c) {
			fail("não devia ter %q", c)
		}
	}
	if len(exp.ContainsAny) > 0 {
		hit := false
		for _, c := range exp.ContainsAny {
			hit = hit || strings.Contains(r.Text, c)
		}
		if !hit {
			fail("falta um de %q", exp.ContainsAny)
		}
	}
	if exp.Questions != nil && r.Questions != *exp.Questions {
		fail("%d pergunta(s), esperadas %d", r.Questions, *exp.Questions)
	}
	if exp.Commands != nil && r.Commands != *exp.Commands {
		fail("%d comando(s), esperados %d", r.Commands, *exp.Commands)
	}
	if exp.MaxMS > 0 && row.Got.MS > exp.MaxMS {
		fail("%.0f ms > %.0f ms", row.Got.MS, exp.MaxMS)
	}
	row.Success = len(row.Fail) == 0
	// false success: jazz said "succeeded" and the task did not call for it
	// (or the answer is wrong), or it acted when it should have handed off
	if r.Status == tkSucceeded && !row.Success {
		row.FalseSuccess = true
	}
	if exp.Handled != nil && !*exp.Handled && r.Handled && r.Status == tkSucceeded {
		row.FalseSuccess = true
	}
	return row
}

// eoFixture builds the project of one task.
func eoFixture(base, id, kind string) (string, error) {
	root := filepath.Join(base, id)
	files := map[string]string{}
	git := true
	switch kind {
	case "go", "":
		files = map[string]string{
			"go.mod":              "module evalfix\n\ngo 1.21\n",
			"parser.go":           "package evalfix\n\n// ParseArgs splits the arguments.\nfunc ParseArgs(a []string) []string { return a }\n",
			"parser_test.go":      "package evalfix\n\nimport \"testing\"\n\nfunc TestParseArgs(t *testing.T) {\n\tif len(ParseArgs([]string{\"a\"})) != 1 {\n\t\tt.Fatal(\"x\")\n\t}\n}\n",
			"lexer.go":            "package evalfix\n\nfunc Tokenize(s string) []string { return []string{s} }\n",
			"lexer_test.go":       "package evalfix\n\nimport \"testing\"\n\nfunc TestTokenize(t *testing.T) {\n\tif len(Tokenize(\"a\")) != 1 {\n\t\tt.Fatal(\"x\")\n\t}\n}\n",
			"internal/db/conn.go": "package db\n\nfunc Open() error { return nil }\n",
			"api/handler.go":      "package api\n\nfunc Handle() {}\n",
			"web/handler.go":      "package web\n\nfunc Handle() {}\n",
			"README.md":           "# evalfix\n\nprojeto de teste do jazz eval offline\n",
		}
	case "py", "py-fail":
		want := "3"
		if kind == "py-fail" {
			want = "4"
		}
		files = map[string]string{
			"requirements.txt": "\n",
			"app.py":           "def soma(a, b):\n    return a + b\n",
			"test_app.py":      "import unittest\n\nfrom app import soma\n\n\nclass T(unittest.TestCase):\n    def test_soma(self):\n        self.assertEqual(soma(1, 2), " + want + ")\n\n    def test_zero(self):\n        self.assertEqual(soma(0, 0), 0)\n\n\nif __name__ == '__main__':\n    unittest.main()\n",
		}
	case "norunner":
		files = map[string]string{"README.md": "# nada pra testar\n", "notes.txt": "x\n"}
	case "nogit":
		files = map[string]string{"README.md": "# sem git\n"}
		git = false
	default:
		return "", fmt.Errorf("fixture desconhecida %q", kind)
	}
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			return "", err
		}
	}
	if git {
		if _, err := exec.LookPath("git"); err == nil {
			c := exec.Command("git", "init", "-q")
			c.Dir = root
			if err := c.Run(); err != nil {
				return "", fmt.Errorf("git init: %w", err)
			}
		} else if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			return "", err
		}
	}
	return root, nil
}

// eoFirstWords is the first n words of s ("go test", "python3 -m").
func eoFirstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// eoFixtureBase creates the fixtures' parent dir inside the sandbox's private
// temp dir when there is one (writable and visible inside it; /tmp is a
// tmpfs there), else in the OS temp dir.
func eoFixtureBase(sb *Sandbox) (string, error) {
	tmpRoot := ""
	if sb.Isolates() && sb.Tmp != "" {
		tmpRoot = sb.Tmp
	}
	return os.MkdirTemp(tmpRoot, "jazz-eval-offline-")
}
