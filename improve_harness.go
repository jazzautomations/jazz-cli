package main

// improve_harness.go: `jazz improve --harness` — the scoreboard-like report
// over many runs (sessions, events, eval runner output). It only PROPOSES:
// prompt-rule text, skill-trigger words, config tweaks and model/provider
// observations, written to .jazz/improve-report.md for a human to review.
// Nothing here edits code, prompts or config (Self-Harness/CRO scope, not DGM:
// docs/research/metaharness-self-improvement.md §2 "DGM lesson").

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// imModelStat is what the runs say about one model.
type imModelStat struct {
	Model                   string
	Requests, Errors, R429  int
	Cutoffs, Timeouts, Runs int
	Lat                     []float64 // ms
	MaxOut                  int       // largest output (tokens) of a cut response
}

// imReport is the aggregate behind the markdown report.
type imReport struct {
	Runs     []*imRun
	Evals    []imEvalTask
	Cands    []imCand
	Models   []*imModelStat
	Classes  map[string]int // failure class -> occurrences
	ClassSes map[string][]string
	Words    map[string]map[string]int // class -> task word -> count
	Exts     map[string]map[string]int // class -> file extension -> count
}

func imAggregate(runs []*imRun, evals []imEvalTask) *imReport {
	rep := &imReport{Runs: runs, Evals: evals, Classes: map[string]int{}, ClassSes: map[string][]string{}, Words: map[string]map[string]int{}, Exts: map[string]map[string]int{}}
	rep.Cands = imExtract(runs)
	byModel := map[string]*imModelStat{}
	stat := func(m string) *imModelStat {
		if m == "" {
			m = "(desconhecido)"
		}
		s := byModel[m]
		if s == nil {
			s = &imModelStat{Model: m}
			byModel[m] = s
		}
		return s
	}
	for _, r := range runs {
		stat(r.Model).Runs++
		if len(r.Events) > 0 {
			for _, e := range r.Events {
				if e.str("kind") != "request.end" {
					continue
				}
				s := stat(e.str("model"))
				s.Requests++
				if e.str("error") != "" {
					s.Errors++
				}
				if e.int("status") == 429 {
					s.R429++
				}
				if f := e.str("finish"); f == "length" || f == "max_tokens" {
					s.Cutoffs++
					s.MaxOut = max(s.MaxOut, e.int("out"))
				}
				if l := e.num("latency_ms"); l > 0 && e.str("error") == "" {
					s.Lat = append(s.Lat, l)
				}
			}
		} else {
			for _, l := range r.MsgLat {
				s := stat(l.Model)
				s.Requests++
				s.Lat = append(s.Lat, l.MS)
				if l.Stop == "length" || l.Stop == "max_tokens" {
					s.Cutoffs++
					s.MaxOut = max(s.MaxOut, l.Out)
				}
			}
			if len(r.MsgLat) == 0 && r.Requests > 0 {
				stat(r.Model).Requests += r.Requests
			}
		}
		for m, n := range r.R429 {
			if len(r.Events) == 0 { // events already counted their 429s
				stat(m).R429 += n
			}
		}
		if r.Status == string(StopTimeout) {
			stat(r.Model).Timeouts++
		}
	}
	// which runs hit which class (for counts, refs and trigger words)
	for _, c := range rep.Cands {
		rep.Classes[c.Class] += c.Count
		for _, s := range c.Sessions {
			if !imHas(rep.ClassSes[c.Class], s) {
				rep.ClassSes[c.Class] = append(rep.ClassSes[c.Class], s)
			}
		}
	}
	runByID := map[string]*imRun{}
	for _, r := range runs {
		runByID[r.ID] = r
	}
	for class, ids := range rep.ClassSes {
		for _, id := range ids {
			r := runByID[id]
			if r == nil {
				continue
			}
			if rep.Words[class] == nil {
				rep.Words[class], rep.Exts[class] = map[string]int{}, map[string]int{}
			}
			for _, w := range imTerms(r.Task) {
				rep.Words[class][w]++
			}
			seen := map[string]bool{}
			for _, c := range r.Calls {
				if c.Path == "" {
					continue
				}
				if x := strings.ToLower(filepath.Ext(c.Path)); x != "" && !seen[x] {
					seen[x] = true
					rep.Exts[class][x]++
				}
			}
		}
	}
	for _, s := range byModel {
		rep.Models = append(rep.Models, s)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		if rep.Models[i].Requests != rep.Models[j].Requests {
			return rep.Models[i].Requests > rep.Models[j].Requests
		}
		return rep.Models[i].Model < rep.Models[j].Model
	})
	return rep
}

