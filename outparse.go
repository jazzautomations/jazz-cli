package main

// outparse.go: deterministic parsers for long bash output (the regex-vs-LLM
// rule, docs/research/skills-personas-scripts.md C.6/D). When a bash result is
// over opParseMin bytes and one parser explains at least opMinConfidence of its
// non-blank lines, the model gets a short structured summary, the lines the
// parser could not explain and the raw tail, instead of the 20/80 cut.
//
// Every parser follows the script output convention:
//
//	{"ok":true,"confidence":0.97,"data":{...},"unparsed":[...],"warnings":[]}
//
// confidence = explained non-blank lines / non-blank lines, capped at 0.5 when
// the format's signature line (a final summary, a diagnostic with file:line, a
// trace with frames) was never seen. ok is false without the signature.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	opParseMin      = 2 << 10 // smaller results go to the model untouched
	opMaxParseBytes = 1 << 20 // above this, parsing costs too much on a Pentium M (~5 MB/s)
	opMinConfidence = 0.8
	opSummaryLines  = 40
	opTailLines     = 30
	opUnparsedShown = 40
	opLineClip      = 300
)

type opResult struct {
	OK         bool     `json:"ok"`
	Confidence float64  `json:"confidence"`
	Data       opData   `json:"data"`
	Unparsed   []string `json:"unparsed"`
	Warnings   []string `json:"warnings"`

	explained []bool // per input line (render lists the unexplained ones above the tail)
	lines     int    // non-blank input lines
}

type opData struct {
	Kind     string      `json:"kind"`   // test | build | trace | log
	Format   string      `json:"format"` // gotest, pytest, unittest, jest, vitest, cargo, go, gcc, tsc, python, rustc, node, log
	Passed   int         `json:"passed,omitempty"`
	Failed   int         `json:"failed,omitempty"`
	Skipped  int         `json:"skipped,omitempty"`
	Errors   int         `json:"errors,omitempty"`
	Warns    int         `json:"warnings,omitempty"`
	Total    int         `json:"total,omitempty"`
	Suites   []opSuite   `json:"suites,omitempty"`
	Failures []opFailure `json:"failures,omitempty"`
	Diags    []opDiag    `json:"diags,omitempty"`
	Traces   []opTrace   `json:"traces,omitempty"`
	Log      *opLogData  `json:"log,omitempty"`
}

type opSuite struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | FAIL | skip
}

type opFailure struct {
	Test    string `json:"test"`
	Status  string `json:"status,omitempty"` // failed (default) | error
	Suite   string `json:"suite,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message,omitempty"`
}

type opDiag struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	Severity string `json:"severity"` // error | warning | note
	Code     string `json:"code,omitempty"`
	Msg      string `json:"msg"`
}

type opTrace struct {
	Lang      string    `json:"lang"`
	Exception string    `json:"exception"`
	Message   string    `json:"message,omitempty"`
	Frames    []opFrame `json:"frames,omitempty"`
}

type opFrame struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Func      string `json:"func,omitempty"`
	InProject bool   `json:"in_project"`
}

type opLogData struct {
	Entries int            `json:"entries"`
	Levels  map[string]int `json:"levels"`
	First   string         `json:"first,omitempty"`
	Last    string         `json:"last,omitempty"`
	Top     []opLogMsg     `json:"top,omitempty"`
	Samples []string       `json:"samples,omitempty"` // first error/warn entries, verbatim
}

type opLogMsg struct {
	Level string `json:"level"`
	Count int    `json:"count"`
	Msg   string `json:"msg"`
}

// opRun is the per-parse state: which lines are explained, and the result.
type opRun struct {
	lines []string
	expl  []bool
	res   opResult
	sig   bool
}

func newOpRun(lines []string, kind, format string) *opRun {
	return &opRun{lines: lines, expl: make([]bool, len(lines)), res: opResult{Data: opData{Kind: kind, Format: format}}}
}

func (p *opRun) mark(i int) {
	if i >= 0 && i < len(p.expl) {
		p.expl[i] = true
	}
}

func (p *opRun) warn(s string) { p.res.Warnings = append(p.res.Warnings, s) }

// finish computes confidence and unparsed[] from the explained lines.
func (p *opRun) finish() *opResult {
	r := &p.res
	n, ex := 0, 0
	for i, l := range p.lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n++
		if p.expl[i] {
			ex++
			continue
		}
		if len(r.Unparsed) < 400 {
			r.Unparsed = append(r.Unparsed, l)
		}
	}
	r.lines, r.explained = n, p.expl
	if n > 0 {
		r.Confidence = float64(ex) / float64(n)
	}
	r.OK = p.sig
	if !p.sig && r.Confidence > 0.5 {
		r.Confidence = 0.5
	}
	r.Confidence = float64(int(r.Confidence*1000+0.5)) / 1000
	if r.Unparsed == nil {
		r.Unparsed = []string{}
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	return r
}

// opRegexes are compiled on first use, not at startup (startup is slow on the Dell).
type opRegexes struct {
	ansi, caret *regexp.Regexp
	// go test / go build
	goRun, goResult, goPkgOK, goPkgFail, goNoTests, goMsg, goDiag, goCover *regexp.Regexp
	// go panic
	goPanic, goRoutine, goFunc, goFile, goCreated, goSignal *regexp.Regexp
	// pytest
	pySection, pyTestHdr, pySubSec, pyFinal, pyNoTests, pyCount, pyProgress, pyProgressQ, pyVerbose, pyHeader, pyE, pyLoc, pySumLine *regexp.Regexp
	// unittest
	utSep, utSep2, utHdr, utVerbose, utProgress, utRan, utOK, utFailed, utCount *regexp.Regexp
	// python traceback
	pyTb, pyFrame, pyExc, pyChain, pySynFile *regexp.Regexp
	// jest / vitest
	jsSuite, jsTest, jsFailHdr, jsSummary, jsCount, jsAt, jsAtFile, jsConsole, vtRun, vtFile, vtDescribe, vtFailHdr, vtSep, vtLoc, vtSummary, vtTotal *regexp.Regexp
	// cargo / rustc
	rsCompile, rsRunning, rsTest, rsResult, rsBlock, rsPanicNew, rsPanicOld, rsDiag, rsArrow, rsFrame, rsNote, rsTail *regexp.Regexp
	// gcc / clang / ld / make
	cDiag, cDiagNoCol, cInFunc, cIncluded, cFrame, cGenerated, cMisc, ldUndef, cCmd *regexp.Regexp
	// tsc
	tsPlain, tsPretty, tsRelated, tsFound, tsFiles *regexp.Regexp
	// node
	nodeExc, nodeLoc, nodeVer, nodeProp *regexp.Regexp
	// logs
	logISO, logSyslog, logGlog, logTime, logAccess, logLevel, logLevelOnly, logLogfmtLevel, logLogfmtMsg, logLogfmtTime, logNorm *regexp.Regexp
}

var (
	opRxOnce sync.Once
	opRxVal  *opRegexes
)

