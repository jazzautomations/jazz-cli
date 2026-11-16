package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		metrics, model, ver                    string // optional: --metrics-out, -m, CLASSIFY_VERSION
	}{
		// rc 124 and the last network line is "esperando" with no "voltou"/"desisti" after: died waiting
		{"morreu-esperando", "morreu-esperando.log", "124", "2700", "", "", "INFRA?", "", "", ""},
		// sympy-16106: 9 min without network in a 45 min deadline (20%) is still a real FAIL
		{"16106-like", "sympy-16106.log", "124", "2700", "FAIL", "2715", "FAIL", "", "", ""},
		// the network came back after 30s and the model failed on its own
		{"voltou-e-falhou", "voltou-e-falhou.log", "3", "2700", "FAIL", "", "FAIL", "", "", ""},
		// rc 124 without jazz's own "tempo esgotado": the LIMIT+60 watchdog killed it
		{"watchdog", "watchdog.log", "124", "600", "", "", "HANG?", "", "", ""},
		{"watchdog-dt", "watchdog.log", "124", "600", "FAIL", "660", "HANG?", "", "", ""},
		// no network trouble, rc 0: PASS
		{"sem-netwait", "sem-netwait-ok.log", "0", "600", "", "", "PASS", "", "", ""},
		{"sem-netwait-check", "sem-netwait-ok.log", "0", "600", "PASS", "180", "PASS", "", "", ""},
		// waits (2m back + 3m gave up) reach half the deadline: not the model's fault, even with rc != 124
		{"rede-metade", "rede-metade.log", "3", "600", "FAIL", "500", "INFRA?", "", "", ""},
		// the same waits in a 45 min deadline: real FAIL
		{"rede-pouca", "rede-metade.log", "3", "2700", "FAIL", "500", "FAIL", "", "", ""},
		// the checker's PASS is never downgraded
		{"pass-com-rede", "morreu-esperando.log", "124", "2700", "PASS", "", "PASS", "", "", ""},
		// jazz's own --timeout (rc 124 + "tempo esgotado"), no network trouble: a real FAIL
		{"timeout-proprio", "sympy-16106.log", "124", "100000", "", "2715", "FAIL", "", "", ""},
		// the provider killed the run (free quota gone; malformed tool call the provider would not
		// deliver): not the agent's failure. v2 on 0.13.4, 2026-11-10: 8 of 11 tasks died like this.
		{"cota-esgotada", "cota-esgotada.log", "1", "600", "FAIL", "174", "INFRA?", "", "", ""},
		{"toolcall-invalida", "toolcall-invalida.log", "1", "600", "FAIL", "547", "INFRA?", "", "", ""},
		// 0.14 phrasings: the chosen model unavailable (exit 75, no hidden swap) and a retired model
		{"pin-cota-014", "pin-cota-014.log", "75", "600", "FAIL", "12", "INFRA?", "", "", ""},
		{"aposentado-014", "aposentado-014.log", "1", "600", "FAIL", "3", "INFRA?", "", "", ""},
		// provider overloaded (5xx) and model that never answered, both with the fallbacks dead too
		{"provedor-503", "provedor-503.log", "1", "600", "FAIL", "32", "INFRA?", "", "", ""},
		{"modelo-mudo", "modelo-mudo.log", "1", "600", "FAIL", "29", "INFRA?", "", "", ""},
		// the agent finished and the checker failed it: a real FAIL, whatever the log says earlier
		{"falha-real", "falha-real.log", "0", "600", "FAIL", "484", "FAIL", "", "", ""},
		// a provider error line with the checker's PASS stays PASS
		{"cota-mas-passou", "cota-esgotada.log", "1", "600", "PASS", "174", "PASS", "", "", ""},
		// no log at all: nothing to blame on the infrastructure
		{"sem-log", "nao-existe.log", "1", "600", "", "", "FAIL", "", "", ""},
		// metrics first (F0 do plano 0.14): another model served the run. The "11/11 gpt-oss" of
		// 03/11 had 54 of 61 requests served by Sonnet: even the checker's PASS is not gpt-oss's.
		{"subst-pass", "sem-netwait-ok.log", "0", "600", "PASS", "180", "SUBST", "metrics-subst.json", "groq/openai/gpt-oss-120b", ""},
		{"subst-fail", "falha-real.log", "0", "600", "FAIL", "484", "SUBST", "metrics-subst.json", "groq/openai/gpt-oss-120b", ""},
		// the requested model served every OK request: the verdict stands
		{"mesmo-modelo-pass", "sem-netwait-ok.log", "0", "600", "PASS", "180", "PASS", "metrics-ok.json", "kilo/cohere/north-mini-code:free", ""},
		{"mesmo-modelo-fail", "falha-real.log", "0", "600", "FAIL", "484", "FAIL", "metrics-ok.json", "kilo/cohere/north-mini-code:free", ""},
		// -m with a bare provider resolves to its default model (Config.ResolveModel): the
		// resolved ref in the metrics is the model asked for, not a substitution; another
		// provider still is
		{"provedor-so-pass", "sem-netwait-ok.log", "0", "600", "PASS", "180", "PASS", "metrics-ok.json", "kilo", ""},
		{"provedor-so-subst", "sem-netwait-ok.log", "0", "600", "PASS", "180", "SUBST", "metrics-subst.json", "groq", ""},
		{"prefixo-nao-e-provedor", "sem-netwait-ok.log", "0", "600", "PASS", "180", "SUBST", "metrics-ok.json", "kilo/cohere", ""},
		// no -m: nothing to compare, the metrics only count OK requests
		{"sem-modelo", "falha-real.log", "0", "600", "FAIL", "484", "FAIL", "metrics-subst.json", "", ""},
		// 0 OK requests (5 of 5 failed): the model never answered, nothing was measured
		{"nostart", "cota-esgotada.log", "1", "600", "FAIL", "174", "INFRA-NOSTART", "metrics-nostart.json", "kilo/cohere/north-mini-code:free", ""},
		// "falha de rede: Failed to parse tool call" on >= 0.13.5 is #33 back (a rejected tool call
		// reported as network): the harness failed, a VALID failure. The legacy log above (no
		// version) stays INFRA?; toolcall-pos33.log is the same shape, synthesized.
		{"pos33", "toolcall-pos33.log", "1", "600", "FAIL", "300", "FAIL-HARNESS", "", "", "0.13.5"},
		{"pos33-0.14", "toolcall-pos33.log", "1", "600", "FAIL", "300", "FAIL-HARNESS", "", "", "0.14.0"},
		{"pos33-antigo", "toolcall-pos33.log", "1", "600", "FAIL", "300", "INFRA?", "", "", "0.13.4"},
		{"pos33-com-metricas", "toolcall-pos33.log", "1", "600", "FAIL", "300", "FAIL-HARNESS", "metrics-ok.json", "kilo/cohere/north-mini-code:free", "0.13.5"},
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Log("sem python3: os casos com métricas não rodam")
	}
	_, pyErr := exec.LookPath("python3")
	for _, c := range cases {
		if c.metrics != "" && pyErr != nil {
			continue
		}
		args := []string{script, filepath.Join(data, c.log), c.rc, c.limit}
		if c.result != "" || c.dt != "" || c.metrics != "" {
			args = append(args, c.result)
		}
		if c.dt != "" || c.metrics != "" {
			args = append(args, c.dt)
		}
		if c.metrics != "" {
			args = append(args, filepath.Join(data, c.metrics), c.model)
		}
		cmd := exec.Command(bash, args...)
		cmd.Env = append(os.Environ(), "CLASSIFY_VERSION="+c.ver)
		out, err := cmd.CombinedOutput()
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
		"TOTAL bruto 1/4 · VÁLIDAS 1/2 (inválidas: rede 1 / cota 0 / troca 0 / hang 1)  (parede: ",
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

// ---------- evals/v2/run.sh: travas do runner (F0 do plano 0.14) ----------

// v2FakeJazz is a jazz -p stand-in: it logs what the runner gave it (isolated
// home, strict flags, the isolated config) and answers the preflight per
// FAKE_PREFLIGHT; every task dies on the free quota, writing the metrics a
// real 0.13.4 run wrote (5 requests, 5 errors, no model served).
const v2FakeJazz = `#!/bin/sh
case "$1" in --version|version) echo "jazz-cli 0.14.0"; exit 0 ;; --help|help) echo "uso: jazz [flags]"; exit 0 ;; esac
out=; model=; prompt=
while [ $# -gt 0 ]; do
  case "$1" in
    --metrics-out) out=$2; shift 2 ;;
    -m) model=$2; shift 2 ;;
    --timeout) shift 2 ;;
    -*) shift ;;
    *) prompt=$1; shift ;;
  esac
done
{
  echo "call home=$JAZZ_HOME plan=$JAZZ_PLAN_CACHE lessons=$JAZZ_LESSONS upd=$JAZZ_NO_UPDATE_CHECK strict=$JAZZ_STRICT_MODEL model=$model"
  echo "config $(tr -d ' \n' < "$JAZZ_HOME/config.json")"
  ls -l "$JAZZ_HOME/config.json" "$JAZZ_HOME/auth.json" | awk '{print "mode", $1}'
} >> "$FAKE_LOG"
if [ "$prompt" = "responda apenas: ok" ] && [ "$FAKE_PREFLIGHT" = ok ]; then
  printf '{"requests": 1, "request_errors": 0, "models": {"%s": 1}}\n' "$model" > "$out"
  echo ok; exit 0
fi
echo "jazz: limite de requisições atingido (Free promotion limit reached); espere ou troque de modelo (/model)"
printf '{"requests": 5, "request_errors": 5, "models": {}}\n' > "$out"
exit 1
`

type v2Env struct {
	home, tasks, log, jazzHome string
}

// v2Setup builds a throwaway HOME (with a jazz config + auth to copy), 11
// fake tasks (CHECK always fails) and the fake jazz. The real evals/v2/tasks
// is never touched (run.sh locks the tasks dir while an agent runs).
func v2Setup(t *testing.T) (v2Env, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("runner bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	e := v2Env{home: t.TempDir(), tasks: t.TempDir()}
	e.log = filepath.Join(t.TempDir(), "fake.log")
	e.jazzHome = filepath.Join(e.home, "jh")
	os.MkdirAll(e.jazzHome, 0o700)
	os.WriteFile(filepath.Join(e.jazzHome, "config.json"), []byte(`{"model": "omni/coder", "fallback": ["kilo/a", "groq/b"], "omni": {"enabled": true}}`), 0o644)
	os.WriteFile(filepath.Join(e.jazzHome, "auth.json"), []byte(`{"kilo": "chave-de-teste"}`), 0o644)
	for i := 1; i <= 11; i++ {
		d := filepath.Join(e.tasks, fmt.Sprintf("%02d-tarefa", i))
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "PROMPT"), []byte("faça a tarefa"), 0o644)
		os.WriteFile(filepath.Join(d, "CHECK"), []byte("echo FAIL; exit 1\n"), 0o644)
		os.WriteFile(filepath.Join(d, "dado.txt"), []byte("x\n"), 0o644)
	}
	bin := filepath.Join(t.TempDir(), "jazz")
	if err := os.WriteFile(bin, []byte(v2FakeJazz), 0o755); err != nil {
		t.Fatal(err)
	}
	return e, bash + "\x00" + bin
}