// imPct is the p-th percentile (nearest rank) of xs.
func imPct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	k := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[max(0, min(k, len(s)-1))]
}

// imRuleFor is the prompt-rule text a failure class suggests.
var imRuleFor = map[string]string{
	"tool-error": "Quando uma ferramenta falhar, leia a mensagem e mude a chamada; a mesma chamada com o mesmo erro não deve se repetir (hoje é só regra: um contador por assinatura de erro no harness tornaria isso mecânico).",
	"cmd-fixed":  "Antes de usar uma ferramenta de linha de comando que pode não existir (rg, pytest, python), confira com `command -v` uma vez e use a alternativa disponível.",
	"cutoff":     "Arquivo novo grande: esqueleto primeiro, depois `edit` com `edits[]` em blocos; nunca um `write` gigante.",
	"guard":      "Quando uma guarda recusar uma ação, siga o motivo dado na recusa em vez de tentar a mesma ação de outra forma.",
	"spiral":     "Duas chamadas bloqueadas seguidas: pare e troque de ferramenta ou de alvo.",
	"revert":     "Não mexa em testes existentes; se o harness reverter uma mudança, corrija o código sob teste.",
	"loop":       "Depois da segunda tentativa igual sem progresso, mude de abordagem e diga por quê.",
	"until":      "Com `--until` falhando, leia a saída inteira do comando antes de editar de novo.",
	"quality":    "Antes de encerrar, rode os testes e o lint que o projeto já tem.",
	"test-fixed": "Rode o teste mais específico logo depois de cada edição, não só no fim.",
	"timeout":    "Perto do prazo, termine com verificação e resumo em vez de abrir trabalho novo.",
}

// imSkillFor maps a failure class to the built-in skill that should have fired.
var imSkillFor = map[string]string{
	"tool-error": "surgical-change", "cmd-fixed": "verify-done", "cutoff": "plan-small", "revert": "write-tests",
	"test-fixed": "write-tests", "quality": "verify-done", "until": "verify-done", "loop": "plan-small", "spiral": "surgical-change",
}

func imTop(m map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	var l []kv
	for k, v := range m {
		l = append(l, kv{k, v})
	}
	sort.Slice(l, func(i, j int) bool {
		if l[i].v != l[j].v {
			return l[i].v > l[j].v
		}
		return l[i].k < l[j].k
	})
	var out []string
	for i := 0; i < len(l) && i < n; i++ {
		out = append(out, fmt.Sprintf("%s (%d)", l[i].k, l[i].v))
	}
	return out
}