func opRx() *opRegexes {
	opRxOnce.Do(func() {
		c := regexp.MustCompile
		opRxVal = &opRegexes{
			ansi:  c(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-B]`),
			caret: c(`^\s*[\^~][\^~\s]*$`),

			goRun:     c(`^\s*=== (RUN|PAUSE|CONT|NAME)\s+(\S+)`),
			goResult:  c(`^(\s*)--- (PASS|FAIL|SKIP): (\S+)(?: \(([\d.]+)s\))?`),
			goPkgOK:   c(`^ok\s+(\S+)\s+(?:[\d.]+s|\(cached\))`),
			goPkgFail: c(`^FAIL\s+(\S+)\s+(?:[\d.]+s|\[(?:build|setup) failed\])`),
			goNoTests: c(`^\?\s+(\S+)\s+\[no test files\]`),
			goMsg:     c(`^(\s+)(\S+\.go):(\d+): (.*)$`),
			goDiag:    c(`^(?:vet: )?(\S+\.go):(\d+):(\d+): (.*)$`),
			goCover:   c(`^\s*(coverage: |testing: warning: no tests to run)`),

			goPanic:   c(`^(panic|fatal error): (.*?)( \[recovered\])?$`),
			goRoutine: c(`^goroutine \d+ \[[^\]]+\]:$`),
			goFunc:    c(`^(\S+)\((.*)\)$`),
			goFile:    c(`^\t(\S+\.(?:go|s)):(\d+)(?: \+0x[0-9a-f]+)?$`),
			goCreated: c(`^created by (\S+)`),
			goSignal:  c(`^\[signal |^\s+panic: |^exit status \d+$|^\.\.\.additional frames elided\.\.\.$|^\.\.\. \d+ (frames|goroutines)`),

			pySection:   c(`^={3,} (.+?) ={3,}$`),
			pyTestHdr:   c(`^_{3,} (.+?) _{3,}$`),
			pySubSec:    c(`^-{3,} (.+?) -{3,}$`),
			pyFinal:     c(`^=*\s*((?:\d+ (?:passed|failed|skipped|errors?|deselected|xfailed|xpassed|warnings?|rerun)(?:, )?)+) in ([\d.]+)s(?: \([^)]*\))?\s*=*$`),
			pyNoTests:   c(`^=*\s*no tests ran in [\d.]+s\s*=*$`),
			pyCount:     c(`(\d+) (passed|failed|skipped|errors?|deselected|xfailed|xpassed|warnings?|rerun)`),
			pyProgress:  c(`^(\S+\.py) [.FEsxXpR]+\s*(?:\[\s*\d+%\])?$`),
			pyProgressQ: c(`^[.FEsxXpR]+\s*(?:\[\s*\d+%\])?$`),
			pyVerbose:   c(`^(\S+::\S.*?) (PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)(?: .*?)?\s*(?:\[\s*\d+%\])?$`),
			pyHeader:    c(`^(platform \S+ -- Python|rootdir: |configfile: |inifile: |plugins: |cachedir: |testpaths: |collecting |collected \d+ items?|cachedir|Test session starts|-- Docs: https://docs\.pytest\.org)`),
			pyE:         c(`^E\s+(.*)$`),
			pyLoc:       c(`^(\S+?\.py):(\d+): (\S.*)$`),
			pySumLine:   c(`^(FAILED|ERROR|XFAIL|XPASS|SKIPPED)\s+(\S+)(?: - (.*))?$`),

			utSep:      c(`^={20,}$`),
			utSep2:     c(`^-{20,}$`),
			utHdr:      c(`^(FAIL|ERROR|UNEXPECTED SUCCESS): (\S+) \(([^)]+)\)`),
			utVerbose:  c(`^.+ \.\.\. (ok|FAIL|ERROR|skipped.*|expected failure|unexpected success)$`),
			utProgress: c(`^[.FEsxu]+$`),
			utRan:      c(`^Ran (\d+) tests? in ([\d.]+)s$`),
			utOK:       c(`^OK(?: \((.*)\))?$`),
			utFailed:   c(`^FAILED \((.*)\)$`),
			utCount:    c(`(failures|errors|skipped|expected failures|unexpected successes)=(\d+)`),

			pyTb:      c(`^\s*Traceback \(most recent call last\):$`),
			pyFrame:   c(`^\s+File "(.+?)", line (\d+)(?:, in (.+))?$`),
			pyExc:     c(`^([A-Za-z_][\w.]*)(?:: (.*))?$`),
			pyChain:   c(`^(During handling of the above exception, another exception occurred:|The above exception was the direct cause of the following exception:)$`),
			pySynFile: c(`^\s*File "(.+?)", line (\d+)$`),

			jsSuite:    c(`^(PASS|FAIL)\s+(\S+)(?:\s+\([\d.]+ ?m?s\))?$`),
			jsTest:     c(`^\s+([✓✔√✕✗×○✎↓])\s+(.*?)(?:\s+\(?\d+(?:\.\d+)? ?m?s\)?)?$`),
			jsFailHdr:  c(`^\s+● (.+)$`),
			jsSummary:  c(`^(Test Suites:|Tests:|Snapshots:|Time:|Ran all test suites|Watch Usage)`),
			jsCount:    c(`(\d+) (failed|passed|skipped|todo|total|pending)`),
			jsAt:       c(`^\s+at .+`),
			jsAtFile:   c(`\(?([^\s()]+?):(\d+):(\d+)\)?$`),
			jsConsole:  c(`^\s+console\.(log|info|warn|error|debug|trace)$`),
			vtRun:      c(`^\s*RUN\s+v\d`),
			vtFile:     c(`^\s*([✓❯×↓])\s+(\S+\.\w+)\s+\((\d+) tests?[^)]*\)(?:\s+\d+(?:\.\d+)?m?s)?$`),
			vtDescribe: c(`^\s+❯\s+(.+?) \(\d+\)$`),
			vtFailHdr:  c(`^\s*FAIL\s+(\S+) > (.+)$`),
			vtSep:      c(`^⎯+.*⎯$`),
			vtLoc:      c(`^\s*❯ (\S+?):(\d+):(\d+)$`),
			vtSummary:  c(`^\s*(Test Files|Tests|Start at|Duration|Snapshots|Type Errors|Errors)\s{2,}(.*)$`),
			vtTotal:    c(`\((\d+)\)\s*$`),

			rsCompile:  c(`^\s+(Compiling|Finished|Running|Doc-tests|Downloaded|Downloading|Blocking|Fresh|Checking|Updating|Locking|Adding|Executable)\b`),
			rsRunning:  c(`^running \d+ tests?$`),
			rsTest:     c(`^test (\S+)(?: - should panic)? \.\.\. (ok|FAILED|ignored.*|bench.*)$`),
			rsResult:   c(`^test result: (ok|FAILED)\. (\d+) passed; (\d+) failed; (\d+) ignored; (\d+) measured; (\d+) filtered out`),
			rsBlock:    c(`^---- (\S+) stdout ----$`),
			rsPanicNew: c(`^thread '(.+?)' panicked at (\S+?):(\d+):(\d+):$`),
			rsPanicOld: c(`^thread '(.+?)' panicked at '(.*)', (\S+?):(\d+):(\d+)$`),
			rsDiag:     c(`^(error|warning)(?:\[(E\d+)\])?: (.*)$`),
			rsArrow:    c(`^\s*--> (\S+?):(\d+):(\d+)$`),
			rsFrame:    c(`^\s*\d*\s*\|.*$|^\s*\.\.\.$`),
			rsNote:     c(`^\s*= (note|help|warning): |^(note|help): `),
			rsTail:     c(`^(error|warning): (aborting due to|could not compile|test failed, to rerun|\d+ targets? failed|.* generated \d+ warnings?)|^For more information about (this|an) error|^Some errors have detailed explanations|^\s+` + "`" + `-p .*` + "`" + `$|^failures:$|^\s+\S+::\S+$|^\s+tests::\S+$|^stack backtrace:$|^\s+\d+: \S|^\s+at \S+:\d+(:\d+)?$`),

			cDiag:      c(`^([^\s:][^:]*):(\d+):(\d+): (fatal error|error|warning|note|remark): (.*)$`),
			cDiagNoCol: c(`^([^\s:][^:]*):(\d+): (fatal error|error|warning|note): (.*)$`),
			cInFunc:    c(`^([^\s:][^:]*): (In (function|member function|constructor|destructor|instantiation of|static member function|lambda function|substitution of)|At (top level|global scope)).*:$`),
			cIncluded:  c(`^(In file included from|\s+from|In instantiation of|\s+required from) .*[:,]$`),
			cFrame:     c(`^\s*\d*\s*\|.*$`),
			cGenerated: c(`^\d+ (errors?|warnings?)( and \d+ (errors?|warnings?))? generated\.$|^compilation terminated\.$|^cc1\w*: (all )?warnings being treated as errors$|^collect2: error: ld returned \d+ exit status$|^clang(\+\+)?: error: linker command failed|^make(\[\d+\])?: (\*\*\*|Entering directory|Leaving directory|Nothing to be done)|^ninja: (build stopped|Entering directory)|^FAILED: \S+`),
			cMisc:      c(`^(?:/\S*/)?ld(?:\.\w+)?: (.*)$|^\S+\.o: in function .*:$|^Undefined symbols for architecture \S+:$|^\s+"\S+", referenced from:$|^\s+\S+ in \S+\.o$`),
			cCmd:       c(`^(?:\S*/)?(?:gcc|g\+\+|cc|c\+\+|clang|clang\+\+|ld|ar|ranlib|nvcc|libtool)(?:-\d+)?\s+-`),
			ldUndef:    c(`^(\S+?):(?:\([^)]*\)|(\d+)): (undefined reference to .*)$`),

			tsPlain:   c(`^(\S+?)\((\d+),(\d+)\): (error|warning) (TS\d+): (.*)$`),
			tsPretty:  c(`^(\S+?):(\d+):(\d+) - (error|warning) (TS\d+): (.*)$`),
			tsRelated: c(`^\s+(\S+?):(\d+):(\d+)( - .*)?$`),
			tsFound:   c(`^Found (\d+) errors?(?: in .*)?\.?$|^Errors\s+Files$|^\s+\d+\s+\S+:\d+$`),
			tsFiles:   c(`^\s*\d+\s+.*$`),

			nodeExc:  c(`^(?:Uncaught )?([A-Z]\w*(?:Error|Exception)|Error|AssertionError \[\w+\])(?: \[(\w+)\])?: (.*)$`),
			nodeLoc:  c(`^(\S+\.(?:m?js|cjs|ts|tsx|jsx|mts|cts)):(\d+)$`),
			nodeVer:  c(`^Node\.js v[\d.]+$|^\(Use ` + "`" + `node --trace-\S+ \.\.\.` + "`" + `.*\)$|^Emitted '\w+' event`),
			nodeProp: c(`^\{$|^\}$|^\s+\w+: .*,?$|^\s+\[\w+\]: .*,?$`),

			logISO:         c(`^\[?(\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2}| ?UTC)?)\]?\s*(.*)$`),
			logSyslog:      c(`^([A-Z][a-z]{2} [ \d]\d \d{2}:\d{2}:\d{2}) (\S+) ([^:\[]+)(?:\[\d+\])?: (.*)$`),
			logGlog:        c(`^([IWEF])(\d{4} \d{2}:\d{2}:\d{2}\.\d+)\s+\d+ \S+:\d+\] (.*)$`),
			logTime:        c(`^\[?(\d{2}:\d{2}:\d{2}(?:[.,]\d+)?)\]?\s+(.*)$`),
			logAccess:      c(`^\S+ \S+ \S+ \[(\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4})\] "(.*?)" (\d{3}) \S+`),
			logLevel:       c(`(?i)(?:^|[\s\[|(<:])(trace|debug|dbg|info|inf|notice|warn|warning|wrn|error|err|eror|fatal|ftl|critical|crit|panic|severe)(?:$|[\s\]|)>:])`),
			logLevelOnly:   c(`^\[?(TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|FATAL|CRITICAL)\]?[:\s]\s*(.*)$`),
			logLogfmtLevel: c(`(?:^|\s)(?:level|lvl|severity)=("?)(\w+)`),
			logLogfmtMsg:   c(`(?:^|\s)(?:msg|message)=(?:"((?:[^"\\]|\\.)*)"|(\S+))`),
			logLogfmtTime:  c(`(?:^|\s)(?:time|ts|t)="?([^"\s]+)`),
			logNorm:        c(`0x[0-9a-fA-F]+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|\d+(?:\.\d+)*`),
		}
	})
	return opRxVal
}

