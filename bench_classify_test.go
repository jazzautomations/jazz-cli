package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The bench/eval runners must tell a real FAIL from an attempt the
// infrastructure spoiled (evals/bench/classify_infra.sh): the network ate the
// deadline (INFRA?) or the external watchdog killed jazz (HANG?). The fixtures
// in evals/bench/testdata/ mirror the -v logs of jazz -p on the Dell
// (docs/research/benchmarks.md: 5/20 raw, 5 passed in ~9 valid attempts).
func TestBenchClassifyInfra(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	script, _ := filepath.Abs(filepath.Join("evals", "bench", "classify_infra.sh"))
	data, _ := filepath.Abs(filepath.Join("evals", "bench", "testdata"))
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, log, rc, limit, result, dt, want string
	}{
		// rc 124 and the last network line is "esperando" with no "voltou"/"desisti" after: died waiting
		{"morreu-esperando", "morreu-esperando.log", "124", "2700", "", "", "INFRA?"},
		// sympy-16106: 9 min without network in a 45 min deadline (20%) is still a real FAIL
		{"16106-like", "sympy-16106.log", "124", "2700", "FAIL", "2715", "FAIL"},
		// the network came back after 30s and the model failed on its own
		{"voltou-e-falhou", "voltou-e-falhou.log", "3", "2700", "FAIL", "", "FAIL"},
		// rc 124 without jazz's own "tempo esgotado": the LIMIT+60 watchdog killed it
		{"watchdog", "watchdog.log", "124", "600", "", "", "HANG?"},
		{"watchdog-dt", "watchdog.log", "124", "600", "FAIL", "660", "HANG?"},
		// no network trouble, rc 0: PASS
		{"sem-netwait", "sem-netwait-ok.log", "0", "600", "", "", "PASS"},
		{"sem-netwait-check", "sem-netwait-ok.log", "0", "600", "PASS", "180", "PASS"},
		// waits (2m back + 3m gave up) reach half the deadline: not the model's fault, even with rc != 124
		{"rede-metade", "rede-metade.log", "3", "600", "FAIL", "500", "INFRA?"},
		// the same waits in a 45 min deadline: real FAIL
		{"rede-pouca", "rede-metade.log", "3", "2700", "FAIL", "500", "FAIL"},
		// the checker's PASS is never downgraded
		{"pass-com-rede", "morreu-esperando.log", "124", "2700", "PASS", "", "PASS"},
		// jazz's own --timeout (rc 124 + "tempo esgotado"), no network trouble: a real FAIL
		{"timeout-proprio", "sympy-16106.log", "124", "100000", "", "2715", "FAIL"},
		// the provider killed the run (free quota gone; malformed tool call the provider would not
		// deliver): not the agent's failure. v2 on 0.13.4, 2026-11-10: 8 of 11 tasks died like this.
		{"cota-esgotada", "cota-esgotada.log", "1", "600", "FAIL", "174", "INFRA?"},
		{"toolcall-invalida", "toolcall-invalida.log", "1", "600", "FAIL", "547", "INFRA?"},
		// provider overloaded (5xx) and model that never answered, both with the fallbacks dead too
		{"provedor-503", "provedor-503.log", "1", "600", "FAIL", "32", "INFRA?"},
		{"modelo-mudo", "modelo-mudo.log", "1", "600", "FAIL", "29", "INFRA?"},
		// the agent finished and the checker failed it: a real FAIL, whatever the log says earlier
		{"falha-real", "falha-real.log", "0", "600", "FAIL", "484", "FAIL"},
		// a provider error line with the checker's PASS stays PASS
		{"cota-mas-passou", "cota-esgotada.log", "1", "600", "PASS", "174", "PASS"},
		// no log at all: nothing to blame on the infrastructure
		{"sem-log", "nao-existe.log", "1", "600", "", "", "FAIL"},
	}
	for _, c := range cases {
		args := []string{script, filepath.Join(data, c.log), c.rc, c.limit}
		if c.result != "" || c.dt != "" {
			args = append(args, c.result)
		}
		if c.dt != "" {
			args = append(args, c.dt)
		}
		out, err := exec.Command(bash, args...).CombinedOutput()
		got := strings.TrimSpace(string(out))
		if err != nil || got != c.want {
			t.Errorf("%s: classify_infra %v = %q (err %v), want %q", c.name, args[1:], got, err, c.want)
		}
	}
}

// lib.sh (the port of the Dell's bench helpers) prints one line per task and
// the final "TOTAL bruto X/N · VÁLIDAS Y/V (...)" line; the per-task line keeps
// the "NAME PASS|FAIL DTs tools=.. rc=.." shape improve_load.go reads, and an
// INFRA?/HANG? line is deliberately outside that shape.
func TestBenchLibTotalLine(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	lib, _ := filepath.Abs(filepath.Join("evals", "bench", "lib.sh"))
	data, _ := filepath.Abs(filepath.Join("evals", "bench", "testdata"))
	sum := filepath.Join(t.TempDir(), "summary.txt")
	sh := `. "$1"; bench_init "$2" 600
record ok PASS 0 180 "$3/sem-netwait-ok.log"
record hang FAIL 124 660 "$3/watchdog.log" "f2p 0/1"
record rede FAIL 3 500 "$3/rede-metade.log"
record real FAIL 3 250 "$3/voltou-e-falhou.log" "FAIL f2p 0/1"
bench_finish "lanes: 1"`
	out, err := exec.Command(bash, "-c", sh, "lib", lib, sum, data).CombinedOutput()
	if err != nil {
		t.Fatalf("lib.sh: %v\n%s", err, out)
	}
	s := string(out)
	for _, w := range []string{
		"ok                       PASS     180s  tools=3   rc=0",
		"hang                     HANG?    660s  tools=?   rc=124 f2p 0/1",
		"rede                     INFRA?   500s  tools=?   rc=3   rede=300s",
		"real                     FAIL     250s  tools=6   rc=3   rede=30s  FAIL f2p 0/1",
		"TOTAL bruto 1/4 · VÁLIDAS 1/2 (2 inválidas: rede 1 / hang 1)  (parede: ",
		"s, lanes: 1)",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("lib.sh output misses %q:\n%s", w, s)
		}
	}
	b, err := os.ReadFile(sum)
	if err != nil || string(b) != s {
		t.Errorf("summary.txt differs from stdout (err %v):\n%s", err, b)
	}
	// the improve harness only reads PASS/FAIL lines: the invalid ones stay out
	tasks := imParseSummary(sum)
	if len(tasks) != 2 || !tasks[0].Pass || tasks[0].Task != "ok" || tasks[1].Pass || tasks[1].Task != "real" || tasks[1].RC != 3 {
		t.Errorf("imParseSummary = %+v, want ok(PASS) and real(FAIL rc=3) only", tasks)
	}
}