// imRenderReport writes the markdown report.
func imRenderReport(rep *imReport, now time.Time) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	p("# jazz improve — relatório do harness\n\n")
	p("> **proposta — revisar antes de aplicar.** Gerado por `jazz improve --harness` em %s a partir de execuções medidas. O jazz não aplica nada disto sozinho: regras de prompt, gatilhos de skill e config mudam só por um humano, de preferência com um A/B pareado (`evals/v2`). n pequeno = ruído; os números abaixo são contagens, não taxas com intervalo de confiança.\n\n", now.Format("2006-01-02 15:04"))
	nSess, nLog := 0, 0
	for _, r := range rep.Runs {
		if r.Source == "log" {
			nLog++
		} else {
			nSess++
		}
	}
	p("## Dados\n\n- execuções: %d (%d sessões do jazz, %d logs de avaliação sem sessão)\n", len(rep.Runs), nSess, nLog)
	if len(rep.Evals) > 0 {
		pass := 0
		dirs := map[string]bool{}
		for _, t := range rep.Evals {
			dirs[t.RunDir] = true
			if t.Pass {
				pass++
			}
		}
		p("- avaliações (`summary.txt`): %d tarefas em %d rodadas, %d PASS\n", len(rep.Evals), len(dirs), pass)
	}
	p("\n")
	if len(rep.Evals) > 0 {
		p("### Placar por tarefa (avaliações)\n\n| tarefa | rodadas | PASS | tempo p50 | tempo p95 | ferramentas p50 |\n|---|---|---|---|---|---|\n")
		byTask := map[string][]imEvalTask{}
		var names []string
		for _, t := range rep.Evals {
			if byTask[t.Task] == nil {
				names = append(names, t.Task)
			}
			byTask[t.Task] = append(byTask[t.Task], t)
		}
		sort.Strings(names)
		for _, n := range names {
			ts := byTask[n]
			var secs, tools []float64
			pass := 0
			for _, t := range ts {
				secs = append(secs, float64(t.Secs))
				tools = append(tools, float64(t.Tools))
				if t.Pass {
					pass++
				}
			}
			p("| %s | %d | %d | %.0fs | %.0fs | %.0f |\n", n, len(ts), pass, imPct(secs, 50), imPct(secs, 95), imPct(tools, 50))
		}
		p("\n")
	}
	p("## Classes de falha recorrentes\n\n")
	if len(rep.Cands) == 0 {
		p("Nenhuma falha recorrente encontrada.\n\n")
	} else {
		p("| classe | ocorrências | sessões | exemplo |\n|---|---|---|---|\n")
		var classes []string
		for c := range rep.Classes {
			classes = append(classes, c)
		}
		sort.Slice(classes, func(i, j int) bool {
			if rep.Classes[classes[i]] != rep.Classes[classes[j]] {
				return rep.Classes[classes[i]] > rep.Classes[classes[j]]
			}
			return classes[i] < classes[j]
		})
		for _, c := range classes {
			ex := ""
			for _, cd := range rep.Cands {
				if cd.Class == c {
					ex = oneLine(cd.Text, 110)
					break
				}
			}
			p("| %s | %d | %d (%s) | %s |\n", c, rep.Classes[c], len(rep.ClassSes[c]), strings.Join(acClipList(rep.ClassSes[c], 3), ", "), imMDCell(imAnon(ex)))
		}
		p("\n### Principais achados\n\n")
		for i, c := range rep.Cands {
			if i >= 15 {
				p("- … +%d\n", len(rep.Cands)-i)
				break
			}
			p("- **%s** ×%d em %d sessão(ões) — %s", c.Class, c.Count, len(c.Sessions), imAnon(oneLine(c.Text, 200)))
			if len(c.Sessions) > 0 {
				p(" (sessões: %s)", strings.Join(acClipList(c.Sessions, 4), ", "))
			}
			p("\n")
		}
		p("\n")
	}
	p("## Candidatas a regra de prompt (texto)\n\n")
	found := false
	for _, c := range imSortedClasses(rep.Classes) {
		if r, ok := imRuleFor[c]; ok && rep.Classes[c] >= 2 {
			p("- (%s, %d×) %s\n", c, rep.Classes[c], r)
			found = true
		}
	}
	if !found {
		p("Nenhuma classe com ≥ 2 ocorrências.\n")
	}
	p("\n## Candidatas a gatilho de skill\n\n")
	found = false
	for _, c := range imSortedClasses(rep.Classes) {
		sk, ok := imSkillFor[c]
		if !ok || len(rep.Words[c]) == 0 {
			continue
		}
		found = true
		p("- `%s` teria ajudado em **%s**: palavras dos pedidos %s", sk, c, strings.Join(imTop(rep.Words[c], 6), ", "))
		if ex := imTop(rep.Exts[c], 4); len(ex) > 0 {
			p("; arquivos %s", strings.Join(ex, ", "))
		}
		p("\n")
	}
	if !found {
		p("Sem dados de pedido suficientes.\n")
	}
	p("\n## Ajustes de config sugeridos\n\n")
	tweaks := imTweaks(rep)
	if len(tweaks) == 0 {
		p("Nenhum.\n")
	}
	for _, t := range tweaks {
		p("- %s\n", t)
	}
	p("\n## Modelos e provedores\n\n| modelo | execuções | requisições | erros | 429 | cortes de saída | prazos estourados | latência p50 | latência p95 |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range rep.Models {
		lat50, lat95 := "—", "—"
		if len(s.Lat) > 0 {
			lat50, lat95 = obFmtDur(imPct(s.Lat, 50)), obFmtDur(imPct(s.Lat, 95))
		}
		cut := fmt.Sprint(s.Cutoffs)
		if s.MaxOut > 0 {
			cut += fmt.Sprintf(" (até %d tok)", s.MaxOut)
		}
		p("| %s | %d | %d | %d | %d | %s | %d | %s | %s |\n", s.Model, s.Runs, s.Requests, s.Errors, s.R429, cut, s.Timeouts, lat50, lat95)
	}
	p("\nLatência só existe para sessões com eventos (v0.6+) ou com metadados de mensagem; logs de avaliação antigos contam só 429.\n")
	return imAnon(b.String())
}