// opClean strips ANSI escapes and keeps only the last carriage-return segment
// of each line (progress bars), as a terminal would show it.
func opClean(s string) []string {
	rx := opRx()
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = rx.ansi.ReplaceAllString(s, "")
	}
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.IndexByte(l, '\r') >= 0 {
			l = strings.TrimRight(l, "\r")
			if j := strings.LastIndexByte(l, '\r'); j >= 0 {
				l = l[j+1:]
			}
			lines[i] = l
		}
	}
	return lines
}

func opAtoi(s string) int { n, _ := strconv.Atoi(s); return n }

func opIndent(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

// opInProject reports whether a frame's file looks like the project's own code
// rather than the standard library or a dependency.
func opInProject(f string) bool {
	for _, s := range []string{"site-packages", "dist-packages", "/lib/python", "<frozen", "node:", "node_modules", "/usr/lib/go", "/usr/local/go/", "/go/pkg/mod/", "/src/runtime/", "/rustc/", "/.cargo/registry/", "<anonymous>", "internal/"} {
		if strings.Contains(f, s) {
			return false
		}
	}
	return f != ""
}

// --- go test ---------------------------------------------------------------

func opGoTest(lines []string) *opResult {
	p := newOpRun(lines, "test", "gotest")
	rx := opRx()
	cur := ""
	msgs := map[string][]string{}
	locs := map[string][2]string{}
	var failed []string
	var failIdx = map[string]bool{}
	pending := 0 // failures not yet assigned to a package
	lastMsgIndent := -1
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			lastMsgIndent = -1
			continue
		}
		if m := rx.goRun.FindStringSubmatch(l); m != nil {
			p.mark(i)
			cur = m[2]
			lastMsgIndent = -1
			continue
		}
		if m := rx.goResult.FindStringSubmatch(l); m != nil {
			p.mark(i)
			p.sig = true
			lastMsgIndent = -1
			name := m[3]
			switch m[2] {
			case "PASS":
				p.res.Data.Passed++
			case "SKIP":
				p.res.Data.Skipped++
			case "FAIL":
				p.res.Data.Failed++
				if !failIdx[name] {
					failIdx[name] = true
					failed = append(failed, name)
					pending++
				}
			}
			cur = name
			continue
		}
		if m := rx.goMsg.FindStringSubmatch(l); m != nil {
			p.mark(i)
			if len(msgs[cur]) < 3 {
				msgs[cur] = append(msgs[cur], m[4])
			}
			if _, ok := locs[cur]; !ok {
				locs[cur] = [2]string{m[2], m[3]}
			}
			lastMsgIndent = len(m[1])
			continue
		}
		if lastMsgIndent >= 0 && opIndent(l) > lastMsgIndent {
			p.mark(i) // continuation of a multi-line t.Error message
			if ms := msgs[cur]; len(ms) > 0 && len(ms[len(ms)-1]) < 300 {
				ms[len(ms)-1] += " / " + strings.TrimSpace(l)
			}
			continue
		}
		lastMsgIndent = -1
		t := strings.TrimSpace(l)
		switch {
		case t == "PASS" || t == "FAIL":
			p.mark(i)
		case rx.goPkgOK.MatchString(l):
			p.mark(i)
			p.sig = true
			p.res.Data.Suites = append(p.res.Data.Suites, opSuite{Name: strings.Fields(l)[1], Status: "ok"})
		case rx.goPkgFail.MatchString(l):
			p.mark(i)
			p.sig = true
			pkg := strings.Fields(l)[1]
			p.res.Data.Suites = append(p.res.Data.Suites, opSuite{Name: pkg, Status: "FAIL"})
			if strings.Contains(l, "[build failed]") || strings.Contains(l, "[setup failed]") {
				p.res.Data.Errors++
			}
			for k := len(failed) - pending; k < len(failed); k++ {
				p.res.Data.Failures = append(p.res.Data.Failures, opFailure{Test: failed[k], Suite: pkg})
			}
			pending = 0
		case rx.goNoTests.MatchString(l):
			p.mark(i)
			p.sig = true
		case rx.goCover.MatchString(l), strings.HasPrefix(l, "# "), strings.HasPrefix(l, "go: downloading "), strings.HasPrefix(l, "go: finding "):
			p.mark(i)
		case rx.goDiag.MatchString(l):
			m := rx.goDiag.FindStringSubmatch(l)
			p.mark(i)
			p.res.Data.Diags = append(p.res.Data.Diags, opDiag{File: m[1], Line: opAtoi(m[2]), Col: opAtoi(m[3]), Severity: "error", Msg: m[4]})
		case rx.goPanic.MatchString(l):
			i = opGoPanic(p, i) - 1
		}
	}
	for k := len(failed) - pending; k < len(failed); k++ {
		p.res.Data.Failures = append(p.res.Data.Failures, opFailure{Test: failed[k]})
	}
	for k := range p.res.Data.Failures {
		f := &p.res.Data.Failures[k]
		f.Message = strings.Join(msgs[f.Test], " | ")
		if lc, ok := locs[f.Test]; ok {
			f.File, f.Line = lc[0], opAtoi(lc[1])
		}
	}
	// A parent test that failed only because a subtest did is noise.
	var keep []opFailure
	for _, f := range p.res.Data.Failures {
		child := false
		for _, g := range p.res.Data.Failures {
			if strings.HasPrefix(g.Test, f.Test+"/") {
				child = true
				break
			}
		}
		if child && f.Message == "" {
			continue
		}
		keep = append(keep, f)
	}
	// A test that panicked has no t.Error line: its location is the test's
	// own frame in the panic trace (or the first project frame).
	for k := range keep {
		f := &keep[k]
		if f.File != "" || len(p.res.Data.Traces) == 0 {
			continue
		}
		top := f.Test
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		tr := p.res.Data.Traces[0]
		var pick *opFrame
		for x := range tr.Frames {
			fr := &tr.Frames[x]
			if strings.HasSuffix(fr.Func, "."+top) {
				pick = fr
				break
			}
			if pick == nil && fr.InProject && len(keep) == 1 {
				pick = fr
			}
		}
		if pick != nil {
			f.File, f.Line = pick.File, pick.Line
		}
		if f.Message == "" {
			f.Message = tr.Exception + ": " + tr.Message
		}
	}
	if dropped := len(p.res.Data.Failures) - len(keep); dropped > 0 && p.res.Data.Failed >= dropped {
		p.res.Data.Failed -= dropped // count leaf failures, as listed
	}
	p.res.Data.Failures = keep
	return p.finish()
}

// opGoPanic consumes a Go panic / fatal error block starting at i and returns
// the index of the first line after it. Only the first goroutine's frames are kept.
func opGoPanic(p *opRun, i int) int {
	rx := opRx()
	lines := p.lines
	m := rx.goPanic.FindStringSubmatch(lines[i])
	tr := opTrace{Lang: "go", Exception: m[1], Message: m[2]}
	p.mark(i)
	routines, fn := 0, ""
	j := i + 1
loop:
	for ; j < len(lines); j++ {
		l := lines[j]
		switch {
		case strings.TrimSpace(l) == "":
			continue
		case rx.goRoutine.MatchString(l):
			routines++
		case rx.goSignal.MatchString(l):
			if strings.HasPrefix(l, "exit status") {
				p.mark(j)
				j++
				break loop
			}
		case rx.goFile.MatchString(l):
			fm := rx.goFile.FindStringSubmatch(l)
			if routines <= 1 && len(tr.Frames) < 30 {
				tr.Frames = append(tr.Frames, opFrame{File: fm[1], Line: opAtoi(fm[2]), Func: fn, InProject: opInProject(fm[1])})
			}
		case rx.goCreated.MatchString(l):
			fn = rx.goCreated.FindStringSubmatch(l)[1]
		case rx.goFunc.MatchString(l) && routines > 0:
			fn = rx.goFunc.FindStringSubmatch(l)[1]
		default:
			break loop
		}
		p.mark(j)
	}
	if len(tr.Frames) > 0 || routines > 0 {
		p.sig = true
	}
	p.res.Data.Traces = append(p.res.Data.Traces, tr)
	return j
}

