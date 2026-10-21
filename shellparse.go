package main

// shellparse.go: a small shell tokenizer for the permission and danger checks
// (security review F2). It is not a full POSIX parser, but unlike the old
// regexes it balances nested parentheses and honours quotes, so
// `npm test $( (curl x) )` and `echo $( (rm -rf ~) )` expose their inner
// commands. Every simple command it finds, including the ones inside $(…),
// `…`, <(…), >(…), ( … ) subshells, heredoc bodies fed to a shell, eval and
// `sh -c`, becomes a segment that DangerReason, IsReadOnly and the permission
// rules examine. When it cannot balance the input it says so (ok == false) and
// callers treat the command as unanalysable: no allow rule matches it and auto
// mode asks.

import (
	"path/filepath"
	"regexp"
	"strings"
)

// shParsed is the result of shellParse.
type shParsed struct {
	segs []string // simple commands, outer ones first within each statement
	code []string // inline interpreter code (python -c, node -e, heredoc to python, awk programs)
	ok   bool     // false: unbalanced quotes/parentheses or nesting too deep
}

const shMaxDepth = 6

// shellParse splits cmd into simple commands (see the file comment).
func shellParse(cmd string) shParsed {
	p := &shParser{ok: true}
	p.parse(cmd, 0)
	return shParsed{segs: p.segs, code: p.code, ok: p.ok}
}

type shHeredoc struct {
	delim  string
	quoted bool // 'EOF' / "EOF" / \EOF: no expansion in the body
	strip  bool // <<- strips leading tabs
}

type shParser struct {
	segs []string
	code []string
	ok   bool
}