func v2Run(t *testing.T, e v2Env, tools string, preflight string, args ...string) (string, int) {
	return v2RunEnv(t, e, tools, preflight, nil, args...)
}

func v2RunEnv(t *testing.T, e v2Env, tools string, preflight string, extra []string, args ...string) (string, int) {
	t.Helper()
	bash, bin, _ := strings.Cut(tools, "\x00")
	script, _ := filepath.Abs(filepath.Join("evals", "v2", "run.sh"))
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+e.home, "JAZZ_HOME="+e.jazzHome, "TASKS="+e.tasks, "JAZZ="+bin, "LIMIT=30",
		"FAKE_LOG="+e.log, "FAKE_PREFLIGHT="+preflight, "TMPDIR="+t.TempDir(), "ONLY=", "LANES=1", "JAZZ_STRICT_MODEL=")
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// A model that dies on quota in the first task: 1 invalid (cota) + 10 SKIP,
// the ONLY="…" to resume, a manifest, and the run never touched the real
// JAZZ_HOME (isolated copy of config + auth, 600, plan cache and lessons
// off, the model pinned in the copy), which is gone at the end.
func TestV2RunnerCircuit(t *testing.T) {
	e, tools := v2Setup(t)
	model := "kilo/cohere/north-mini-code:free"
	out, code := v2Run(t, e, tools, "ok", model)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if n := strings.Count(out, " INFRA-NOSTART "); n != 1 {
		t.Errorf("INFRA-NOSTART = %d, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, " SKIP "); n != 10 {
		t.Errorf("SKIP = %d, want 10:\n%s", n, out)
	}
	for _, w := range []string{
		"TOTAL bruto 0/1 · VÁLIDAS 0/0 (inválidas: rede 0 / cota 1 / troca 0 / hang 0)",
		"puladas: 10",
		`ONLY="01 02 03 04 05 06 07 08 09 10 11"`, // the tripped task measured nothing either
		"trava: config",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("saída sem %q:\n%s", w, out)
		}
	}
	runs, _ := filepath.Glob(filepath.Join(e.home, "jazz-eval-results", "v2-*"))
	if len(runs) != 1 {
		t.Fatalf("rodadas = %v", runs)
	}
	run := runs[0]
	if _, err := os.Stat(filepath.Join(run, "02-tarefa.log")); err == nil {
		t.Error("a tarefa 02 rodou depois do disjuntor")
	}
	var man map[string]any
	b, err := os.ReadFile(filepath.Join(run, "manifest.json"))
	if err != nil || json.Unmarshal(b, &man) != nil {
		t.Fatalf("manifest.json: %v\n%s", err, b)
	}
	for _, k := range []string{"version", "binary_sha256", "host", "uname", "python", "model", "strict", "jazz_env", "started"} {
		if _, ok := man[k]; !ok {
			t.Errorf("manifest sem %q: %s", k, b)
		}
	}
	if man["model"] != model || man["version"] != "jazz-cli 0.14.0" || man["strict"] != "config" {
		t.Errorf("manifest = %s", b)
	}
	if env, _ := man["jazz_env"].(map[string]any); env["JAZZ_PLAN_CACHE"] != "off" || env["JAZZ_LESSONS"] != "off" {
		t.Errorf("manifest jazz_env = %v", man["jazz_env"])
	}
	logb, _ := os.ReadFile(e.log)
	log := string(logb)
	if strings.Contains(log, "home="+e.jazzHome+" ") || !strings.Contains(log, "plan=off lessons=off upd=1 strict=1 model="+model) {
		t.Errorf("o jazz não rodou isolado:\n%s", log)
	}
	if !strings.Contains(log, `"fallback":["`+model+`"]`) || !strings.Contains(log, `"omni":{"enabled":false}`) {
		t.Errorf("o modelo não ficou preso no config isolado:\n%s", log)
	}
	if strings.Count(log, "mode -rw-------") < 2 || strings.Contains(log, "mode -rw-r--r--") {
		t.Errorf("config/auth isolados sem modo 600:\n%s", log)
	}
	for _, l := range strings.Split(log, "\n") {
		if h, ok := strings.CutPrefix(l, "call home="); ok {
			if d, _, _ := strings.Cut(h, " "); d != "" {
				if _, err := os.Stat(d); err == nil {
					t.Errorf("JAZZ_HOME isolado (com credenciais) sobrou: %s", d)
				}
			}
		}
	}
	// the real home kept its own config
	if b, _ := os.ReadFile(filepath.Join(e.jazzHome, "config.json")); !strings.Contains(string(b), "omni/coder") {
		t.Errorf("config de verdade mudou: %s", b)
	}
	if _, err := os.Stat(filepath.Join(e.home, "jazz-eval-results", ".lock")); err == nil {
		t.Error("a trava ficou depois da rodada")
	}
	if st, err := os.Stat(e.tasks); err != nil || st.Mode().Perm()&0o700 != 0o700 {
		t.Errorf("tasks/ ficou trancada: %v %v", st.Mode(), err)
	}
}