// --- pytest ----------------------------------------------------------------

func opPytest(lines []string) *opResult {
	p := newOpRun(lines, "test", "pytest")
	rx := opRx()
	sec := ""
	type block struct {
		name, file, exc string
		line            int
		msgs            []string
	}
	var blocks []*block
	var cur *block
	var fails []opFailure
	inSumItem := false
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := rx.pyFinal.FindStringSubmatch(l); m != nil {
			p.mark(i)
			p.sig = true
			for _, c := range rx.pyCount.FindAllStringSubmatch(m[1], -1) {
				n := opAtoi(c[1])
				switch c[2] {
				case "passed", "xpassed":
					p.res.Data.Passed += n
				case "failed":
					p.res.Data.Failed += n
				case "skipped", "deselected", "xfailed":
					p.res.Data.Skipped += n
				case "error", "errors":
					p.res.Data.Errors += n
				case "warning", "warnings":
					p.res.Data.Warns += n
				}
			}
			sec = "done"
			continue
		}
		if rx.pyNoTests.MatchString(l) {
			p.mark(i)
			p.sig = true
			sec = "done"
			continue
		}
		if m := rx.pySection.FindStringSubmatch(l); m != nil {
			p.mark(i)
			sec = strings.ToLower(m[1])
			cur, inSumItem = nil, false
			continue
		}
		switch sec {
		case "failures", "errors", "xfailures", "xpasses", "passes":
			p.mark(i) // pytest owns everything inside these sections
			if m := rx.pyTestHdr.FindStringSubmatch(l); m != nil {
				cur = &block{name: m[1]}
				blocks = append(blocks, cur)
				continue
			}
			if cur == nil || rx.pySubSec.MatchString(l) {
				continue
			}
			if m := rx.pyE.FindStringSubmatch(l); m != nil {
				if t := strings.TrimSpace(m[1]); t != "" && len(cur.msgs) < 3 {
					cur.msgs = append(cur.msgs, t)
				}
			} else if m := rx.pyLoc.FindStringSubmatch(l); m != nil {
				cur.file, cur.line, cur.exc = m[1], opAtoi(m[2]), m[3]
			}
			continue
		case "short test summary info":
			p.mark(i)
			if m := rx.pySumLine.FindStringSubmatch(l); m != nil {
				inSumItem = m[1] == "FAILED" || m[1] == "ERROR"
				if inSumItem {
					st := "failed"
					if m[1] == "ERROR" {
						st = "error"
					}
					fails = append(fails, opFailure{Test: m[2], Status: st, Message: m[3]})
				}
			} else if inSumItem && len(fails) > 0 {
				f := &fails[len(fails)-1]
				if t := strings.TrimSpace(l); t != "" && len(f.Message) < 300 {
					f.Message += " / " + t
				}
			}
			continue
		case "test session starts", "warnings summary", "rerun test summary info":
			if sec != "test session starts" {
				p.mark(i)
				continue
			}
		}
		switch {
		case rx.pyHeader.MatchString(l), rx.pyProgress.MatchString(l), rx.pyProgressQ.MatchString(l):
			p.mark(i)
		case rx.pyVerbose.MatchString(l):
			p.mark(i)
		}
	}
	// Join the summary lines with the detailed blocks (file:line, E lines).
	key := func(s string) string {
		for _, pre := range []string{"ERROR at setup of ", "ERROR at teardown of ", "ERROR collecting "} {
			s = strings.TrimPrefix(s, pre)
		}
		if i := strings.Index(s, "::"); i >= 0 {
			s = s[i+2:]
		}
		return strings.ReplaceAll(s, "::", ".")
	}
	if len(fails) == 0 {
		for _, b := range blocks {
			st := "failed"
			if strings.HasPrefix(b.name, "ERROR ") {
				st = "error"
			}
			fails = append(fails, opFailure{Test: b.name, Status: st})
		}
	}
	for k := range fails {
		f := &fails[k]
		for _, b := range blocks {
			if key(b.name) == key(f.Test) || b.name == f.Test {
				f.File, f.Line = b.file, b.line
				if len(b.msgs) > 0 && (f.Message == "" || len(b.msgs) > 1) {
					f.Message = strings.Join(b.msgs, " / ")
				} else if f.Message == "" {
					f.Message = b.exc
				}
				break
			}
		}
		if f.Status == "failed" {
			f.Status = ""
		}
	}
	p.res.Data.Failures = fails
	return p.finish()
}

// --- python unittest -------------------------------------------------------

func opUnittest(lines []string) *opResult {
	p := newOpRun(lines, "test", "unittest")
	rx := opRx()
	inBlock, seenFrame := false, false
	var cur *opFailure
	ran := 0
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := rx.utRan.FindStringSubmatch(l); m != nil {
			p.mark(i)
			p.sig = true
			ran = opAtoi(m[1])
			inBlock, cur = false, nil
			continue
		}
		if rx.utSep.MatchString(l) {
			p.mark(i)
			inBlock, cur, seenFrame = true, nil, false
			continue
		}
		if inBlock {
			p.mark(i)
			if m := rx.utHdr.FindStringSubmatch(l); m != nil && cur == nil {
				st := ""
				if m[1] == "ERROR" {
					st = "error"
				}
				name := m[3]
				if !strings.HasSuffix(name, "."+m[2]) {
					name += "." + m[2]
				}
				p.res.Data.Failures = append(p.res.Data.Failures, opFailure{Test: name, Status: st})
				cur = &p.res.Data.Failures[len(p.res.Data.Failures)-1]
				continue
			}
			if cur == nil {
				continue
			}
			if m := rx.pyFrame.FindStringSubmatch(l); m != nil {
				cur.File, cur.Line = m[1], opAtoi(m[2])
				seenFrame = true
				continue
			}
			if seenFrame && opIndent(l) == 0 && !rx.pyTb.MatchString(l) {
				if m := rx.pyExc.FindStringSubmatch(l); m != nil {
					cur.Message = l
				}
			}
			continue
		}
		switch {
		case rx.utOK.MatchString(l):
			p.mark(i)
			for _, c := range rx.utCount.FindAllStringSubmatch(l, -1) {
				if c[1] == "skipped" || c[1] == "expected failures" {
					p.res.Data.Skipped += opAtoi(c[2])
				}
			}
		case rx.utFailed.MatchString(l):
			p.mark(i)
			for _, c := range rx.utCount.FindAllStringSubmatch(l, -1) {
				n := opAtoi(c[2])
				switch c[1] {
				case "failures", "unexpected successes":
					p.res.Data.Failed += n
				case "errors":
					p.res.Data.Errors += n
				case "skipped", "expected failures":
					p.res.Data.Skipped += n
				}
			}
		case rx.utVerbose.MatchString(l), rx.utProgress.MatchString(l), rx.utSep2.MatchString(l):
			p.mark(i)
		}
	}
	if ran > 0 {
		p.res.Data.Total = ran
		p.res.Data.Passed = ran - p.res.Data.Failed - p.res.Data.Errors - p.res.Data.Skipped
		if p.res.Data.Passed < 0 {
			p.res.Data.Passed = 0
		}
	}
	return p.finish()
}

// --- jest / vitest ---------------------------------------------------------

