package main

import (
	"strings"
)

// Indentation-flexible edit matching (ported from Hermes Agent,
// tools/fuzzy_match.py "indentation_flexible" + re-indent of the
// replacement). Weak models copy a block with spaces where the file has tabs
// (Go) or with the indentation of another nesting level. Only for one edit,
// not replace_all, a block of 2+ non-blank lines, and a unique match.

func eiLead(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }

func eiCols(ws string) int {
	n := 0
	for _, r := range ws {
		if r == '\t' {
			n += 4
		} else {
			n++
		}
	}
	return n
}

// eiUnit: the smallest positive step between indentation widths of lines.
func eiUnit(lines []string, def int) int {
	u := 0
	prev := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		c := eiCols(eiLead(l))
		if prev >= 0 {
			d := c - prev
			if d < 0 {
				d = -d
			}
			if d > 0 && (u == 0 || d < u) {
				u = d
			}
		}
		prev = c
	}
	if u == 0 {
		return def
	}
	return u
}

// eiApply tries the indentation-flexible match of old in content and returns
// the new content (ok=false: no unique match).
func eiApply(content, old, new string) (string, bool) {
	ol := strings.Split(strings.TrimRight(old, "\n"), "\n")
	nonBlank := 0
	for _, l := range ol {
		if strings.TrimSpace(l) != "" {
			nonBlank++
		}
	}
	if nonBlank < 2 {
		return "", false
	}
	cl := strings.Split(content, "\n")
	at := -1
	for i := 0; i+len(ol) <= len(cl); i++ {
		okw := true
		for j, l := range ol {
			if strings.TrimSpace(cl[i+j]) != strings.TrimSpace(l) {
				okw = false
				break
			}
		}
		if okw {
			if at >= 0 {
				return "", false // not unique
			}
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	// indentation of the first non-blank line, in the old text and in the file
	oBase, cBase := "", ""
	for j, l := range ol {
		if strings.TrimSpace(l) != "" {
			oBase, cBase = eiLead(l), eiLead(cl[at+j])
			break
		}
	}
	fileTabs := strings.Contains(cBase, "\t")
	window := cl[at : at+len(ol)]
	if !fileTabs {
		for _, l := range window {
			if strings.HasPrefix(l, "\t") {
				fileTabs = true
			}
		}
	}
	oUnit := eiUnit(ol, 4)
	fUnit := "    "
	if fileTabs {
		fUnit = "\t"
	} else if u := eiUnit(window, 4); u > 0 {
		fUnit = strings.Repeat(" ", u)
	}
	var nl []string
	for _, l := range strings.Split(strings.TrimRight(new, "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			nl = append(nl, "")
			continue
		}
		rel := eiCols(eiLead(l)) - eiCols(oBase)
		if rel < 0 {
			rel = 0
		}
		levels, rest := rel/oUnit, rel%oUnit
		nl = append(nl, cBase+strings.Repeat(fUnit, levels)+strings.Repeat(" ", rest)+strings.TrimLeft(l, " \t"))
	}
	out := append(append(append([]string{}, cl[:at]...), nl...), cl[at+len(ol):]...)
	res := strings.Join(out, "\n")
	if res == content {
		return "", false
	}
	return res, true
}

// eiHunks: the one changed region between before and after, as a hunk.
func eiHunks(before, after string) []editHunk {
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	return []editHunk{{Line: p + 1, Old: a[p : len(a)-s], New: b[p : len(b)-s]}}
}