// parse walks s at nesting depth d, appending segments.
func (p *shParser) parse(s string, d int) {
	if d > shMaxDepth {
		p.ok = false
		return
	}
	var cur strings.Builder
	var pending []shHeredoc
	lineStart := len(p.segs) // first segment of the current line (for heredoc consumers)
	flush := func() {
		seg := strings.TrimSpace(cur.String())
		cur.Reset()
		if seg != "" {
			p.finish(seg, d)
		}
	}
	atWordStart := func() bool {
		t := cur.String()
		return t == "" || strings.HasSuffix(t, " ") || strings.HasSuffix(t, "\t")
	}
	atCmdStart := func() bool { return strings.TrimSpace(cur.String()) == "" }
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < n && s[i+1] == '\n' { // line continuation
				i++
				cur.WriteByte(' ')
				continue
			}
			cur.WriteByte(c)
			if i+1 < n {
				i++
				cur.WriteByte(s[i])
			}
		case c == '$' && i+1 < n && s[i+1] == '\'': // $'ANSI-C' quoting: \' does not close it
			end := shAnsiEnd(s, i+1)
			if end < 0 {
				p.ok = false
				cur.WriteString(s[i:])
				i = n
				continue
			}
			cur.WriteString(s[i : end+1])
			i = end
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				p.ok = false
				cur.WriteString(s[i:])
				i = n
				continue
			}
			cur.WriteString(s[i : i+j+2])
			i += j + 1
		case c == '"':
			end := p.dquote(s, i, d)
			if end < 0 {
				p.ok = false
				cur.WriteString(s[i:])
				i = n
				continue
			}
			cur.WriteString(s[i : end+1])
			i = end
		case c == '`':
			end := shBacktickEnd(s, i)
			if end < 0 {
				p.ok = false
				cur.WriteString(s[i:])
				i = n
				continue
			}
			p.parse(shUnescapeBacktick(s[i+1:end]), d+1)
			cur.WriteString(s[i : end+1])
			i = end
		case c == '$' && i+1 < n && s[i+1] == '(':
			if i+2 < n && s[i+2] == '(' { // $(( arithmetic ))
				end := shParenEnd(s, i+1)
				if end < 0 {
					p.ok = false
					i = n
					continue
				}
				body := s[i+3 : end]
				if strings.Contains(body, "$(") || strings.Contains(body, "`") {
					p.parse(body, d+1) // command substitution inside arithmetic: examine it
				}
				cur.WriteString(s[i : end+1])
				i = end
				continue
			}
			end := shParenEnd(s, i+1)
			if end < 0 {
				p.ok = false
				cur.WriteString(s[i:])
				i = n
				continue
			}
			p.parse(s[i+2:end], d+1)
			cur.WriteString(s[i : end+1])
			i = end
		case (c == '<' || c == '>') && i+1 < n && s[i+1] == '(':
			end := shParenEnd(s, i+1)
			if end < 0 {
				p.ok = false
				cur.WriteString(s[i:])
				i = n
				continue
			}
			p.parse(s[i+2:end], d+1)
			cur.WriteString(s[i : end+1])
			i = end
		case c == '(' && atCmdStart():
			// ( subshell ) or (( arithmetic ))
			end := shParenEnd(s, i)
			if end < 0 {
				p.ok = false
				i = n
				continue
			}
			body := s[i+1 : end]
			if strings.HasPrefix(body, "(") && strings.HasSuffix(body, ")") {
				if strings.Contains(body, "$(") || strings.Contains(body, "`") {
					p.parse(body[1:len(body)-1], d+1)
				}
			} else {
				p.parse(body, d+1)
			}
			i = end
		case c == '#' && atWordStart():
			for i < n && s[i] != '\n' {
				i++
			}
			i-- // let the newline be handled as a separator
		case strings.HasPrefix(s[i:], "<<<"): // here-string: data, not a heredoc
			cur.WriteString("<<<")
			i += 2
		case c == '<' && i+1 < n && s[i+1] == '<':
			// heredoc: <<WORD, <<-WORD, <<'WORD', <<"WORD", <<\WORD
			j := i + 2
			strip := false
			if j < n && s[j] == '-' {
				strip = true
				j++
			}
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			k := j
			quoted := false
			var delim strings.Builder
			for k < n && !strings.ContainsRune(" \t\n;|&<>()", rune(s[k])) {
				switch s[k] {
				case '\'', '"':
					quoted = true
					q := s[k]
					e := strings.IndexByte(s[k+1:], q)
					if e < 0 {
						p.ok = false
						k = n
						continue
					}
					delim.WriteString(s[k+1 : k+1+e])
					k += e + 2
					continue
				case '\\':
					quoted = true
					k++
					if k < n {
						delim.WriteByte(s[k])
					}
					k++
					continue
				}
				delim.WriteByte(s[k])
				k++
			}
			if delim.Len() == 0 {
				p.ok = false
			} else {
				pending = append(pending, shHeredoc{delim: delim.String(), quoted: quoted, strip: strip})
			}
			cur.WriteString(s[i:min(k, n)])
			i = k - 1
		case c == '>' || c == '<':
			cur.WriteByte(c)
			// >&2, <&0, >&-: a file-descriptor duplication, not the & operator
			if i+1 < n && s[i+1] == '>' {
				i++
				cur.WriteByte('>')
			}
			if i+1 < n && s[i+1] == '&' {
				i++
				cur.WriteByte('&')
			}
		case c == '&' && i+1 < n && s[i+1] == '>': // &> file
			cur.WriteString("&>")
			i++
		case c == ';' || c == '&' || c == '|':
			flush()
			if i+1 < n && (s[i+1] == c || c == '|' && s[i+1] == '&' || c == ';' && s[i+1] == '&') {
				i++ // && || ;; |& ;&
			}
		case c == '\n':
			flush()
			if len(pending) > 0 {
				i = p.heredocs(s, i+1, pending, lineStart, d) - 1
				pending = nil
			}
			lineStart = len(p.segs)
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	if len(pending) > 0 {
		p.ok = false // heredoc without a body (no newline): unanalysable
	}
}

