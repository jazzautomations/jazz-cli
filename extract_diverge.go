package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Extract that merges copies which were not the same (eval v2 task 08 on the
// d610: formatar_reais rounded differently in pix.py; the shared version
// changed Pix output in 200 of 4096 cases). On an extract request, the
// pre-images of the modules the model edited are compared per helper now
// provided by the target: when the old copies differed, the model hears it
// once, with both versions, before the answer is accepted.

// hgDefBody returns the source of top-level definition name in s ("" = not
// found): Python by indentation, brace languages up to the closing "}" at
// column 0.
func hgDefBody(s, name string) string {
	lines := strings.Split(s, "\n")
	start := -1
	for i, l := range lines {
		for _, p := range []string{"def " + name + "(", "async def " + name + "(", "func " + name + "(", "function " + name + "(", "export function " + name + "("} {
			if strings.HasPrefix(l, p) {
				start = i
			}
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return ""
	}
	brace := !strings.Contains(lines[start], "def ")
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if brace {
			if strings.HasPrefix(l, "}") {
				end = i + 1
				break
			}
			continue
		}
		if strings.TrimSpace(l) != "" && l[0] != ' ' && l[0] != '\t' && l[0] != ')' {
			end = i
			break
		}
	}
	return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n ")
}

// hgNormBody drops blank lines, comments and docstring-only lines, and trims
// indentation, so formatting differences do not count as different code.
func hgNormBody(b string) string {
	var out []string
	inDoc := false
	for i, l := range strings.Split(b, "\n") {
		t := strings.TrimSpace(l)
		if i > 0 && (strings.HasPrefix(t, `"""`) || strings.HasPrefix(t, "'''")) {
			if !(len(t) > 3 && (strings.HasSuffix(t, `"""`) || strings.HasSuffix(t, "'''"))) {
				inDoc = !inDoc
			}
			continue
		}
		if inDoc || t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		if j := strings.Index(t, "  #"); j > 0 {
			t = strings.TrimSpace(t[:j])
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

// hgDivergedCopies: for each helper the extract target now provides, the
// edited modules whose turn-start copy differed from another module's.
func (a *Agent) hgDivergedCopies() []string {
	st := a.hg
	if st == nil || st.extract == "" {
		return nil
	}
	target := a.tools.abs(st.extract)
	cur, ok := hgReadFile(target)
	if !ok {
		return nil
	}
	provided := hgTopDefs(cur)
	type copyAt struct{ rel, body string }
	byName := map[string][]copyAt{}
	for abs, pre := range st.pre {
		if pre == nil || abs == target || filepath.Ext(abs) != filepath.Ext(target) {
			continue
		}
		rel := a.tools.rel(abs)
		for n := range hgTopDefs(*pre) {
			if _, ok := provided[n]; !ok {
				continue
			}
			if b := hgDefBody(*pre, n); b != "" {
				byName[n] = append(byName[n], copyAt{rel, b})
			}
		}
	}
	var names []string
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		cs := byName[n]
		sort.Slice(cs, func(i, j int) bool { return cs[i].rel < cs[j].rel })
		groups := map[string][]copyAt{}
		var order []string
		for _, c := range cs {
			k := hgNormBody(c.body)
			if _, seen := groups[k]; !seen {
				order = append(order, k)
			}
			groups[k] = append(groups[k], c)
		}
		if len(order) < 2 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s was NOT the same in every module before your change (%d different versions):", n, len(order))
		for _, k := range order {
			var where []string
			for _, c := range groups[k] {
				where = append(where, c.rel)
			}
			fmt.Fprintf(&b, "\n--- %s (before):\n%s", strings.Join(where, ", "), acClip(groups[k][0].body, 900))
		}
		out = append(out, b.String())
	}
	return out
}