func imSortedClasses(m map[string]int) []string {
	var l []string
	for k := range m {
		l = append(l, k)
	}
	sort.Slice(l, func(i, j int) bool {
		if m[l[i]] != m[l[j]] {
			return m[l[i]] > m[l[j]]
		}
		return l[i] < l[j]
	})
	return l
}

func imMDCell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

// imTweaks proposes config changes the numbers support.
func imTweaks(rep *imReport) []string {
	var out []string
	if n := rep.Classes["cutoff"]; n > 0 {
		out = append(out, fmt.Sprintf("%d corte(s) de saída: confira `max_output_tokens` / `models.<ref>.max_out` dos modelos da tabela abaixo e mantenha `cutoff_continue` ligado.", n))
	}
	if n := rep.Classes["timeout"]; n > 0 {
		out = append(out, fmt.Sprintf("%d execução(ões) estouraram o prazo: aumente `--timeout-grace` (folga para salvar) ou o `--timeout` das tarefas lentas; veja quais modelos têm p95 alto.", n))
	}
	budget := 0
	for _, c := range rep.Cands {
		if c.Tag == "orçamento" {
			budget += c.Count
		}
	}
	if budget >= 3 {
		out = append(out, fmt.Sprintf("o orçamento de exploração recusou %d leituras: avalie `budget: normal` para essas tarefas (A/B antes de mudar o padrão).", budget))
	}
	r429 := 0
	for _, s := range rep.Models {
		r429 += s.R429
	}
	if r429 >= 3 {
		out = append(out, fmt.Sprintf("%d respostas HTTP 429: mantenha `fallback_order: score` e pelo menos um modelo de outro provedor no `fallback`.", r429))
	}
	if rep.Classes["guard"] >= 3 {
		out = append(out, "guardas recusaram ações repetidamente: revise se as regras de permissão (`permissions`) do projeto deveriam liberar esses comandos.")
	}
	return out
}

func cmdImproveHarness(root string, o imOpts, w io.Writer) int {
	var src imSources
	if len(o.from) > 0 {
		src = imScan(o.from...)
	} else {
		src.Runs = imProjectRuns(root, o.n, o.ids)
		if o.n == 1 { // the default of -n is for lessons; the report wants every run
			src.Runs = imProjectRuns(root, 0, o.ids)
		}
	}
	if len(src.Runs) == 0 && len(src.Evals) == 0 {
		fmt.Fprintln(os.Stderr, "jazz: nada para agregar (rode jazz nesta pasta ou passe --from <pasta de resultados>)")
		return 1
	}
	rep := imAggregate(src.Runs, src.Evals)
	md := imRenderReport(rep, time.Now())
	out := o.out
	if out == "" {
		out = filepath.Join(lsProjectRoot(root), ".jazz", "improve-report.md")
	}
	if out == "-" {
		fmt.Fprint(w, md)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	fmt.Fprintf(w, "relatório (proposta — revisar antes de aplicar): %s · %d execuções · %d classes de falha\n", out, len(src.Runs), len(rep.Classes))
	return 0
}
