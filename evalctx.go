package main

// evalctx.go: `jazz eval context` — the gate for any change to what the model
// sees of the history (ctxview.go). It replays decision points of recorded
// sessions: the same model gets the history as it was and the history under
// the policy, and the two next actions are compared. The full history is also
// asked twice, because a model does not agree with itself every time: that
// rate is the ceiling the policy is judged against.
//
// The criterion is the one of "Tools Need Not Speak in Text" (arXiv
// 2611.06500): a compressed observation is good when it preserves the next
// action, not when it preserves the text.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ecOpts struct {
	dir        string // sessions root, one project dir, or one .jsonl
	match      string // only session paths containing this
	n          int    // decision points to replay
	perSession int    // at most this many points per session
	model      string
	pol        cvPolicy
	compare    bool // also replay the stale-read policies on the same points
	dry        bool // no model calls: only what the policy would save
	out        string
	timeout    time.Duration
}

// ecPoint is one recorded decision: the history the model had and what it did.
type ecPoint struct {
	File   string
	Entry  string // id of the assistant entry
	idx    int    // its index on the session path
	Saved  int    // bytes the policy removes from this history
	Before int
}

// ecAct is the action of a reply, reduced to what is compared.
type ecAct struct {
	Name   string `json:"name"`             // tool name; "(text)" when the model answered without a call
	Target string `json:"target,omitempty"` // file, pattern, or the first words of a command
	Args   string `json:"args,omitempty"`   // canonical arguments
}

type ecRow struct {
	File     string `json:"file"`
	Entry    string `json:"entry"`
	Before   int    `json:"bytes_before"`
	After    int    `json:"bytes_after"`
	Masked   int    `json:"masked"`
	Recorded ecAct  `json:"recorded"`
	Full     ecAct  `json:"full"`
	Full2    ecAct  `json:"full2"`
	View     ecAct  `json:"view"`
	Infra    string `json:"infra,omitempty"` // provider failure: the point does not count
	InFull   int    `json:"in_full,omitempty"`
	InView   int    `json:"in_view,omitempty"`
	// Others: the same point under the other policies of --compare
	Others []ecAlt `json:"others,omitempty"`
}

// ecAlt is one more policy replayed on the same point.
type ecAlt struct {
	Name   string `json:"name"`
	After  int    `json:"bytes_after"`
	Masked int    `json:"masked"`
	View   ecAct  `json:"view"`
	In     int    `json:"in,omitempty"`
}

// ecVariants are the policies --compare adds to the one given by the flags.
func ecVariants(base cvPolicy) []struct {
	name string
	pol  cvPolicy
} {
	st, ed := base, base
	st.Reads, ed.Reads = "stale", "edited"
	return []struct {
		name string
		pol  cvPolicy
	}{{"relidas", st}, {"relidas ou editadas", ed}}
}

func ecAction(calls []ToolCall) ecAct {
	if len(calls) == 0 {
		return ecAct{Name: "(text)"}
	}
	tc := calls[0]
	a := ecAct{Name: tc.Name, Args: strings.TrimSpace(tc.Args)}
	var m map[string]any
	if json.Unmarshal([]byte(tc.Args), &m) != nil {
		return a
	}
	if b, err := json.Marshal(m); err == nil { // map keys come out sorted
		a.Args = string(b)
	}
	for _, k := range []string{"path", "file", "file_path", "pattern", "query", "url", "name"} {
		if s, ok := m[k].(string); ok && s != "" {
			a.Target = s
			return a
		}
	}
	for _, k := range []string{"command", "cmd"} {
		if s, ok := m[k].(string); ok {
			a.Target = ecCmdHead(s)
			return a
		}
	}
	return a
}