// dquote returns the index of the closing double quote of the string that
// starts at s[i], parsing $(…) and `…` inside it; -1 when unterminated.
func (p *shParser) dquote(s string, i, d int) int {
	n := len(s)
	for j := i + 1; j < n; j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j
		case '`':
			end := shBacktickEnd(s, j)
			if end < 0 {
				return -1
			}
			p.parse(shUnescapeBacktick(s[j+1:end]), d+1)
			j = end
		case '$':
			if j+1 < n && s[j+1] == '(' {
				end := shParenEnd(s, j+1)
				if end < 0 {
					return -1
				}
				if !(j+2 < n && s[j+2] == '(') {
					p.parse(s[j+2:end], d+1)
				} else if b := s[j+3 : end]; strings.Contains(b, "$(") || strings.Contains(b, "`") {
					p.parse(b, d+1)
				}
				j = end
			}
		}
	}
	return -1
}

// heredocs consumes the bodies of pending heredocs starting at s[start] and
// returns the index after the last body. A body is examined as commands when
// the line that opened it runs a shell (bash <<EOF, cat <<EOF | sh), as inline
// code when it feeds an interpreter, and for $(…) substitutions when its
// delimiter is unquoted (the shell expands them even for cat).
func (p *shParser) heredocs(s string, start int, pending []shHeredoc, lineStart, d int) int {
	shell, interp := false, false
	for _, seg := range p.segs[lineStart:] {
		t := stripWrappers(shellFields(seg))
		if len(t) == 0 {
			continue
		}
		w := filepath.Base(t[0])
		if isShell(w) {
			shell = true
		}
		if reInterp.MatchString(w) {
			interp = true
		}
	}
	i := start
	for _, h := range pending {
		var body strings.Builder
		found := false
		for i < len(s) {
			j := strings.IndexByte(s[i:], '\n')
			line := ""
			if j < 0 {
				line, i = s[i:], len(s)
			} else {
				line, i = s[i:i+j], i+j+1
			}
			cmp := line
			if h.strip {
				cmp = strings.TrimLeft(cmp, "\t")
			}
			if cmp == h.delim {
				found = true
				break
			}
			body.WriteString(line + "\n")
		}
		if !found {
			p.ok = false
		}
		b := body.String()
		switch {
		case shell:
			p.parse(b, d+1)
		case interp:
			p.code = append(p.code, b)
			if !h.quoted {
				p.substOnly(b, d+1)
			}
		case !h.quoted:
			p.substOnly(b, d+1)
		}
	}
	return i
}

// substOnly examines only the command substitutions inside text that the shell
// expands (an unquoted heredoc body).
func (p *shParser) substOnly(s string, d int) {
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case s[i] == '`':
			end := shBacktickEnd(s, i)
			if end < 0 {
				p.ok = false
				return
			}
			p.parse(shUnescapeBacktick(s[i+1:end]), d)
			i = end
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
			end := shParenEnd(s, i+1)
			if end < 0 {
				p.ok = false
				return
			}
			p.parse(s[i+2:end], d)
			i = end
		}
	}
}

// reInterp: interpreters whose inline code (-c/-e) is scanned for danger.
var reInterp = regexp.MustCompile(`^(python[0-9.]*|pypy[0-9.]*|node|nodejs|deno|bun|ruby|perl|php|lua[0-9.]*|Rscript|osascript|tclsh|awk|gawk|mawk|nawk)$`)