// A model that does not answer the preflight: "modelo indisponível", exit 3,
// no task burned.
func TestV2RunnerPreflight(t *testing.T) {
	e, tools := v2Setup(t)
	out, code := v2Run(t, e, tools, "fail", "gemini/gemini-3.8-flash")
	if code != 3 || !strings.Contains(out, "modelo indisponível: gemini/gemini-3.8-flash") || !strings.Contains(out, "limite de requisições") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	logs, _ := filepath.Glob(filepath.Join(e.home, "jazz-eval-results", "v2-*", "0*.log"))
	if len(logs) != 0 {
		t.Errorf("tarefas rodaram sem o modelo responder: %v", logs)
	}
	if b, _ := os.ReadFile(e.log); strings.Count(string(b), "call home=") != 1 {
		t.Errorf("chamadas ao jazz = %d, want 1 (só o teste prévio)", strings.Count(string(b), "call home="))
	}
}

// Two rounds on one machine share the quota pool: the second refuses.
func TestV2RunnerLock(t *testing.T) {
	e, tools := v2Setup(t)
	lock := filepath.Join(e.home, "jazz-eval-results", ".lock")
	os.MkdirAll(lock, 0o755)
	os.WriteFile(filepath.Join(lock, "owner"), []byte("pid 1 desde ontem"), 0o644)
	out, code := v2Run(t, e, tools, "ok", "kilo/x")
	if code != 2 || !strings.Contains(out, "outra rodada de eval já está rodando") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Error("a trava de outra rodada foi apagada")
	}
	if b, _ := os.ReadFile(e.log); len(b) > 0 {
		t.Errorf("o jazz rodou com a trava de outra rodada:\n%s", b)
	}
}

