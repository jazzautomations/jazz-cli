package main

import (
	"regexp"
	"sync/atomic"
)

// lazyRe is a regexp compiled on first use. Package-level regexps used to be
// built by regexp.MustCompile during the package init: ~600 programs, ~9 MB
// and ~56k allocations on every process start (jazz version included), which
// on the reference Pentium M is 100+ ms and several GC cycles before main()
// runs (docs/research/low-level-performance.md, A1/A2, item 1). A lazyRe costs
// one small object at init and compiles its program the first time a method
// is called. Compiling twice under a race is harmless (a *regexp.Regexp is
// immutable and safe for concurrent use); the first stored one wins.
//
// An invalid pattern still panics, now at first use instead of at init;
// TestLazyRxAllCompile compiles every registered pattern so a bad literal
// fails the test suite the same way it used to fail the binary's start.
type lazyRe struct {
	src string
	re  atomic.Pointer[regexp.Regexp]
}

// lazyRxAll registers every lazyRx for the compile-all test.
var lazyRxAll []*lazyRe

// lazyRx is the drop-in for regexp.MustCompile in package-level vars.
func lazyRx(src string) *lazyRe {
	l := &lazyRe{src: src}
	lazyRxAll = append(lazyRxAll, l)
	return l
}

// lazyOf wraps an already compiled regexp (for tables mixing both kinds).
func lazyOf(re *regexp.Regexp) *lazyRe {
	l := &lazyRe{src: re.String()}
	l.re.Store(re)
	return l
}

// R returns the compiled regexp, compiling it on the first call.
func (l *lazyRe) R() *regexp.Regexp {
	if re := l.re.Load(); re != nil {
		return re
	}
	re := regexp.MustCompile(l.src)
	if !l.re.CompareAndSwap(nil, re) {
		return l.re.Load()
	}
	return re
}

// String returns the source text without compiling.
func (l *lazyRe) String() string { return l.src }

func (l *lazyRe) Match(b []byte) bool                  { return l.R().Match(b) }
func (l *lazyRe) MatchString(s string) bool            { return l.R().MatchString(s) }
func (l *lazyRe) Find(b []byte) []byte                 { return l.R().Find(b) }
func (l *lazyRe) FindIndex(b []byte) []int             { return l.R().FindIndex(b) }
func (l *lazyRe) FindSubmatch(b []byte) [][]byte       { return l.R().FindSubmatch(b) }
func (l *lazyRe) FindAll(b []byte, n int) [][]byte     { return l.R().FindAll(b, n) }
func (l *lazyRe) FindAllIndex(b []byte, n int) [][]int { return l.R().FindAllIndex(b, n) }
func (l *lazyRe) FindAllSubmatch(b []byte, n int) [][][]byte {
	return l.R().FindAllSubmatch(b, n)
}
func (l *lazyRe) FindAllSubmatchIndex(b []byte, n int) [][]int {
	return l.R().FindAllSubmatchIndex(b, n)
}
func (l *lazyRe) FindSubmatchIndex(b []byte) []int     { return l.R().FindSubmatchIndex(b) }
func (l *lazyRe) FindString(s string) string           { return l.R().FindString(s) }
func (l *lazyRe) FindStringIndex(s string) []int       { return l.R().FindStringIndex(s) }
func (l *lazyRe) FindStringSubmatch(s string) []string { return l.R().FindStringSubmatch(s) }
func (l *lazyRe) FindStringSubmatchIndex(s string) []int {
	return l.R().FindStringSubmatchIndex(s)
}
func (l *lazyRe) FindAllString(s string, n int) []string { return l.R().FindAllString(s, n) }
func (l *lazyRe) FindAllStringIndex(s string, n int) [][]int {
	return l.R().FindAllStringIndex(s, n)
}
func (l *lazyRe) FindAllStringSubmatch(s string, n int) [][]string {
	return l.R().FindAllStringSubmatch(s, n)
}
func (l *lazyRe) FindAllStringSubmatchIndex(s string, n int) [][]int {
	return l.R().FindAllStringSubmatchIndex(s, n)
}
func (l *lazyRe) ReplaceAll(src, repl []byte) []byte { return l.R().ReplaceAll(src, repl) }
func (l *lazyRe) ReplaceAllFunc(src []byte, f func([]byte) []byte) []byte {
	return l.R().ReplaceAllFunc(src, f)
}
func (l *lazyRe) ReplaceAllString(s, repl string) string { return l.R().ReplaceAllString(s, repl) }
func (l *lazyRe) ReplaceAllLiteralString(s, repl string) string {
	return l.R().ReplaceAllLiteralString(s, repl)
}
func (l *lazyRe) ReplaceAllStringFunc(s string, f func(string) string) string {
	return l.R().ReplaceAllStringFunc(s, f)
}
func (l *lazyRe) Split(s string, n int) []string { return l.R().Split(s, n) }
func (l *lazyRe) SubexpNames() []string          { return l.R().SubexpNames() }
func (l *lazyRe) SubexpIndex(name string) int    { return l.R().SubexpIndex(name) }
func (l *lazyRe) NumSubexp() int                 { return l.R().NumSubexp() }
func (l *lazyRe) Expand(dst []byte, template []byte, src []byte, match []int) []byte {
	return l.R().Expand(dst, template, src, match)
}
func (l *lazyRe) ExpandString(dst []byte, template string, src string, match []int) []byte {
	return l.R().ExpandString(dst, template, src, match)
}