// finish records one simple command and descends into what it runs:
// eval words, `sh -c` bodies, find -exec commands and interpreter code.
func (p *shParser) finish(seg string, d int) {
	toks := stripWrappers(shellFields(seg))
	if len(toks) == 0 {
		return
	}
	switch toks[0] {
	case "fi", "done", "esac", "}", "{", ")", "then", "do", "else":
		if len(toks) == 1 {
			return
		}
	}
	p.segs = append(p.segs, strings.Join(toks, " "))
	w := filepath.Base(toks[0])
	switch {
	case w == "eval" || w == "source" || w == ".":
		if w == "eval" && len(toks) >= 2 {
			p.parse(strings.Join(toks[1:], " "), d+1)
		}
	case isShell(w):
		for i := 1; i < len(toks)-1; i++ {
			a := toks[i]
			if a == "-c" || strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.HasSuffix(a, "c") && len(a) <= 4 {
				p.parse(toks[i+1], d+1)
				break
			}
		}
	case w == "find":
		for i := 1; i < len(toks); i++ {
			if toks[i] == "-exec" || toks[i] == "-execdir" || toks[i] == "-ok" || toks[i] == "-okdir" {
				j := i + 1
				for j < len(toks) && toks[j] != ";" && toks[j] != "+" && toks[j] != `\;` {
					j++
				}
				if j > i+1 {
					p.parse(shJoinQuoted(toks[i+1:j]), d+1)
				}
				i = j
			}
		}
	case reInterp.MatchString(w):
		if code := interpInlineCode(w, toks[1:]); code != "" {
			p.code = append(p.code, code)
		}
	}
}

// interpInlineCode returns the program text given on the command line
// (python -c CODE, node -e CODE, perl -e, ruby -e, php -r, awk 'prog').
func interpInlineCode(w string, args []string) string {
	awk := strings.HasSuffix(w, "awk")
	for i := 0; i < len(args); i++ {
		a := args[i]
		if awk {
			if a == "-f" || a == "-v" || a == "-F" {
				i++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			return a
		}
		switch a {
		case "-c", "-e", "-E", "--eval", "-p", "--print", "-r", "-x":
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		// clustered short flags ending in c/e: python -Sc 'code', perl -ne 'code'
		if len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a[len(a)-1:], "ce") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--eval=") {
			return strings.TrimPrefix(a, "--eval=")
		}
		if !strings.HasPrefix(a, "-") {
			return "" // a script file: not inline code
		}
	}
	return ""
}

// shJoinQuoted rebuilds a command line from tokens, quoting ones with spaces.
func shJoinQuoted(t []string) string {
	out := make([]string, len(t))
	for i, x := range t {
		if strings.ContainsAny(x, " \t'\"") {
			out[i] = "'" + strings.ReplaceAll(x, "'", `'\''`) + "'"
		} else {
			out[i] = x
		}
	}
	return strings.Join(out, " ")
}

// shParenEnd returns the index of the ')' that closes the '(' at s[open],
// honouring quotes, escapes, backticks and nesting; -1 when unbalanced.
func shParenEnd(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return -1
			}
			i += j + 1
		case '"':
			j := shDquoteEnd(s, i)
			if j < 0 {
				return -1
			}
			i = j
		case '`':
			j := shBacktickEnd(s, i)
			if j < 0 {
				return -1
			}
			i = j
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// shDquoteEnd is the closing quote of a double-quoted string (no parsing).
func shDquoteEnd(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j
		case '$':
			if j+1 < len(s) && s[j+1] == '(' {
				e := shParenEnd(s, j+1)
				if e < 0 {
					return -1
				}
				j = e
			}
		case '`':
			e := shBacktickEnd(s, j)
			if e < 0 {
				return -1
			}
			j = e
		}
	}
	return -1
}

// shAnsiEnd is the closing quote of a $'…' string whose opening quote is s[i].
func shAnsiEnd(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '\'':
			return j
		}
	}
	return -1
}

// shBacktickEnd is the closing backtick of the one at s[i] (escapes honoured).
func shBacktickEnd(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '`':
			return j
		}
	}
	return -1
}

// shUnescapeBacktick undoes \` \$ \\ inside an old-style substitution.
func shUnescapeBacktick(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer("\\`", "`", `\$`, `$`, `\\`, `\`)
	return r.Replace(s)
}