// With lanes the breaker stops launching new tasks once a finished one had
// no OK request; whatever was not launched is SKIP.
func TestV2RunnerCircuitLanes(t *testing.T) {
	e, tools := v2Setup(t)
	out, code := v2RunEnv(t, e, tools, "ok", []string{"LANES=2"}, "kilo/x")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	inv, skip := strings.Count(out, " INFRA-NOSTART "), strings.Count(out, " SKIP ")
	if inv < 1 || inv > 3 || inv+skip != 11 {
		t.Errorf("INFRA-NOSTART=%d SKIP=%d, want 1..3 and 11 in all:\n%s", inv, skip, out)
	}
	if !strings.Contains(out, "disjuntor:") {
		t.Errorf("sem a linha do disjuntor:\n%s", out)
	}
}

// eg_served_other (evals/bench/evalguard.sh) is what the preflight uses to
// refuse a run whose model was swapped: the same rule as classify_same_model.
func TestEvalGuardServedOther(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	guard, _ := filepath.Abs(filepath.Join("evals", "bench", "evalguard.sh"))
	for _, c := range []struct{ model, served, want string }{
		{"kilo/cohere/north-mini-code:free", "kilo/cohere/north-mini-code:free", ""},
		{"groq/openai/gpt-oss-120b", "anthropic/claude-sonnet-5-5,groq/openai/gpt-oss-120b", "anthropic/claude-sonnet-5-5"},
		{"gemini", "gemini/gemini-3.8-flash", ""},
		{"gemini", "gemini/gemini-3.8-flash,kilo/cohere/north-mini-code:free", "kilo/cohere/north-mini-code:free"},
		{"kilo/cohere", "kilo/cohere/north-mini-code:free", "kilo/cohere/north-mini-code:free"},
		{"gemini", "-", ""},
	} {
		out, err := exec.Command(bash, "-c", `. "$1"; eg_served_other "$2" "$3"`, "x", guard, c.model, c.served).CombinedOutput()
		if err != nil || string(out) != c.want {
			t.Errorf("eg_served_other %q %q = %q (err %v), want %q", c.model, c.served, out, err, c.want)
		}
	}
}

// eg_dead is the breaker: it trips only when the metrics say 0 OK requests.
// A task with no metrics (a hang killed by the watchdog) is not evidence that
// the model stopped answering and must not skip the rest of the round.
func TestEvalGuardDead(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	guard, _ := filepath.Abs(filepath.Join("evals", "bench", "evalguard.sh"))
	data, _ := filepath.Abs(filepath.Join("evals", "bench", "testdata"))
	for _, c := range []struct {
		file string
		dead bool
	}{
		{"metrics-nostart.json", true},
		{"metrics-ok.json", false},
		{"metrics-subst.json", false},
		{"nao-existe.json", false},
	} {
		err := exec.Command(bash, "-c", `. "$1"; eg_dead "$2"`, "x", guard, filepath.Join(data, c.file)).Run()
		if got := err == nil; got != c.dead {
			t.Errorf("eg_dead %s = %v, want %v", c.file, got, c.dead)
		}
	}
}