// ecCmdHead is the first two words of the last command of a line ("cd x &&
// go test ./..." → "go test"): enough to tell a test run from a grep.
func ecCmdHead(s string) string {
	for _, sep := range []string{"&&", ";", "\n"} {
		if i := strings.LastIndex(s, sep); i >= 0 && strings.TrimSpace(s[i+len(sep):]) != "" {
			s = s[i+len(sep):]
		}
	}
	f := strings.Fields(s)
	if len(f) > 2 {
		f = f[:2]
	}
	return strings.Join(f, " ")
}

// ecSessionFiles lists the session files under dir (a root of project dirs,
// one project dir, or a single file), skipping subagent sessions.
func ecSessionFiles(dir, match string) []string {
	var out []string
	add := func(p string) {
		if strings.HasSuffix(p, ".jsonl") && (match == "" || strings.Contains(p, match)) {
			out = append(out, p)
		}
	}
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		add(dir)
		return out
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			add(p)
			continue
		}
		sub, _ := os.ReadDir(p)
		for _, s := range sub {
			if !s.IsDir() {
				add(filepath.Join(p, s.Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

// ecScan finds the decision points of one session where the policy changes
// the history: assistant entries that called a tool.
func ecScan(file string, pol cvPolicy) []ecPoint {
	ents, err := sessReadEntries(file)
	if err != nil || len(ents) == 0 {
		return nil
	}
	path := sessPath(ents, "")
	var pts []ecPoint
	var msgs []Message
	for i, se := range path {
		if se.Tampered {
			return nil // altered outside jazz: not evidence
		}
		switch se.T {
		case "compact":
			msgs = nil
		case "rewind":
			if se.N >= 0 && se.N <= len(msgs) {
				msgs = msgs[:se.N]
			}
		case "msg":
			if se.Msg == nil {
				continue
			}
			if se.Msg.Role == "assistant" && len(se.Msg.ToolCalls) > 0 {
				if _, st := cvMask(msgs, pol); st.Masked > 0 {
					pts = append(pts, ecPoint{File: file, Entry: se.ID, idx: i, Saved: st.Before - st.After, Before: st.Before})
				}
			}
			msgs = append(msgs, *se.Msg)
		}
	}
	return pts
}

// ecSpread picks k items evenly spaced over n (deterministic: no sampling
// noise between two runs of the gate).
func ecSpread(n, k int) []int {
	if k >= n {
		k = n
	}
	out := make([]int, 0, k)
	for j := 0; j < k; j++ {
		out = append(out, (2*j+1)*n/(2*k))
	}
	return out
}

func ecPick(files []string, o ecOpts) (all int, picked []ecPoint) {
	var pool []ecPoint
	for _, f := range files {
		pts := ecScan(f, o.pol)
		all += len(pts)
		for _, i := range ecSpread(len(pts), o.perSession) {
			pool = append(pool, pts[i])
		}
	}
	for _, i := range ecSpread(len(pool), o.n) {
		picked = append(picked, pool[i])
	}
	return all, picked
}

// ecHistory rebuilds what the model had in front of it at a point.
func ecHistory(p ecPoint) (msgs []Message, system string, recorded []ToolCall, err error) {
	ents, err := sessReadEntries(p.File)
	if err != nil {
		return nil, "", nil, err
	}
	path := sessPath(ents, "")
	if p.idx >= len(path) || path[p.idx].Msg == nil {
		return nil, "", nil, errors.New("ponto fora da sessão")
	}
	r := sessReplay(path[:p.idx])
	return repairHistory(r.msgs), r.system, path[p.idx].Msg.ToolCalls, nil
}

// ecAsk is one model call (a var so tests can stub it).
var ecAsk = func(ctx context.Context, cfg *Config, ref, system string, msgs []Message) (*ChatResponse, error) {
	pname, mid := SplitModel(ref)
	prov, err := cfg.NewProvider(pname)
	if err != nil {
		return nil, err
	}
	ms := cfg.ModelSettings(ref)
	if ms.Tools == "text" || ms.Tools == "none" {
		return nil, fmt.Errorf("%s não tem chamada de ferramenta nativa: escolha outro modelo com --model", ref)
	}
	if !cfg.SupportsImages(ref) {
		msgs = acStripImages(msgs, ref)
	}
	zero := 0.0
	req := ChatRequest{Model: mid, System: system, Messages: msgs, Tools: buildToolSpecs(true, true), MaxTokens: 1024, Effort: ms.Effort, Temperature: &zero}
	for try := 0; ; try++ {
		resp, err := prov.Chat(ctx, req, nil)
		var ae *APIError
		if err != nil && try == 0 && errors.As(err, &ae) && ae.Status == 429 && !ae.Daily && ae.RetryAfter > 0 && ae.RetryAfter <= 90*time.Second {
			select {
			case <-time.After(ae.RetryAfter + time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return resp, err
	}
}

func cmdEvalContext(root string, args []string, w io.Writer) int {
	const usage = "uso: jazz eval context [--sessions DIR|ARQ] [--match TXT] [--n 40] [--per-session 3] [--model REF] [--keep 3] [--min 1000] [--spare read] [--reads stale|edited] [--compare] [--dry] [--out DIR]"
	o := ecOpts{dir: filepath.Join(dataDir(), "sessions"), n: 40, perSession: 3, pol: cvDefault(), out: filepath.Join(root, "results"), timeout: 3 * time.Minute}
	for i := 0; i < len(args); i++ {
		val := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		num := func(dst *int) bool {
			n, err := strconv.Atoi(val())
			if err != nil || n < 0 {
				return false
			}
			*dst = n
			return true
		}
		ok := true
		switch strings.TrimLeft(args[i], "-") {
		case "sessions":
			o.dir = val()
		case "match":
			o.match = val()
		case "model":
			o.model = val()
		case "out":
			o.out = val()
		case "n":
			ok = num(&o.n)
		case "per-session":
			ok = num(&o.perSession)
		case "keep":
			ok = num(&o.pol.Keep)
		case "min":
			ok = num(&o.pol.MinBytes)
		case "spare":
			o.pol.Spare = pfSplitFlag(val())
		case "reads":
			o.pol.Reads = val()
			ok = o.pol.Reads == "stale" || o.pol.Reads == "edited"
		case "compare":
			o.compare = true
		case "dry":
			o.dry = true
		default:
			ok = false
		}
		if !ok {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
	}
	files := ecSessionFiles(o.dir, o.match)
	all, pts := ecPick(files, o)
	fmt.Fprintf(w, "jazz eval context · política: %d resultados recentes literais, antigos acima de %d bytes viram aviso\n", o.pol.Keep, o.pol.MinBytes)
	if len(o.pol.Spare) > 0 {
		fmt.Fprintf(w, "poupados (nunca viram aviso): %s\n", strings.Join(o.pol.Spare, ", "))
	}
	switch o.pol.Reads {
	case "stale":
		fmt.Fprintln(w, "leituras: só vira aviso a que foi relida depois")
	case "edited":
		fmt.Fprintln(w, "leituras: só vira aviso a que foi relida ou cujo arquivo foi editado depois")
	}
	fmt.Fprintf(w, "%d sessões · %d decisões em que a política muda o histórico · %d sorteadas (no máximo %d por sessão)\n", len(files), all, len(pts), o.perSession)
	if len(pts) == 0 {
		fmt.Fprintln(w, "nada pra medir")
		return 1
	}
	saved, before := 0, 0
	for _, p := range pts {
		saved, before = saved+p.Saved, before+p.Before
	}
	fmt.Fprintf(w, "histórico nessas decisões: %d KB → %d KB (−%.1f%%)\n", before/1000, (before-saved)/1000, 100*float64(saved)/float64(max(before, 1)))
	if o.dry {
		return 0
	}
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	ref := o.model
	if ref == "" {
		ref = cfg.Model
	}
	if ref == "" {
		ref = cfg.PickDefaultModel()
	}
	if ref, err = cfg.ResolveModel(ref); err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	fmt.Fprintf(w, "modelo: %s · 3 chamadas por decisão (histórico inteiro duas vezes, com a política uma)\n\n", ref)
	var rows []ecRow
	for k, p := range pts {
		row := ecReplayPoint(cfg, ref, p, o)
		rows = append(rows, row)
		mark := "="
		switch {
		case row.Infra != "":
			mark = "INFRA " + oneLine(row.Infra, 60)
		case row.Full.Name != row.View.Name:
			mark = "≠ " + row.Full.Name + " → " + row.View.Name
		}
		fmt.Fprintf(w, "  %2d/%d %s %s\n", k+1, len(pts), filepath.Base(p.File)+"@"+p.Entry, mark)
	}
	s := ecSummarize(rows)
	fmt.Fprintln(w)
	fmt.Fprint(w, s.text())
	alts := map[string]any{}
	if o.compare {
		for k, v := range ecVariants(o.pol) {
			as := ecSummarize(ecAltRows(rows, k))
			alts[v.name] = as
			fmt.Fprintf(w, "\nleituras: %s\n%s", v.name, as.altText())
		}
	}
	if o.out != "" {
		if err := os.MkdirAll(o.out, 0o755); err == nil {
			p := filepath.Join(o.out, "context-"+time.Now().Format("20060102-150405")+".json")
			b, _ := json.MarshalIndent(map[string]any{"model": ref, "keep": o.pol.Keep, "min_bytes": o.pol.MinBytes, "spare": o.pol.Spare, "reads": o.pol.Reads, "summary": s, "others": alts, "rows": rows}, "", " ")
			if os.WriteFile(p, b, 0o644) == nil {
				fmt.Fprintln(w, "detalhe:", p)
			}
		}
	}
	if s.Valid == 0 {
		return 1
	}
	return 0
}

func ecReplayPoint(cfg *Config, ref string, p ecPoint, o ecOpts) ecRow {
	row := ecRow{File: filepath.Base(filepath.Dir(p.File)) + "/" + filepath.Base(p.File), Entry: p.Entry}
	msgs, system, rec, err := ecHistory(p)
	if err != nil {
		row.Infra = err.Error()
		return row
	}
	view, st := cvMask(msgs, o.pol)
	row.Before, row.After, row.Masked, row.Recorded = st.Before, st.After, st.Masked, ecAction(rec)
	ask := func(m []Message, act *ecAct, in *int) bool {
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		defer cancel()
		resp, err := ecAsk(ctx, cfg, ref, system, m)
		if err == nil && resp == nil {
			err = errors.New("resposta vazia")
		}
		if err != nil {
			row.Infra = err.Error()
			return false
		}
		*act = ecAction(resp.ToolCalls)
		if in != nil {
			*in = resp.Usage.In
		}
		return true
	}
	if !(ask(msgs, &row.Full, &row.InFull) && ask(msgs, &row.Full2, nil) && ask(view, &row.View, &row.InView)) || !o.compare {
		return row
	}
	for _, v := range ecVariants(o.pol) {
		vm, vs := cvMask(msgs, v.pol)
		alt := ecAlt{Name: v.name, After: vs.After, Masked: vs.Masked}
		if vs.Masked == 0 {
			// same input as the full history: the second full answer is a
			// fair draw of what this policy gets, at no extra call
			alt.View, alt.In = row.Full2, row.InFull
		} else if !ask(vm, &alt.View, &alt.In) {
			return row
		}
		row.Others = append(row.Others, alt)
	}
	return row
}

// ecSummary: agreement of the next action, at three levels of strictness.
type ecSummary struct {
	Points int `json:"points"`
	Valid  int `json:"valid"`
	Infra  int `json:"infra"`
	// same tool / same tool and target / same arguments, full vs full again
	// (the ceiling) and full vs view
	SelfName, SelfTarget, SelfArgs int
	ViewName, ViewTarget, ViewArgs int
	// against what the session actually did
	RecFull, RecView int
	InFull, InView   int // input tokens reported by the provider
}

func ecSame(a, b ecAct) (name, target, args bool) {
	name = a.Name == b.Name
	target = name && a.Target == b.Target
	args = name && a.Args == b.Args
	return
}

func ecSummarize(rows []ecRow) ecSummary {
	s := ecSummary{Points: len(rows)}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	for _, r := range rows {
		if r.Infra != "" {
			s.Infra++
			continue
		}
		s.Valid++
		n, t, a := ecSame(r.Full, r.Full2)
		s.SelfName, s.SelfTarget, s.SelfArgs = s.SelfName+b2i(n), s.SelfTarget+b2i(t), s.SelfArgs+b2i(a)
		n, t, a = ecSame(r.Full, r.View)
		s.ViewName, s.ViewTarget, s.ViewArgs = s.ViewName+b2i(n), s.ViewTarget+b2i(t), s.ViewArgs+b2i(a)
		n, _, _ = ecSame(r.Recorded, r.Full)
		s.RecFull += b2i(n)
		n, _, _ = ecSame(r.Recorded, r.View)
		s.RecView += b2i(n)
		s.InFull, s.InView = s.InFull+r.InFull, s.InView+r.InView
	}
	return s
}

func (s ecSummary) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "decisões válidas: %d de %d (%d de infra, fora da conta)\n", s.Valid, s.Points, s.Infra)
	if s.Valid == 0 {
		return b.String()
	}
	pc := func(n int) string { return fmt.Sprintf("%d/%d (%.0f%%)", n, s.Valid, 100*float64(n)/float64(s.Valid)) }
	fmt.Fprintf(&b, "%-28s %-16s %-16s %s\n", "próxima ação igual", "mesma ferramenta", "mesmo alvo", "mesmos argumentos")
	fmt.Fprintf(&b, "%-28s %-16s %-16s %s\n", "inteiro × inteiro (teto)", pc(s.SelfName), pc(s.SelfTarget), pc(s.SelfArgs))
	fmt.Fprintf(&b, "%-28s %-16s %-16s %s\n", "inteiro × com a política", pc(s.ViewName), pc(s.ViewTarget), pc(s.ViewArgs))
	fmt.Fprintf(&b, "igual à sessão gravada (ferramenta): inteiro %s · com a política %s\n", pc(s.RecFull), pc(s.RecView))
	if s.InFull > 0 && s.InView > 0 {
		fmt.Fprintf(&b, "tokens de entrada (provedor): %d → %d (−%.1f%%)\n", s.InFull, s.InView, 100*float64(s.InFull-s.InView)/float64(s.InFull))
	}
	return b.String()
}

// ecAltRows are the rows as if policy k of --compare were the one replayed.
func ecAltRows(rows []ecRow, k int) []ecRow {
	var out []ecRow
	for _, r := range rows {
		if r.Infra != "" || k >= len(r.Others) {
			r.Infra = "sem medida"
			out = append(out, r)
			continue
		}
		a := r.Others[k]
		r.View, r.After, r.Masked, r.InView = a.View, a.After, a.Masked, a.In
		out = append(out, r)
	}
	return out
}

func (s ecSummary) altText() string {
	if s.Valid == 0 {
		return "sem decisão válida\n"
	}
	pc := func(n int) string { return fmt.Sprintf("%d/%d (%.0f%%)", n, s.Valid, 100*float64(n)/float64(s.Valid)) }
	var b strings.Builder
	fmt.Fprintf(&b, "%-28s %-16s %-16s %s\n", "inteiro × com a política", pc(s.ViewName), pc(s.ViewTarget), pc(s.ViewArgs))
	fmt.Fprintf(&b, "igual à sessão gravada (ferramenta): %s", pc(s.RecView))
	if s.InFull > 0 && s.InView > 0 {
		fmt.Fprintf(&b, " · tokens de entrada: %d → %d (−%.1f%%)", s.InFull, s.InView, 100*float64(s.InFull-s.InView)/float64(s.InFull))
	}
	b.WriteString("\n")
	return b.String()
}
