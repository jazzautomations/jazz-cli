package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestLazyRxAllCompile keeps the old "bad pattern fails at start" guarantee:
// every package-level lazy regexp must compile.
func TestLazyRxAllCompile(t *testing.T) {
	if len(lazyRxAll) < 100 {
		t.Fatalf("only %d lazy regexps registered; the init-time registry is broken", len(lazyRxAll))
	}
	for _, l := range lazyRxAll {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("pattern %q: %v", l.src, r)
				}
			}()
			if l.R().String() != l.src {
				t.Errorf("String mismatch for %q", l.src)
			}
		}()
	}
}

// TestInitMainBudget is the regression lock of item 1 of the low-level study:
// the package init (everything before main() runs, on every command) must
// stay under 1 MB and 5k allocations. It re-runs this test binary with
// GODEBUG=inittrace=1 and reads the line of this package.
func TestInitMainBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	out, _ := cmd.CombinedOutput()
	re := regexp.MustCompile(`(?m)^init jazzcli @[\d.]+ m?s, [\d.]+ m?s clock, (\d+) bytes, (\d+) allocs`)
	m := re.FindStringSubmatch(string(out))
	if m == nil {
		t.Skipf("no inittrace line for jazzcli in:\n%.400s", out)
	}
	bytes, _ := strconv.Atoi(m[1])
	allocs, _ := strconv.Atoi(m[2])
	t.Logf("init jazzcli: %d bytes, %d allocs", bytes, allocs)
	if bytes > 1<<20 || allocs > 5000 {
		t.Fatalf("package init too heavy: %d bytes, %d allocs (budget 1 MB / 5k); a package-level regexp.MustCompile or big literal crept back — use lazyRx", bytes, allocs)
	}
}

// noInlineMustCompileAllow lists the functions still allowed to call
// regexp.MustCompile in their body. "dynamic": the pattern is built from a
// runtime value (regexp.QuoteMeta of an argument), so it cannot be a
// package-level lazyRx; a fixed string literal in one of these fails the
// test. "todo": fixed literals outside the files of wave 1 item 7, to be
// hoisted in wave 2 (buildtarget.go belongs to the guard FP item). An entry
// that no longer matches anything fails the test too, so the list only
// shrinks. Keys are file:func (file:Type.Method for methods); a bare file
// name covers every function of that file.
var noInlineMustCompileAllow = map[string]string{
	"lazyrx.go:lazyRe.R":          "dynamic", // the lazy compile itself
	"docs.go:dcSelect":            "dynamic", // QuoteMeta(sym)
	"docs_fetch.go:dcC7Version":   "dynamic", // QuoteMeta(id), QuoteMeta(want)
	"shell.go:Shell.Run":          "dynamic", // QuoteMeta(marker)
	"methods.go:brTextOp":         "dynamic", // QuoteMeta(from) no ignore_case
	"web.go:dropBlockRes":         "dynamic", // tag names from the caller
	"buildtarget.go":              "todo",    // wave 2 (guard FP item owns the file)
	"hardening2.go:h2WholeDanger": "todo",    // wave 2
	"improve.go:imAdvice":         "todo",    // wave 2
	"offers.go:olShellFailMenu":   "todo",    // wave 2
	"tools.go:closeTruncated":     "todo",    // wave 2
	"winsafety.go:winReadOnly":    "todo",    // wave 2
}

// TestNoInlineMustCompile is the regression lock of wave 1 item 7: no
// regexp.MustCompile inside a function body of the non-test files, except
// the allowlist above. A compile inside a function runs on every call (the
// guard path paid ~6 per bash command and 2 more per pipe segment); the fix
// is a package-level lazyRx, compiled once per process.
func TestNoInlineMustCompile(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	n := 0
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			file := filepath.Base(path)
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				key := file + ":" + funcKey(fd)
				ast.Inspect(fd.Body, func(nd ast.Node) bool {
					call, ok := nd.(*ast.CallExpr)
					if !ok || !isRegexpMustCompile(call.Fun) {
						return true
					}
					n++
					pos := fset.Position(call.Pos())
					fixed := len(call.Args) == 1 && isConstString(call.Args[0])
					kind, ok := noInlineMustCompileAllow[key]
					if !ok {
						kind, ok = noInlineMustCompileAllow[file]
						if ok {
							used[file] = true
						}
					} else {
						used[key] = true
					}
					switch {
					case !ok:
						t.Errorf("%s:%d: regexp.MustCompile inside %s; hoist it to a package-level lazyRx (or add to noInlineMustCompileAllow with a reason)", file, pos.Line, key)
					case kind == "dynamic" && fixed:
						t.Errorf("%s:%d: fixed-literal regexp.MustCompile inside %s, which is allowlisted only for dynamic patterns; hoist it to a lazyRx", file, pos.Line, key)
					}
					return true
				})
			}
		}
	}
	if n == 0 {
		t.Fatal("no regexp.MustCompile call found at all; the scan is broken")
	}
	for k := range noInlineMustCompileAllow {
		if !used[k] {
			t.Errorf("noInlineMustCompileAllow entry %q no longer matches anything; drop it", k)
		}
	}
}

func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	typ := fd.Recv.List[0].Type
	if st, ok := typ.(*ast.StarExpr); ok {
		typ = st.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

func isRegexpMustCompile(fn ast.Expr) bool {
	sel, ok := fn.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "regexp" && (sel.Sel.Name == "MustCompile" || sel.Sel.Name == "MustCompilePOSIX")
}

// isConstString: a string literal or a concatenation of string literals.
func isConstString(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.ParenExpr:
		return isConstString(v.X)
	case *ast.BinaryExpr:
		return v.Op == token.ADD && isConstString(v.X) && isConstString(v.Y)
	}
	return false
}
