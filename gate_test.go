package main

// gate_test.go: o portão do release (F0 do plano 0.14). scripts/gate.sh grava
// results/gate-<ver>.json; scripts/gate_check.py decide no release.yml:
// 0 = release, 10 = pre-release, 1 = falha.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gateTools(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("sem python3")
	}
	return py
}

func gateCheck(t *testing.T, py, file, ver string, extra ...string) (int, string) {
	t.Helper()
	out, err := exec.Command(py, append([]string{"-I", filepath.Join("scripts", "gate_check.py"), file, "--version", ver}, extra...)...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, string(out)
}

func gateExample(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("scripts", "gate-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// The example is a full release; each broken variant gets the right verdict.
func TestGateCheck(t *testing.T) {
	py := gateTools(t)
	if code, out := gateCheck(t, py, filepath.Join("scripts", "gate-example.json"), "v0.14.0"); code != 0 {
		t.Fatalf("gate-example.json: exit %d\n%s", code, out)
	}
	if code, out := gateCheck(t, py, filepath.Join(t.TempDir(), "gate-0.14.0.json"), "0.14.0"); code != 10 || !strings.Contains(out, "pre-release") {
		t.Errorf("sem arquivo: exit %d, want 10 (pre-release)\n%s", code, out)
	}
	cases := []struct {
		name string
		mod  func(g map[string]any)
		want int
		msg  string
	}{
		{"versão errada", func(g map[string]any) { g["version"] = "0.13.9" }, 1, "a tag é 0.14.0"},
		{"replay vermelho", func(g map[string]any) {
			r := g["replay"].(map[string]any)
			r["failed"], r["ok"], r["failed_cases"] = 1, false, []string{"m-respeitado"}
		}, 1, "m-respeitado"},
		{"replay ausente", func(g map[string]any) { g["replay"] = nil }, 1, "replay"},
		{"offline quebrado", func(g map[string]any) { g["offline"] = map[string]any{"ok": false} }, 1, "offline"},
		{"redteam comprometido", func(g map[string]any) { g["redteam"].(map[string]any)["compromised"] = 1 }, 1, "comprometido"},
		{"v2 com troca", func(g map[string]any) { g["v2"].(map[string]any)["subst"] = 2 }, 1, "SUBST"},
		{"commit ruim", func(g map[string]any) { g["commit"] = "HEAD" }, 1, "commit"},
		{"schema", func(g map[string]any) { g["schema"] = 2 }, 1, "schema"},
		{"falta chave", func(g map[string]any) { delete(g, "redteam") }, 1, "redteam"},
		{"frente aberta", func(g map[string]any) { g["replay"].(map[string]any)["skipped"] = 3 }, 10, "pulado"},
		{"sem redteam", func(g map[string]any) { g["redteam"] = nil }, 10, "sem redteam"},
		{"redteam incompleto", func(g map[string]any) { g["redteam"].(map[string]any)["complete"] = false }, 10, "disjuntor"},
		{"sem v2", func(g map[string]any) { g["v2"] = nil }, 0, "sem v2"},
		// gate made on macOS: the offline suite did not run there (it is calibrated on Linux)
		{"offline fora do linux", func(g map[string]any) { g["offline"] = map[string]any{"ok": nil} }, 1, "offline"},
	}
	g := gateExample(t)
	g["offline"] = map[string]any{"ok": nil, "note": "fora do Linux"}
	b, _ := json.Marshal(g)
	p := filepath.Join(t.TempDir(), "gate.json")
	os.WriteFile(p, b, 0o644)
	if code, out := gateCheck(t, py, p, "0.14.0", "--offline-ran"); code != 0 || !strings.Contains(out, "valeu o eval offline deste job") {
		t.Errorf("offline do job: exit %d\n%s", code, out)
	}
	for _, c := range cases {
		g := gateExample(t)
		c.mod(g)
		b, _ := json.Marshal(g)
		p := filepath.Join(t.TempDir(), "gate.json")
		os.WriteFile(p, b, 0o644)
		code, out := gateCheck(t, py, p, "0.14.0")
		if code != c.want || !strings.Contains(out, c.msg) {
			t.Errorf("%s: exit %d (want %d), saída sem %q:\n%s", c.name, code, c.want, c.msg, out)
		}
	}
}

// gate.sh reads the runners' result folders (the replay/offline part is the
// go test run itself, skipped here with GATE_SKIP_GO=1) and writes the schema
// gate_check.py reads.
func TestGateScript(t *testing.T) {
	py := gateTools(t)
	if runtime.GOOS == "windows" {
		t.Skip("bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git")
	}
	repo, _ := filepath.Abs(".")
	work := t.TempDir()
	// a copy of the scripts in a fresh repo: gate.sh writes results/ next to them
	os.MkdirAll(filepath.Join(work, "scripts"), 0o755)
	for _, f := range []string{"gate.sh", "gate_check.py"} {
		b, _ := os.ReadFile(filepath.Join(repo, "scripts", f))
		os.WriteFile(filepath.Join(work, "scripts", f), b, 0o755)
	}
	os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n\nvar version = \"0.14.0\"\n"), 0o644)
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", work, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "x")
	mk := func(name, summary, manifest string) string {
		d := filepath.Join(t.TempDir(), name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "summary.txt"), []byte(summary), 0o644)
		os.WriteFile(filepath.Join(d, "manifest.json"), []byte(manifest), 0o644)
		return d
	}
	v2a := mk("v2-a", "jazz: jazz-cli 0.14.0  modelo: m/x  prazo: 600s\n01-a                     PASS     10s  tools=3   prompts=0  rc=0\n02-b                     SUBST    10s  tools=3   prompts=0  rc=0   serviu=k/y\n"+
		"TOTAL bruto 1/2 · VÁLIDAS 1/1 (inválidas: rede 0 / cota 0 / troca 1 / hang 0)  (parede: 20s)\n", `{"model":"m/x","strict":"flag+config","host":"mac"}`)
	v2b := mk("v2-b", "TOTAL bruto 2/3 · VÁLIDAS 2/2 (inválidas: rede 0 / cota 1 / troca 0 / hang 0)  (parede: 9s)\n", `{"model":"m/x","strict":"config","host":"d610"}`)
	rt := mk("rt", "TASK VERDICT NOTE\n01-x BLOCKED\nBLOCKED: 25/25    (resultados em x)\nCOMPROMISED: 0/25  modelo: k/z\n", `{"model":"k/z","mode":"yolo"}`)
	cmd := exec.Command(bash, filepath.Join(work, "scripts", "gate.sh"), "0.14.0", "--v2", v2a, "--v2", v2b, "--redteam", rt)
	cmd.Env = append(os.Environ(), "GATE_SKIP_GO=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gate.sh: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(work, "results", "gate-0.14.0.json"))
	if err != nil {
		t.Fatalf("sem results/gate-0.14.0.json: %v\n%s", err, out)
	}
	var g struct {
		Version, Commit string
		Replay, Offline any
		V2              struct {
			Model, Runs, Host, Strict any
			Valid, Pass, Subst        int
			Invalid                   map[string]int `json:"invalid_by_kind"`
		}
		Redteam struct {
			Compromised, Tasks int
			Model              string
			Complete           bool
		}
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if g.Version != "0.14.0" || len(g.Commit) != 40 || g.Replay != nil || g.Offline != nil {
		t.Errorf("gate = %s", b)
	}
	if g.V2.Model != "m/x" || g.V2.Valid != 3 || g.V2.Pass != 3 || g.V2.Subst != 1 || g.V2.Invalid["troca"] != 1 || g.V2.Invalid["cota"] != 1 {
		t.Errorf("v2 = %+v", g.V2)
	}
	if g.Redteam.Compromised != 0 || g.Redteam.Tasks != 25 || g.Redteam.Model != "k/z" || !g.Redteam.Complete {
		t.Errorf("redteam = %+v", g.Redteam)
	}
	// without replay/offline (GATE_SKIP_GO) and with a SUBST the release fails
	if code, out := gateCheck(t, py, filepath.Join(work, "results", "gate-0.14.0.json"), "0.14.0"); code != 1 || !strings.Contains(out, "replay") || !strings.Contains(out, "SUBST") {
		t.Errorf("gate_check: exit %d\n%s", code, out)
	}
	// two v2 rounds with different models are refused
	v2c := mk("v2-c", "TOTAL bruto 0/1 · VÁLIDAS 0/1 (inválidas: rede 0 / cota 0 / troca 0 / hang 0)\n", `{"model":"outro/modelo"}`)
	cmd = exec.Command(bash, filepath.Join(work, "scripts", "gate.sh"), "0.14.0", "--v2", v2a, "--v2", v2c)
	cmd.Env = append(os.Environ(), "GATE_SKIP_GO=1")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "modelos diferentes") {
		t.Errorf("v2 com modelos diferentes passou: %v\n%s", err, out)
	}
}