func opJest(lines []string) *opResult {
	p := newOpRun(lines, "test", "jest")
	rx := opRx()
	inSuite, inFail := false, false
	var cur *opFailure
	gotMsg := false
	addFail := func(name, suite string) {
		p.res.Data.Failures = append(p.res.Data.Failures, opFailure{Test: name, Suite: suite})
		cur = &p.res.Data.Failures[len(p.res.Data.Failures)-1]
		inFail, gotMsg = true, false
	}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if m := rx.jsSuite.FindStringSubmatch(l); m != nil {
			p.mark(i)
			inSuite, inFail, cur = true, false, nil
			st := "ok"
			if m[1] == "FAIL" {
				st = "FAIL"
			}
			p.res.Data.Suites = append(p.res.Data.Suites, opSuite{Name: m[2], Status: st})
			continue
		}
		if m := rx.vtFailHdr.FindStringSubmatch(l); m != nil {
			p.mark(i)
			p.res.Data.Format = "vitest"
			addFail(m[2], m[1])
			continue
		}
		if m := rx.jsFailHdr.FindStringSubmatch(l); m != nil {
			p.mark(i)
			addFail(m[1], "")
			continue
		}
		if rx.vtSep.MatchString(l) || rx.vtRun.MatchString(l) {
			p.mark(i)
			inFail, cur = false, nil
			continue
		}
		if m := rx.vtSummary.FindStringSubmatch(l); m != nil && !inFail {
			p.mark(i)
			p.res.Data.Format = "vitest"
			if m[1] == "Tests" {
				p.sig = true
				opJestCounts(&p.res.Data, m[2])
				if tm := rx.vtTotal.FindStringSubmatch(m[2]); tm != nil {
					p.res.Data.Total = opAtoi(tm[1])
				}
			}
			continue
		}
		if rx.jsSummary.MatchString(l) {
			p.mark(i)
			inFail, cur = false, nil
			if strings.HasPrefix(l, "Tests:") {
				p.sig = true
				opJestCounts(&p.res.Data, l)
			}
			continue
		}
		if inFail && cur != nil {
			p.mark(i) // failure body: message, diff, code frame, stack
			if m := rx.vtLoc.FindStringSubmatch(l); m != nil {
				if cur.File == "" {
					cur.File, cur.Line = m[1], opAtoi(m[2])
				}
				continue
			}
			if rx.jsAt.MatchString(l) {
				if m := rx.jsAtFile.FindStringSubmatch(t); m != nil && cur.File == "" && opInProject(m[1]) {
					cur.File, cur.Line = m[1], opAtoi(m[2])
				}
				continue
			}
			if !gotMsg {
				cur.Message, gotMsg = t, true
			} else if (strings.HasPrefix(t, "Expected:") || strings.HasPrefix(t, "Received:")) && len(cur.Message) < 300 {
				cur.Message += " / " + t
			}
			continue
		}
		switch {
		case rx.vtFile.MatchString(l):
			p.mark(i)
			p.res.Data.Format = "vitest"
			m := rx.vtFile.FindStringSubmatch(l)
			st := "ok"
			if m[1] != "✓" {
				st = "FAIL"
			}
			if m[1] == "↓" {
				st = "skip"
			}
			p.res.Data.Suites = append(p.res.Data.Suites, opSuite{Name: m[2], Status: st})
			inSuite = true
		case rx.jsTest.MatchString(l), rx.vtDescribe.MatchString(l), rx.jsConsole.MatchString(l), rx.jsAt.MatchString(l):
			p.mark(i)
		case inSuite && opIndent(l) >= 2 && len(t) < 160:
			p.mark(i) // describe() header in --verbose output
		}
	}
	return p.finish()
}

func opJestCounts(d *opData, s string) {
	for _, c := range opRx().jsCount.FindAllStringSubmatch(s, -1) {
		n := opAtoi(c[1])
		switch c[2] {
		case "passed":
			d.Passed = n
		case "failed":
			d.Failed = n
		case "skipped", "todo", "pending":
			d.Skipped += n
		case "total":
			d.Total = n
		}
	}
}

// --- cargo test ------------------------------------------------------------

func opCargo(lines []string) *opResult {
	p := newOpRun(lines, "test", "cargo")
	rx := opRx()
	var cur *opFailure
	idx := map[string]int{}
	inBlock, wantMsg := false, false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if m := rx.rsResult.FindStringSubmatch(l); m != nil {
			p.mark(i)
			p.sig = true
			p.res.Data.Passed += opAtoi(m[2])
			p.res.Data.Failed += opAtoi(m[3])
			p.res.Data.Skipped += opAtoi(m[4])
			inBlock = false
			continue
		}
		if m := rx.rsTest.FindStringSubmatch(l); m != nil {
			p.mark(i)
			if m[2] == "FAILED" {
				if _, ok := idx[m[1]]; !ok {
					idx[m[1]] = len(p.res.Data.Failures)
					p.res.Data.Failures = append(p.res.Data.Failures, opFailure{Test: m[1]})
				}
			}
			continue
		}
		if m := rx.rsBlock.FindStringSubmatch(l); m != nil {
			p.mark(i)
			inBlock, cur, wantMsg = true, nil, false
			k, ok := idx[m[1]]
			if !ok {
				k = len(p.res.Data.Failures)
				idx[m[1]] = k
				p.res.Data.Failures = append(p.res.Data.Failures, opFailure{Test: m[1]})
			}
			cur = &p.res.Data.Failures[k]
			continue
		}
		if rx.rsCompile.MatchString(l) || rx.rsRunning.MatchString(l) || rx.rsTail.MatchString(l) || strings.HasPrefix(t, "note: run with `RUST_BACKTRACE=") {
			p.mark(i)
			if t == "failures:" {
				inBlock = false
			}
			continue
		}
		if inBlock && cur != nil {
			p.mark(i)
			if m := rx.rsPanicNew.FindStringSubmatch(l); m != nil {
				cur.File, cur.Line, wantMsg = m[2], opAtoi(m[3]), true
				continue
			}
			if m := rx.rsPanicOld.FindStringSubmatch(l); m != nil {
				cur.File, cur.Line, cur.Message = m[3], opAtoi(m[4]), m[2]
				continue
			}
			if wantMsg {
				if cur.Message == "" {
					cur.Message = t
				} else if len(cur.Message) < 300 {
					cur.Message += " / " + t
				}
			}
			continue
		}
		if opRustLine(p, i) {
			continue
		}
	}
	return p.finish()
}

// opRustLine handles one rustc diagnostic line (shared by cargo and build).
func opRustLine(p *opRun, i int) bool {
	rx := opRx()
	l := p.lines[i]
	if m := rx.rsDiag.FindStringSubmatch(l); m != nil {
		p.mark(i)
		if rx.rsTail.MatchString(l) {
			return true
		}
		p.res.Data.Diags = append(p.res.Data.Diags, opDiag{Severity: m[1], Code: m[2], Msg: m[3]})
		return true
	}
	if m := rx.rsArrow.FindStringSubmatch(l); m != nil {
		p.mark(i)
		if n := len(p.res.Data.Diags); n > 0 && p.res.Data.Diags[n-1].File == "" {
			d := &p.res.Data.Diags[n-1]
			d.File, d.Line, d.Col = m[1], opAtoi(m[2]), opAtoi(m[3])
			p.sig = p.sig || p.res.Data.Kind == "build"
		}
		return true
	}
	if len(p.res.Data.Diags) > 0 && (rx.rsFrame.MatchString(l) || rx.rsNote.MatchString(l)) {
		p.mark(i)
		return true
	}
	if rx.rsTail.MatchString(l) {
		p.mark(i)
		return true
	}
	return false
}

// --- build / compile errors ------------------------------------------------

func opBuild(lines []string) *opResult {
	p := newOpRun(lines, "build", "")
	rx := opRx()
	formats := map[string]int{}
	add := func(f string, d opDiag) {
		formats[f]++
		p.res.Data.Diags = append(p.res.Data.Diags, d)
		if d.File != "" && d.Line > 0 {
			p.sig = true
		}
	}
	lastDiag := -1 // line index of the last diagnostic (continuations follow it)
	pySyn := -1    // line of a pending `File "x", line N` (SyntaxError)
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if m := rx.goDiag.FindStringSubmatch(l); m != nil {
			p.mark(i)
			add("go", opDiag{File: m[1], Line: opAtoi(m[2]), Col: opAtoi(m[3]), Severity: "error", Msg: m[4]})
			lastDiag = i
			continue
		}
		if m := rx.tsPlain.FindStringSubmatch(l); m != nil {
			p.mark(i)
			add("tsc", opDiag{File: m[1], Line: opAtoi(m[2]), Col: opAtoi(m[3]), Severity: m[4], Code: m[5], Msg: m[6]})
			lastDiag = i
			continue
		}
		if m := rx.tsPretty.FindStringSubmatch(l); m != nil {
			p.mark(i)
			add("tsc", opDiag{File: m[1], Line: opAtoi(m[2]), Col: opAtoi(m[3]), Severity: m[4], Code: m[5], Msg: m[6]})
			lastDiag = i
			continue
		}
		if m := rx.cDiag.FindStringSubmatch(l); m != nil {
			p.mark(i)
			sev := m[4]
			if sev == "fatal error" {
				sev = "error"
			}
			add("gcc", opDiag{File: m[1], Line: opAtoi(m[2]), Col: opAtoi(m[3]), Severity: sev, Msg: m[5]})
			lastDiag = i
			continue
		}
		if m := rx.cDiagNoCol.FindStringSubmatch(l); m != nil {
			p.mark(i)
			sev := m[3]
			if sev == "fatal error" {
				sev = "error"
			}
			add("gcc", opDiag{File: m[1], Line: opAtoi(m[2]), Severity: sev, Msg: m[4]})
			lastDiag = i
			continue
		}
		if m := rx.ldUndef.FindStringSubmatch(l); m != nil {
			p.mark(i)
			add("gcc", opDiag{File: m[1], Line: opAtoi(m[2]), Severity: "error", Msg: m[3]})
			lastDiag = i
			continue
		}
		if m := rx.pySynFile.FindStringSubmatch(l); m != nil {
			p.mark(i)
			pySyn = i
			continue
		}
		if pySyn >= 0 && i-pySyn <= 4 {
			if m := rx.pyExc.FindStringSubmatch(l); m != nil && opIndent(l) == 0 && strings.HasSuffix(m[1], "Error") {
				fm := rx.pySynFile.FindStringSubmatch(lines[pySyn])
				p.mark(i)
				add("python", opDiag{File: fm[1], Line: opAtoi(fm[2]), Severity: "error", Code: m[1], Msg: m[2]})
				pySyn, lastDiag = -1, i
				continue
			}
			p.mark(i) // source echo and caret of the SyntaxError
			continue
		}
		if strings.HasPrefix(l, "Sorry: ") && strings.Contains(l, "Error: ") {
			p.mark(i) // py_compile: "Sorry: IndentationError: ... (x.py, line 3)"
			msg := strings.TrimPrefix(l, "Sorry: ")
			d := opDiag{Severity: "error", Msg: msg}
			if j := strings.LastIndex(msg, " ("); j >= 0 && strings.HasSuffix(msg, ")") {
				if f, ln, ok := strings.Cut(msg[j+2:len(msg)-1], ", line "); ok {
					d.File, d.Line, d.Msg = f, opAtoi(ln), msg[:j]
				}
			}
			add("python", d)
			continue
		}
		if nd := len(p.res.Data.Diags); opRustLine(p, i) {
			if len(p.res.Data.Diags) > nd {
				formats["rustc"]++
				lastDiag = i
			}
			continue
		}
		if rx.caret.MatchString(l) && lastDiag >= 0 {
			p.mark(i)
			if i > 0 && !p.expl[i-1] && strings.TrimSpace(lines[i-1]) != "" {
				p.mark(i - 1) // source echo above the caret (clang/tsc pretty)
			}
			continue
		}
		switch {
		case strings.HasPrefix(l, "# "), strings.HasPrefix(l, "go: downloading "), strings.HasPrefix(l, "note: "),
			rx.cInFunc.MatchString(l), rx.cIncluded.MatchString(l), rx.cGenerated.MatchString(l), rx.cMisc.MatchString(l),
			rx.tsFound.MatchString(l), rx.cCmd.MatchString(l), rx.rsCompile.MatchString(l):
			p.mark(i)
			continue
		case lastDiag >= 0 && rx.cFrame.MatchString(l):
			p.mark(i) // gcc/clang 9+ code frame "  12 |   x = 1;"
			continue
		case lastDiag >= 0 && strings.HasPrefix(l, "\t"):
			p.mark(i) // go: "\thave (int)\n\twant (string)"
			continue
		case lastDiag >= 0 && rx.tsRelated.MatchString(l):
			p.mark(i) // tsc related information
			continue
		case lastDiag >= 0 && i-lastDiag <= 3 && rx.tsFiles.MatchString(l) && i+1 < len(lines) && rx.caret.MatchString(lines[i+1]):
			p.mark(i)
			continue
		case lastDiag >= 0 && i == lastDiag+1 && opIndent(l) >= 2 && len(p.res.Data.Diags) > 0 && formats["tsc"] > 0:
			p.mark(i) // tsc message continuation
			d := &p.res.Data.Diags[len(p.res.Data.Diags)-1]
			if len(d.Msg) < 300 {
				d.Msg += " " + t
			}
			lastDiag = i
			continue
		}
	}
	for _, d := range p.res.Data.Diags {
		switch d.Severity {
		case "error":
			p.res.Data.Errors++
		case "warning":
			p.res.Data.Warns++
		}
	}
	best, n := "build", 0
	for f, c := range formats {
		if c > n || c == n && f < best {
			best, n = f, c
		}
	}
	p.res.Data.Format = best
	return p.finish()
}

// --- stack traces ----------------------------------------------------------

func opTraceParse(lines []string) *opResult {
	p := newOpRun(lines, "trace", "")
	rx := opRx()
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		switch {
		case rx.pyTb.MatchString(l):
			i = opPyTrace(p, i) - 1
		case rx.pyChain.MatchString(l):
			p.mark(i)
		case rx.goPanic.MatchString(l):
			i = opGoPanic(p, i) - 1
		case rx.goRoutine.MatchString(l):
			// goroutine dump without a panic line (SIGQUIT, test timeout)
			p.res.Data.Traces = append(p.res.Data.Traces, opTrace{Lang: "go", Exception: "goroutine dump"})
			lines2 := p.lines
			p.mark(i)
			fn := ""
			j := i + 1
			for ; j < len(lines2); j++ {
				x := lines2[j]
				if rx.goFile.MatchString(x) {
					fm := rx.goFile.FindStringSubmatch(x)
					tr := &p.res.Data.Traces[len(p.res.Data.Traces)-1]
					if len(tr.Frames) < 30 {
						tr.Frames = append(tr.Frames, opFrame{File: fm[1], Line: opAtoi(fm[2]), Func: fn, InProject: opInProject(fm[1])})
					}
				} else if rx.goFunc.MatchString(x) {
					fn = rx.goFunc.FindStringSubmatch(x)[1]
				} else if !(strings.TrimSpace(x) == "" || rx.goRoutine.MatchString(x) || rx.goCreated.MatchString(x) || rx.goSignal.MatchString(x)) {
					break
				}
				p.mark(j)
			}
			p.sig = true
			i = j - 1
		case rx.nodeExc.MatchString(l) && i+1 < len(lines) && rx.jsAt.MatchString(lines[i+1]):
			i = opNodeTrace(p, i) - 1
		case rx.nodeLoc.MatchString(l):
			// "file.js:12" + source echo + caret, printed before the exception
			p.mark(i)
			for k := i + 1; k < len(lines) && k <= i+3; k++ {
				if rx.caret.MatchString(lines[k]) {
					for x := i + 1; x <= k; x++ {
						p.mark(x)
					}
					i = k
					break
				}
			}
		case rx.nodeVer.MatchString(l):
			p.mark(i)
		}
	}
	if n := len(p.res.Data.Traces); n > 0 {
		p.res.Data.Format = p.res.Data.Traces[n-1].Lang
		if p.res.Data.Format == "" {
			p.res.Data.Format = "trace"
		}
	} else {
		p.res.Data.Format = "trace"
	}
	return p.finish()
}

// opPyTrace consumes a Python traceback starting at i.
func opPyTrace(p *opRun, i int) int {
	rx := opRx()
	lines := p.lines
	p.mark(i)
	base := opIndent(lines[i])
	tr := opTrace{Lang: "python"}
	j := i + 1
	for ; j < len(lines); j++ {
		l := lines[j]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := rx.pyFrame.FindStringSubmatch(l); m != nil {
			p.mark(j)
			if len(tr.Frames) < 40 {
				tr.Frames = append(tr.Frames, opFrame{File: m[1], Line: opAtoi(m[2]), Func: m[3], InProject: opInProject(m[1])})
			}
			continue
		}
		if opIndent(l) > base {
			p.mark(j) // source line, carets, "[Previous line repeated N more times]"
			continue
		}
		if m := rx.pyExc.FindStringSubmatch(strings.TrimSpace(l)); m != nil && len(tr.Frames) > 0 {
			p.mark(j)
			tr.Exception, tr.Message = m[1], m[2]
			p.sig = true
			j++
		}
		break
	}
	p.res.Data.Traces = append(p.res.Data.Traces, tr)
	return j
}

// opNodeTrace consumes "TypeError: msg" plus its "    at ..." frames.
func opNodeTrace(p *opRun, i int) int {
	rx := opRx()
	lines := p.lines
	m := rx.nodeExc.FindStringSubmatch(lines[i])
	tr := opTrace{Lang: "node", Exception: m[1], Message: m[3]}
	p.mark(i)
	j := i + 1
	for ; j < len(lines); j++ {
		l := lines[j]
		if !rx.jsAt.MatchString(l) {
			break
		}
		p.mark(j)
		at := strings.TrimPrefix(strings.TrimSpace(l), "at ")
		fn := ""
		if k := strings.LastIndex(at, " ("); k >= 0 {
			fn, at = at[:k], strings.TrimSuffix(at[k+2:], ")")
		}
		if fm := rx.jsAtFile.FindStringSubmatch(at); fm != nil && len(tr.Frames) < 30 {
			tr.Frames = append(tr.Frames, opFrame{File: fm[1], Line: opAtoi(fm[2]), Func: fn, InProject: opInProject(fm[1])})
		}
	}
	// error properties printed after the stack: { code: 'ERR_X' }
	for ; j < len(lines) && j < i+60; j++ {
		l := lines[j]
		if strings.TrimSpace(l) == "" || !rx.nodeProp.MatchString(l) {
			break
		}
		p.mark(j)
	}
	if len(tr.Frames) > 0 {
		p.sig = true
	}
	p.res.Data.Traces = append(p.res.Data.Traces, tr)
	return j
}

// --- generic log lines -----------------------------------------------------

func opLevel(s string) string {
	switch strings.ToLower(s) {
	case "trace":
		return "trace"
	case "debug", "dbg":
		return "debug"
	case "info", "inf", "notice", "i":
		return "info"
	case "warn", "warning", "wrn", "w":
		return "warn"
	case "error", "err", "eror", "e", "severe":
		return "error"
	case "fatal", "ftl", "critical", "crit", "panic", "f":
		return "fatal"
	}
	return ""
}

func opLog(lines []string) *opResult {
	p := newOpRun(lines, "log", "log")
	rx := opRx()
	d := &opLogData{Levels: map[string]int{}}
	type agg struct {
		level, msg string
		n, first   int
	}
	groups := map[string]*agg{}
	stamped, entries := 0, 0
	inEntry := false
	sampleN := 0
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		ts, lvl, msg := "", "", ""
		ok := false
		switch {
		case l[0] == '{' && len(l) < 8192 && (strings.Contains(l, `"level"`) || strings.Contains(l, `"lvl"`) || strings.Contains(l, `"severity"`)):
			ts, lvl, msg, ok = opJSONLog(l)
		case strings.Contains(l, "level=") || strings.Contains(l, "lvl="):
			if m := rx.logLogfmtLevel.FindStringSubmatch(l); m != nil {
				lvl, ok = m[2], true
				if mm := rx.logLogfmtMsg.FindStringSubmatch(l); mm != nil {
					msg = mm[1] + mm[2]
				} else {
					msg = l
				}
				if tm := rx.logLogfmtTime.FindStringSubmatch(l); tm != nil {
					ts = tm[1]
				}
			}
		}
		if !ok {
			if m := rx.logISO.FindStringSubmatch(l); m != nil {
				ts, msg, ok = m[1], m[2], true
			} else if m := rx.logSyslog.FindStringSubmatch(l); m != nil {
				ts, msg, ok = m[1], m[3]+": "+m[4], true
			} else if m := rx.logGlog.FindStringSubmatch(l); m != nil {
				ts, lvl, msg, ok = m[2], m[1], m[3], true
			} else if m := rx.logAccess.FindStringSubmatch(l); m != nil {
				ts, msg, ok = m[1], m[3]+" "+m[2], true
				switch m[3][0] {
				case '5':
					lvl = "error"
				case '4':
					lvl = "warn"
				default:
					lvl = "info"
				}
			} else if m := rx.logTime.FindStringSubmatch(l); m != nil {
				ts, msg, ok = m[1], m[2], true
			} else if m := rx.logLevelOnly.FindStringSubmatch(l); m != nil {
				lvl, msg, ok = m[1], m[2], true
			}
		}
		if !ok {
			if inEntry && rx.pyTb.MatchString(l) {
				sig := p.sig // a traceback inside a log does not make it a log
				i = opPyTrace(p, i) - 1
				p.sig = sig
				continue
			}
			if inEntry && (opIndent(l) > 0 || strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "Caused by:")) {
				p.mark(i) // continuation of the previous entry (stack trace in a log)
			} else {
				inEntry = false
			}
			continue
		}
		if lvl == "" {
			head := msg
			if len(head) > 48 {
				head = head[:48]
			}
			if m := rx.logLevel.FindStringSubmatch(head); m != nil {
				lvl = m[1]
			}
		}
		lv := opLevel(lvl)
		if lv == "" {
			lv = "other"
		}
		p.mark(i)
		inEntry = true
		entries++
		if ts != "" {
			stamped++
			if d.First == "" {
				d.First = ts
			}
			d.Last = ts
		}
		d.Levels[lv]++
		norm := rx.logNorm.ReplaceAllString(msg, "#")
		if len(norm) > 160 {
			norm = norm[:160]
		}
		k := lv + "\x00" + norm
		if g := groups[k]; g != nil {
			g.n++
		} else {
			groups[k] = &agg{level: lv, msg: strings.TrimSpace(msg), n: 1, first: i}
		}
		if (lv == "error" || lv == "fatal" || lv == "warn" && sampleN < 4) && len(d.Samples) < 10 {
			d.Samples = append(d.Samples, opClip(t, 240))
			if lv == "warn" {
				sampleN++
			}
		}
	}
	d.Entries = entries
	var gs []*agg
	for _, g := range groups {
		gs = append(gs, g)
	}
	rank := map[string]int{"fatal": 0, "error": 1, "warn": 2}
	sort.Slice(gs, func(a, b int) bool {
		ra, ok := rank[gs[a].level]
		if !ok {
			ra = 3
		}
		rb, ok := rank[gs[b].level]
		if !ok {
			rb = 3
		}
		if gs[a].n != gs[b].n && (ra == rb || gs[a].n > 1 && gs[b].n > 1) {
			return gs[a].n > gs[b].n
		}
		if ra != rb {
			return ra < rb
		}
		return gs[a].first < gs[b].first
	})
	for k, g := range gs {
		if k == 8 {
			break
		}
		d.Top = append(d.Top, opLogMsg{Level: g.level, Count: g.n, Msg: opClip(g.msg, 200)})
	}
	p.res.Data.Log = d
	// Signature: enough entries, most of them timestamped or levelled.
	if entries >= 5 && (stamped >= 3 || entries >= 10) {
		p.sig = true
	}
	return p.finish()
}

// opJSONLog reads ts/level/msg from one JSON log line without encoding/json's
// reflection cost on every line: only the first level of string fields.
func opJSONLog(l string) (ts, lvl, msg string, ok bool) {
	get := func(keys ...string) string {
		for _, k := range keys {
			i := strings.Index(l, `"`+k+`"`)
			if i < 0 {
				continue
			}
			rest := strings.TrimLeft(l[i+len(k)+2:], " ")
			if !strings.HasPrefix(rest, ":") {
				continue
			}
			rest = strings.TrimLeft(rest[1:], " ")
			if strings.HasPrefix(rest, `"`) {
				if e := strings.IndexByte(rest[1:], '"'); e >= 0 {
					return rest[1 : e+1]
				}
				return ""
			}
			if e := strings.IndexAny(rest, ",}"); e > 0 {
				return strings.TrimSpace(rest[:e])
			}
		}
		return ""
	}
	lvl = get("level", "lvl", "severity")
	if lvl == "" {
		return "", "", "", false
	}
	return get("time", "ts", "timestamp", "@timestamp"), lvl, get("msg", "message"), true
}

// --- dispatch --------------------------------------------------------------

type opParserDef struct {
	name  string
	sniff func(s string) bool
	run   func(lines []string) *opResult
}

var (
	opSniffLocRx  = lazyRx(`^\S*\.\w+:\d+:\d+$`)
	opSniffLineRx = lazyRx(`^\S+:\d+$`)
)

// opSniffBuild looks for one compiler diagnostic line. It is line-based with
// small anchored regexes on the text before the first ": " because one
// unanchored multi-pattern regex over the whole output cost 80% of the parse
// time (13 MB/s on an M1, ~1 MB/s on the Pentium M).
func opSniffBuild(s string) bool {
	if strings.Contains(s, "undefined reference to ") || strings.Contains(s, "SyntaxError") ||
		strings.Contains(s, "IndentationError") || strings.Contains(s, "TabError") {
		return true
	}
	for len(s) > 0 {
		l := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			l, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		l = strings.TrimPrefix(l, "vet: ")
		if i := strings.Index(l, ": "); i > 0 && i < 400 {
			pre, rest := l[:i], l[i+2:]
			if pre == "error" || pre == "warning" || strings.HasPrefix(pre, "error[E") || strings.HasPrefix(pre, "warning[") {
				return true // rustc
			}
			if strings.IndexByte(pre, ':') > 0 && (opSniffLocRx.MatchString(pre) ||
				(strings.HasPrefix(rest, "error") || strings.HasPrefix(rest, "warning") || strings.HasPrefix(rest, "fatal error")) && opSniffLineRx.MatchString(pre)) {
				return true // go, gcc/clang, tsc --pretty without color
			}
		}
		if strings.Contains(l, " error TS") || strings.Contains(l, " warning TS") || strings.HasPrefix(strings.TrimLeft(l, " "), "--> ") {
			return true // tsc, rustc location arrow
		}
	}
	return false
}

var opParsers = []opParserDef{
	{"gotest", func(s string) bool {
		return strings.Contains(s, "--- FAIL") || strings.Contains(s, "--- PASS") || strings.Contains(s, "=== RUN") ||
			strings.Contains(s, "[no test files]") || strings.HasPrefix(s, "ok  ") || strings.Contains(s, "\nok  ") ||
			strings.Contains(s, "FAIL\t")
	}, opGoTest},
	{"pytest", func(s string) bool {
		return strings.Contains(s, "test session starts") || strings.Contains(s, "no tests ran") ||
			strings.Contains(s, " passed") || strings.Contains(s, " failed") || strings.Contains(s, " error")
	}, opPytest},
	{"unittest", func(s string) bool { return strings.HasPrefix(s, "Ran ") || strings.Contains(s, "\nRan ") }, opUnittest},
	{"jest", func(s string) bool {
		return strings.Contains(s, "Tests:") || strings.Contains(s, "Test Files ") || strings.Contains(s, "Tests  ")
	}, opJest},
	{"cargo", func(s string) bool { return strings.Contains(s, "test result: ") }, opCargo},
	{"build", opSniffBuild, opBuild},
	{"trace", func(s string) bool {
		return strings.Contains(s, "Traceback (most recent call last)") || strings.Contains(s, "goroutine ") ||
			strings.Contains(s, "panic: ") || strings.Contains(s, "\n    at ") || strings.Contains(s, "fatal error: ")
	}, opTraceParse},
	{"log", opSniffLog, opLog},
}

// opSniffLog: at least 3 of the first 40 non-blank lines start like a log line.
func opSniffLog(s string) bool {
	n, hits := 0, 0
	for len(s) > 0 && n < 40 {
		l := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			l, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		if l == "" {
			continue
		}
		n++
		c := l[0]
		if c >= '0' && c <= '9' || c == '[' || c == '{' || strings.Contains(l, "level=") ||
			len(l) > 4 && strings.Contains("JanFebMarAprMayJunJulAugSepOctNovDec", l[:3]) && l[3] == ' ' ||
			len(l) > 1 && strings.IndexByte("IWEF", c) >= 0 && l[1] >= '0' && l[1] <= '9' ||
			strings.HasPrefix(l, "INFO") || strings.HasPrefix(l, "WARN") || strings.HasPrefix(l, "ERROR") || strings.HasPrefix(l, "DEBUG") {
			hits++
		}
	}
	return hits >= 3
}

// opParse runs every parser whose cheap sniff matches and returns the best
// result (ok first, then confidence), or nil when nothing matched.
func opParse(s string) *opResult {
	if len(s) > opMaxParseBytes {
		return nil
	}
	lines := opClean(s)
	joined := strings.Join(lines, "\n")
	var best *opResult
	for _, d := range opParsers {
		if !d.sniff(joined) {
			continue
		}
		r := d.run(lines)
		if best == nil || r.OK && !best.OK || r.OK == best.OK && r.Confidence > best.Confidence {
			best = r
		}
	}
	return best
}

// --- rendering -------------------------------------------------------------

func opClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

func opLoc(file string, line, col int) string {
	switch {
	case file == "":
		return ""
	case line == 0:
		return file
	case col == 0:
		return fmt.Sprintf("%s:%d", file, line)
	}
	return fmt.Sprintf("%s:%d:%d", file, line, col)
}

// opSummary is the structured part (≤ opSummaryLines lines).
func opSummary(r *opResult) []string {
	d := &r.Data
	var out []string
	counts := func() string {
		var parts []string
		for _, c := range []struct {
			n    int
			name string
		}{{d.Failed, "failed"}, {d.Errors, "errors"}, {d.Passed, "passed"}, {d.Skipped, "skipped"}, {d.Warns, "warnings"}, {d.Total, "total"}} {
			if c.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", c.n, c.name))
			}
		}
		if len(parts) == 0 {
			return "no counts"
		}
		return strings.Join(parts, ", ")
	}
	switch d.Kind {
	case "test":
		out = append(out, fmt.Sprintf("%s: %s", d.Format, counts()))
		if len(d.Suites) > 0 {
			var bad []string
			ok := 0
			for _, s := range d.Suites {
				if s.Status == "FAIL" {
					bad = append(bad, s.Name)
				} else {
					ok++
				}
			}
			line := fmt.Sprintf("suites: %d ok, %d failed", ok, len(bad))
			if len(bad) > 0 {
				line += ": " + opClip(strings.Join(bad, ", "), 240)
			}
			out = append(out, line)
		}
		for k, f := range d.Failures {
			if k == 20 {
				out = append(out, fmt.Sprintf("… +%d more failures", len(d.Failures)-k))
				break
			}
			st := "FAIL"
			if f.Status == "error" {
				st = "ERROR"
			}
			line := st + " " + f.Test
			if loc := opLoc(f.File, f.Line, 0); loc != "" {
				line += " at " + loc
			}
			if f.Message != "" {
				line += ": " + f.Message
			}
			out = append(out, opClip(line, opLineClip))
		}
	case "build":
		files := map[string]bool{}
		for _, x := range d.Diags {
			files[x.File] = true
		}
		out = append(out, fmt.Sprintf("%s build: %d errors, %d warnings in %d files", d.Format, d.Errors, d.Warns, len(files)))
	case "trace":
	case "log":
		lg := d.Log
		var lv []string
		for _, k := range []string{"fatal", "error", "warn", "info", "debug", "trace", "other"} {
			if n := lg.Levels[k]; n > 0 {
				lv = append(lv, fmt.Sprintf("%s %d", k, n))
			}
		}
		line := fmt.Sprintf("log: %d entries", lg.Entries)
		if lg.First != "" {
			line += fmt.Sprintf(", %s → %s", lg.First, lg.Last)
		}
		out = append(out, line+"; levels: "+strings.Join(lv, ", "))
		if len(lg.Top) > 0 {
			out = append(out, "top messages:")
			for _, t := range lg.Top {
				out = append(out, opClip(fmt.Sprintf("  %d× [%s] %s", t.Count, t.Level, t.Msg), opLineClip))
			}
		}
		if len(lg.Samples) > 0 {
			out = append(out, "first errors/warnings:")
			for _, s := range lg.Samples {
				out = append(out, "  "+s)
			}
		}
	}
	for k, x := range d.Diags {
		if k == 25 {
			out = append(out, fmt.Sprintf("… +%d more diagnostics", len(d.Diags)-k))
			break
		}
		code := ""
		if x.Code != "" {
			code = "[" + x.Code + "]"
		}
		loc := opLoc(x.File, x.Line, x.Col)
		if loc != "" {
			loc += ": "
		}
		out = append(out, opClip(fmt.Sprintf("%s%s%s: %s", loc, x.Severity, code, x.Msg), opLineClip))
	}
	for k, t := range d.Traces {
		head := t.Exception
		if t.Message != "" {
			head += ": " + t.Message
		}
		out = append(out, opClip(fmt.Sprintf("%s %s", t.Lang, head), opLineClip))
		if k > 0 && k < len(d.Traces)-1 {
			continue // frames only for the first (root cause) and last exceptions
		}
		frames := t.Frames
		// Prefer project frames; keep at most 10, innermost call last as printed.
		var show []opFrame
		for _, f := range frames {
			if f.InProject {
				show = append(show, f)
			}
		}
		if len(show) == 0 {
			show = frames
		}
		if len(show) > 10 {
			show = append(show[:4:4], show[len(show)-6:]...)
		}
		for _, f := range show {
			fn := ""
			if f.Func != "" {
				fn = " in " + f.Func
			}
			out = append(out, opClip(fmt.Sprintf("  %s:%d%s", f.File, f.Line, fn), opLineClip))
		}
		if n := len(frames) - len(show); n > 0 {
			out = append(out, fmt.Sprintf("  (+%d library/runtime frames)", n))
		}
	}
	if len(out) > opSummaryLines {
		extra := len(out) - opSummaryLines + 1
		out = append(out[:opSummaryLines-1], fmt.Sprintf("… +%d more summary lines (see full output)", extra))
	}
	return out
}

// opRender builds what the model sees for a parsed result. lines are the
// cleaned output lines; spillPath may be empty.
func opRender(r *opResult, lines []string, spillPath string, rawBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Output parsed as %s %s (%.0f%% of %d lines explained, %d bytes). Summary, then unexplained lines, then the last %d lines.]\n",
		r.Data.Format, r.Data.Kind, r.Confidence*100, r.lines, rawBytes, opTailLines)
	for _, l := range opSummary(r) {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	tailFrom := len(lines) - opTailLines
	if tailFrom < 0 {
		tailFrom = 0
	}
	var un []string
	hidden := 0
	for i := 0; i < tailFrom && i < len(r.explained); i++ { // the tail shows the rest
		if r.explained[i] || strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if len(un) >= opUnparsedShown {
			hidden++
			continue
		}
		un = append(un, opClip(lines[i], opLineClip))
	}
	if len(un) > 0 {
		fmt.Fprintf(&b, "--- unexplained lines (%d) ---\n", len(un)+hidden)
		for _, l := range un {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		if hidden > 0 {
			fmt.Fprintf(&b, "… +%d more unexplained lines\n", hidden)
		}
	}
	if tailFrom > 0 {
		fmt.Fprintf(&b, "--- last %d lines ---\n", len(lines)-tailFrom)
	}
	for _, l := range lines[tailFrom:] {
		b.WriteString(opClip(l, maxLineChars))
		b.WriteByte('\n')
	}
	if spillPath != "" {
		fmt.Fprintf(&b, "[Full output saved to %s — use rg on it or read it with offset/limit instead of re-running.]", spillPath)
	}
	return strings.TrimRight(b.String(), "\n")
}
