//go:build !windows

package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------- helpers ----------

func shellTestNew(t *testing.T) (*Shell, string) {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
	home := t.TempDir()
	// macOS t.TempDir lives under a symlink (/var -> /private/var); bash's $PWD
	// keeps the logical path, so resolve to compare reliably.
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	s := NewShell(home)
	t.Cleanup(s.Close)
	return s, home
}

// shellOut drops the single trailing newline the command itself printed: the
// Shell returns the command output byte-exact (it strips only the protocol's
// own leading newline before the marker).
func shellOut(r ShellResult) string { return strings.TrimSuffix(r.Output, "\n") }

func shellRun(t *testing.T, s *Shell, cmd string) ShellResult {
	t.Helper()
	return shellRunT(t, s, cmd, 20*time.Second)
}

func shellRunT(t *testing.T, s *Shell, cmd string, to time.Duration) ShellResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), to+10*time.Second)
	defer cancel()
	return s.Run(ctx, cmd, to, nil)
}

func shellPidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func shellWaitDead(pid int, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !shellPidAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !shellPidAlive(pid)
}

// ---------- persistence ----------

func TestShellCwdPersists(t *testing.T) {
	s, home := shellTestNew(t)
	if err := os.Mkdir(filepath.Join(home, "sub dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := shellRun(t, s, `cd "sub dir"`)
	if r.ExitCode != 0 || r.Reset {
		t.Fatalf("cd failed: %+v", r)
	}
	want := filepath.Join(home, "sub dir")
	if s.Cwd != want {
		t.Fatalf("Cwd = %q want %q", s.Cwd, want)
	}
	r = shellRun(t, s, "pwd")
	if shellOut(r) != want {
		t.Fatalf("pwd = %q want %q", r.Output, want)
	}
}

func TestShellFirstCommandStartsInHome(t *testing.T) {
	s, home := shellTestNew(t)
	r := shellRun(t, s, "pwd")
	if shellOut(r) != home || r.ExitCode != 0 {
		t.Fatalf("first pwd = %+v want %q", r, home)
	}
	if s.Cwd != home {
		t.Fatalf("Cwd = %q", s.Cwd)
	}
}

func TestShellFirstCommandOutputClean(t *testing.T) {
	// The init line (set +o history; export LC_ALL...) must not leak anything
	// into the first command's output (e.g. setlocale warnings).
	s, _ := shellTestNew(t)
	r := shellRun(t, s, "echo hi")
	if shellOut(r) != "hi" {
		t.Fatalf("first output = %q, want %q", r.Output, "hi")
	}
}

func TestShellEnvAndFunctionsPersist(t *testing.T) {
	s, _ := shellTestNew(t)
	shellRun(t, s, "export JZ_TEST_VAR='hello world'; JZ_LOCAL=42; jzf() { echo \"fn:$1\"; }")
	cases := []struct{ cmd, want string }{
		{`echo "$JZ_TEST_VAR"`, "hello world"},
		{`echo "$JZ_LOCAL"`, "42"},
		{`jzf x`, "fn:x"},
		{`bash -c 'echo "$JZ_TEST_VAR"'`, "hello world"}, // exported to children
		{`echo "$JAZZ_CLI $TERM $PAGER"`, "1 dumb cat"},
	}
	for _, c := range cases {
		if r := shellRun(t, s, c.cmd); shellOut(r) != c.want || r.ExitCode != 0 {
			t.Errorf("%s => %+v, want %q", c.cmd, r, c.want)
		}
	}
}

// ---------- exit codes ----------

func TestShellExitCodes(t *testing.T) {
	s, _ := shellTestNew(t)
	cases := []struct {
		cmd  string
		code int
		out  string
	}{
		{"true", 0, ""},
		{"false", 1, ""},
		{"(exit 7)", 7, ""},
		{"echo out; (exit 255)", 255, "out"},
		{"sh -c 'exit 42'", 42, ""},
		{"no_such_command_jz_xyz 2>/dev/null", 127, ""},
		{"false; true", 0, ""},
		{"true | false", 1, ""},
		{"echo err 1>&2; false", 1, "err"}, // stderr merged
	}
	for _, c := range cases {
		r := shellRun(t, s, c.cmd)
		if r.ExitCode != c.code || r.Reset || shellOut(r) != c.out {
			t.Errorf("%q => code=%d reset=%v out=%q; want code=%d out=%q", c.cmd, r.ExitCode, r.Reset, r.Output, c.code, c.out)
		}
	}
}

// ---------- quoting / content ----------

func TestShellQuotingAndContent(t *testing.T) {
	s, _ := shellTestNew(t)
	cases := []struct {
		name, cmd, want string
	}{
		{"multiline", "echo a\necho b\n\necho c", "a\nb\nc"},
		{"heredoc", "cat <<'EOF'\nline $1 `x`\n  indented\\n\nEOF", "line $1 `x`\n  indented\\n"},
		{"heredoc-expand", "V=zz\ncat <<EOF\nv=$V\nEOF", "v=zz"},
		{"single-quotes", `echo 'it'"'"'s' '"dq"'`, `it's "dq"`},
		{"backslashes", `printf '%s\n' 'a\b\\c'`, `a\b\\c`},
		{"printf-escapes", `printf 'x\ty\n'`, "x\ty"},
		{"tab-in-cmd", "echo\t\ttabbed", "tabbed"},
		{"cr-in-cmd", "printf 'a\rb'", "a\rb"},
		{"unicode", "echo 'olá, mundo — ✓ 日本語 🎷'", "olá, mundo — ✓ 日本語 🎷"},
		{"marker-text", "echo __JZ_; echo '__JZ_deadbeefdeadbeef_0_/tmp__'", "__JZ_\n__JZ_deadbeefdeadbeef_0_/tmp__"},
		{"dollar-single", `echo $'a\tb'`, "a\tb"},
		{"semicolons-and-comment", "echo one; echo two # comment ; echo no", "one\ntwo"},
		{"subshell-cd-no-leak", "(cd /; pwd)", "/"},
		{"control-char", "printf '\\x01x' | od -An -tx1 | tr -d ' \\n'", "0178"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := shellRun(t, s, c.cmd)
			if shellOut(r) != c.want || r.ExitCode != 0 || r.Reset {
				t.Fatalf("cmd %q\n got %q (code %d reset %v note %q)\nwant %q", c.cmd, r.Output, r.ExitCode, r.Reset, r.Note, c.want)
			}
		})
	}
}

func TestShellOutputTrailingNewlines(t *testing.T) {
	s, _ := shellTestNew(t)
	cases := []struct{ cmd, want string }{
		{"printf abc", "abc"},           // no trailing newline
		{"echo abc", "abc"},             // one trailing newline is stripped
		{"printf 'abc\\n\\n'", "abc\n"}, // only one is stripped
		{"printf ''", ""},
	}
	for _, c := range cases {
		if r := shellRun(t, s, c.cmd); shellOut(r) != c.want {
			t.Errorf("%q => %q want %q", c.cmd, r.Output, c.want)
		}
	}
	// exact raw semantics: output is byte-exact, including the final newline
	for _, c := range []struct{ cmd, raw string }{
		{"printf abc", "abc"},
		{"echo abc", "abc\n"},
		{"printf 'abc\\n\\n'", "abc\n\n"},
		{"printf ''", ""},
	} {
		if r := shellRun(t, s, c.cmd); r.Output != c.raw {
			t.Errorf("raw %q => %q want %q", c.cmd, r.Output, c.raw)
		}
	}
	// and the next call is unaffected by output without trailing newline
	if r := shellRun(t, s, "printf x; printf y"); shellOut(r) != "xy" {
		t.Errorf("got %q", r.Output)
	}
	if r := shellRun(t, s, "echo next"); shellOut(r) != "next" {
		t.Errorf("got %q", r.Output)
	}
}

func TestShellSyntaxErrorsDontHang(t *testing.T) {
	s, _ := shellTestNew(t)
	// unterminated heredoc: bash warns and runs with what it has; must not hang.
	if r := shellRunT(t, s, "cat <<EOF", 5*time.Second); r.Reset {
		t.Errorf("heredoc without terminator reset the shell: %+v", r)
	}
	for _, cmd := range []string{
		`echo "unterminated`,
		`echo 'unterminated`,
		`if then fi`,
		`for`,
		`)`,
		`$(`,
		"echo `",
		`{`,
	} {
		t0 := time.Now()
		r := shellRunT(t, s, cmd, 5*time.Second)
		if r.Reset || r.ExitCode == 0 || time.Since(t0) > 4*time.Second {
			t.Errorf("%q => %+v (took %s); want fast nonzero, no reset", cmd, r, time.Since(t0))
		}
	}
	if r := shellRun(t, s, "echo alive"); shellOut(r) != "alive" || r.Reset {
		t.Fatalf("shell broken after syntax errors: %+v", r)
	}
}

func TestShellStdinIsDevNull(t *testing.T) {
	s, _ := shellTestNew(t)
	cases := []struct{ cmd, want string }{
		{"cat; echo done", "done"},
		{"read x; echo \"rc=$? x=[$x]\"", "rc=1 x=[]"},
		{"head -n1; echo ok", "ok"},
		{"wc -c | tr -d ' '", "0"},
	}
	for _, c := range cases {
		r := shellRunT(t, s, c.cmd, 5*time.Second)
		if shellOut(r) != c.want || r.Reset {
			t.Errorf("%q => %+v want %q", c.cmd, r, c.want)
		}
	}
	// protocol intact afterwards
	if r := shellRun(t, s, "echo still"); shellOut(r) != "still" {
		t.Fatalf("protocol corrupted: %+v", r)
	}
}

// ---------- reset semantics ----------

func TestShellTimeoutKillsProcessGroupAndResets(t *testing.T) {
	s, home := shellTestNew(t)
	sub := filepath.Join(home, "d")
	os.Mkdir(sub, 0o755)
	shellRun(t, s, "cd d; export JZ_GONE=1")
	if s.Cwd != sub {
		t.Fatalf("Cwd = %q", s.Cwd)
	}
	t0 := time.Now()
	r := shellRunT(t, s, "sleep 60 & echo bg=$!; sleep 60", 700*time.Millisecond)
	if el := time.Since(t0); el > 5*time.Second {
		t.Fatalf("timeout took %s", el)
	}
	if !r.Reset || r.ExitCode != -1 || !strings.Contains(r.Note, "timed out") {
		t.Fatalf("want reset/timeout, got %+v", r)
	}
	if s.Cwd != home {
		t.Fatalf("Cwd after reset = %q want %q", s.Cwd, home)
	}
	m := regexp.MustCompile(`bg=(\d+)`).FindStringSubmatch(r.Output)
	if m == nil {
		t.Fatalf("partial output not kept: %q", r.Output)
	}
	pid, _ := strconv.Atoi(m[1])
	if !shellWaitDead(pid, 3*time.Second) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("background child %d survived the reset", pid)
	}
	r = shellRun(t, s, `pwd; echo "[${JZ_GONE:-unset}]"`)
	if shellOut(r) != home+"\n[unset]" || r.Reset {
		t.Fatalf("fresh shell = %+v", r)
	}
}

func TestShellContextCancelResets(t *testing.T) {
	s, home := shellTestNew(t)
	os.Mkdir(filepath.Join(home, "d"), 0o755)
	shellRun(t, s, "cd d")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	t0 := time.Now()
	r := s.Run(ctx, "echo started; sleep 60", time.Minute, nil)
	if time.Since(t0) > 5*time.Second {
		t.Fatalf("cancel took %s", time.Since(t0))
	}
	if !r.Reset || r.ExitCode != -1 || !strings.Contains(r.Note, "Interrupted") {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(r.Output, "started") {
		t.Errorf("partial output lost: %q", r.Output)
	}
	if s.Cwd != home {
		t.Fatalf("Cwd = %q", s.Cwd)
	}
	if r := shellRun(t, s, "pwd"); shellOut(r) != home {
		t.Fatalf("after cancel pwd = %+v", r)
	}
}

func TestShellAlreadyCancelledContext(t *testing.T) {
	s, _ := shellTestNew(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := s.Run(ctx, "sleep 30", time.Minute, nil)
	// Either the command raced to completion (impossible for sleep 30) or reset.
	if !r.Reset {
		t.Fatalf("want reset, got %+v", r)
	}
	if r := shellRun(t, s, "echo ok"); shellOut(r) != "ok" {
		t.Fatalf("got %+v", r)
	}
}

func TestShellExitResetsAndReportsCode(t *testing.T) {
	s, home := shellTestNew(t)
	os.Mkdir(filepath.Join(home, "d"), 0o755)
	shellRun(t, s, "cd d; export JZ_X=1")
	r := shellRun(t, s, "echo bye; exit 3")
	if !r.Reset || r.ExitCode != 3 {
		t.Fatalf("got %+v", r)
	}
	if shellOut(r) != "bye" {
		t.Errorf("output = %q", r.Output)
	}
	if !strings.Contains(r.Note, "code 3") {
		t.Errorf("note = %q", r.Note)
	}
	if s.Cwd != home {
		t.Errorf("Cwd = %q", s.Cwd)
	}
	r = shellRun(t, s, `pwd; echo "${JZ_X:-unset}"`)
	if shellOut(r) != home+"\nunset" || r.Reset || r.ExitCode != 0 {
		t.Fatalf("next call = %+v", r)
	}
	// bare exit uses status of last command
	r = shellRun(t, s, "false; exit")
	if !r.Reset || r.ExitCode != 1 {
		t.Fatalf("bare exit = %+v", r)
	}
	if r := shellRun(t, s, "echo ok"); shellOut(r) != "ok" {
		t.Fatalf("got %+v", r)
	}
}

func TestShellDeadBetweenCallsRecovers(t *testing.T) {
	s, _ := shellTestNew(t)
	shellRun(t, s, "true")
	// kill bash out from under the Shell
	pid := s.inst.cmd.Process.Pid
	syscall.Kill(pid, syscall.SIGKILL)
	<-s.inst.dead
	r := shellRun(t, s, "echo x")
	if !r.Reset {
		// acceptable alternative: it noticed EOF and reported reset
		t.Fatalf("want reset after external kill, got %+v", r)
	}
	r = shellRun(t, s, "echo again")
	if shellOut(r) != "again" || r.Reset {
		t.Fatalf("not recovered: %+v", r)
	}
}

func TestShellCloseKillsChildren(t *testing.T) {
	s, _ := shellTestNew(t)
	r := shellRun(t, s, "sleep 60 >/dev/null 2>&1 & echo $!")
	pid, err := strconv.Atoi(shellOut(r))
	if err != nil {
		t.Fatalf("output %q", r.Output)
	}
	if !shellPidAlive(pid) {
		t.Fatalf("bg not running")
	}
	s.Close()
	if !shellWaitDead(pid, 3*time.Second) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d survived Close", pid)
	}
	if r := shellRun(t, s, "echo reopened"); shellOut(r) != "reopened" {
		t.Fatalf("after Close: %+v", r)
	}
}

// ---------- big output ----------

func TestShellLargeOutput5MB(t *testing.T) {
	s, _ := shellTestNew(t)
	const n = 5 << 20
	r := shellRunT(t, s, "head -c 5242880 /dev/zero | tr '\\0' 'a'; echo; echo END", 60*time.Second)
	if r.Reset || r.ExitCode != 0 {
		t.Fatalf("got reset=%v code=%d note=%q len=%d", r.Reset, r.ExitCode, r.Note, len(r.Output))
	}
	if len(r.Output) != n+5 || !strings.HasSuffix(r.Output, "\nEND\n") || strings.Trim(r.Output[:n], "a") != "" {
		t.Fatalf("len=%d suffix=%q", len(r.Output), r.Output[max(0, len(r.Output)-10):])
	}
	if r.Note != "" {
		t.Errorf("unexpected note %q", r.Note)
	}
	if r := shellRun(t, s, "echo after"); shellOut(r) != "after" {
		t.Fatalf("got %+v", r)
	}
}

func TestShellLargeOutputManyLines(t *testing.T) {
	s, _ := shellTestNew(t)
	r := shellRunT(t, s, "i=0; while [ $i -lt 20000 ]; do echo line$i; i=$((i+1)); done", 60*time.Second)
	if r.Reset || r.ExitCode != 0 {
		t.Fatalf("%+v", r.Note)
	}
	lines := strings.Split(shellOut(r), "\n")
	if len(lines) != 20000 || lines[0] != "line0" || lines[19999] != "line19999" {
		t.Fatalf("got %d lines", len(lines))
	}
}

func TestShellOutputOverMaxKeepStillFindsMarker(t *testing.T) {
	s, _ := shellTestNew(t)
	s.maxKeep = 64 << 10
	r := shellRunT(t, s, "head -c 1000000 /dev/zero | tr '\\0' 'a'; echo; echo END", 15*time.Second)
	if r.Reset || r.ExitCode != 0 {
		t.Fatalf("over-limit output: reset=%v code=%d note=%q", r.Reset, r.ExitCode, r.Note)
	}
	if !strings.Contains(r.Note, "discarded") {
		t.Errorf("want discard note, got %q", r.Note)
	}
	if len(r.Output) > (64<<10)+(32<<10) {
		t.Errorf("kept %d bytes, over the limit", len(r.Output))
	}
}

// shellFake wires a Shell to an in-test fake: we read the command line the
// Shell writes, learn its random marker, and feed output chunks split exactly
// where we want. This makes chunk-boundary behaviour deterministic.
type shellFake struct {
	s     *Shell
	lines *bufio.Reader
	out   chan []byte
}

func shellNewFake(t *testing.T) *shellFake {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })
	s := NewShell(t.TempDir())
	s.inst = &shellInst{ // non-nil: Run won't start a real bash
		cmd:   exec.Command("true"),
		stdin: pw,
		out:   make(chan []byte, 64),
		dead:  make(chan struct{}),
	}
	return &shellFake{s: s, lines: bufio.NewReader(pr), out: s.inst.out}
}

func (f *shellFake) marker(t *testing.T) string {
	t.Helper()
	line, err := f.lines.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(__JZ_[0-9a-f]{16}_)`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no marker in %q", line)
	}
	if !strings.HasPrefix(line, "eval $'") || !strings.Contains(line, "</dev/null 2>&1") {
		t.Fatalf("unexpected protocol line %q", line)
	}
	return m[1]
}

func shellFakeRun(t *testing.T, chunks func(marker string) [][]byte) ShellResult {
	t.Helper()
	f := shellNewFake(t)
	res := make(chan ShellResult, 1)
	go func() { res <- f.s.Run(context.Background(), "x", 2*time.Second, nil) }()
	mk := f.marker(t)
	for _, c := range chunks(mk) {
		f.out <- c
	}
	return <-res
}

func TestShellMarkerSplitAcrossChunks(t *testing.T) {
	cwd := "/home/user/proj"
	for split := 1; split < 42; split++ {
		r := shellFakeRun(t, func(mk string) [][]byte {
			all := "hello\n\n" + mk + "5_" + cwd + "_" + mk + "E\n" // protocol: marker code _ pwd _ marker E
			cut := len("hello\n") + split
			return [][]byte{[]byte(all[:cut]), []byte(all[cut:])}
		})
		if r.Reset || r.ExitCode != 5 || shellOut(r) != "hello" {
			t.Fatalf("split %d: %+v", split, r)
		}
	}
}

func TestShellMarkerSplitLongCwd(t *testing.T) {
	cwd := "/home/user/" + strings.Repeat("very-long-directory-name/", 8) + "proj"
	r := shellFakeRun(t, func(mk string) [][]byte {
		all := "out\n" + mk + "0_" + cwd + "_" + mk + "E\n"
		cut := strings.Index(all, mk) + len(mk) + 2 + 100 // inside the cwd
		return [][]byte{[]byte(all[:cut]), []byte(all[cut:])}
	})
	if r.Reset || r.ExitCode != 0 || shellOut(r) != "out" {
		t.Fatalf("got %+v", r)
	}
}

func TestShellLongCwdRealShell(t *testing.T) {
	// Real-shell version of the above: long cwd plus enough output that the
	// reader's 32 KiB chunk boundary may land inside the marker line. Not
	// deterministic, so it only checks the common (unsplit) path works.
	s, home := shellTestNew(t)
	deep := filepath.Join(home, strings.Repeat("d", 100), strings.Repeat("e", 100))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	shellRun(t, s, "cd "+bashQuote(deep))
	if s.Cwd != deep {
		t.Fatalf("Cwd = %q", s.Cwd)
	}
	r := shellRun(t, s, "echo ok")
	if shellOut(r) != "ok" {
		t.Fatalf("%+v", r)
	}
}

func TestShellCwdWithUnderscoresAndSpaces(t *testing.T) {
	s, home := shellTestNew(t)
	name := "a__b _c__ 0_x__"
	d := filepath.Join(home, name)
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	r := shellRun(t, s, "cd "+bashQuote(name)+"; (exit 4)")
	if r.ExitCode != 4 || s.Cwd != d {
		t.Fatalf("code=%d Cwd=%q want %q", r.ExitCode, s.Cwd, d)
	}
}

func TestShellOnChunkStreams(t *testing.T) {
	s, _ := shellTestNew(t)
	var got []byte
	sawNil := false
	r := s.Run(context.Background(), "echo one; sleep 0.3; echo two", 10*time.Second, func(b []byte) {
		if b == nil {
			sawNil = true
			return
		}
		got = append(got, b...)
	})
	if shellOut(r) != "one\ntwo" {
		t.Fatalf("%+v", r)
	}
	if !sawNil {
		t.Errorf("onChunk(nil) end signal not sent")
	}
	if !strings.Contains(string(got), "one") {
		t.Errorf("live chunk not streamed: %q", got)
	}
	if strings.Contains(string(got), "__JZ_") {
		t.Errorf("marker leaked to UI: %q", got)
	}
}

// Documents behaviour: a background job that keeps printing after its command
// returned leaks its output into later commands' results (they share the pty
// pipe). The protocol itself must stay intact: exit codes and the final
// command output must still be correct.
func TestShellBackgroundPrinterDocumented(t *testing.T) {
	s, _ := shellTestNew(t)
	r := shellRun(t, s, "(for i in 1 2 3 4 5 6 7 8 9 10; do echo bgtick; sleep 0.1; done) & echo started")
	if r.ExitCode != 0 || !strings.HasPrefix(r.Output, "started") && !strings.HasPrefix(r.Output, "bgtick") {
		t.Fatalf("%+v", r)
	}
	time.Sleep(250 * time.Millisecond)
	r = shellRun(t, s, "echo mine; (exit 6)")
	if r.ExitCode != 6 || r.Reset {
		t.Fatalf("protocol corrupted by bg job: %+v", r)
	}
	if !strings.Contains(r.Output, "mine") {
		t.Fatalf("own output missing: %q", r.Output)
	}
	t.Logf("documented: second result contains bg noise: %q", r.Output)
	time.Sleep(1200 * time.Millisecond) // let bg job finish
	shellRun(t, s, "true")              // flushes noise the bg job printed while the shell was idle
	r = shellRun(t, s, "echo clean")
	if shellOut(r) != "clean" {
		t.Errorf("after bg job finished: %q", r.Output)
	}
}

func TestShellBackgroundJobOutputToFileNoLeak(t *testing.T) {
	s, home := shellTestNew(t)
	r := shellRun(t, s, "nohup sh -c 'sleep 0.3; echo done' > bg.log 2>&1 &")
	if r.ExitCode != 0 || r.Reset {
		t.Fatalf("%+v", r)
	}
	time.Sleep(600 * time.Millisecond)
	b, _ := os.ReadFile(filepath.Join(home, "bg.log"))
	if strings.TrimSpace(string(b)) != "done" {
		t.Fatalf("log = %q", b)
	}
	if r := shellRun(t, s, "echo x"); shellOut(r) != "x" {
		t.Fatalf("%q", r.Output)
	}
}

func TestShellBashQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "$''"},
		{"abc", "$'abc'"},
		{"a'b", `$'a\'b'`},
		{`a\b`, `$'a\\b'`},
		{"a\nb\tc\rd", `$'a\nb\tc\rd'`},
		{"\x01\x7f", `$'\x01\x7f'`},
		{"é", "$'é'"},
	}
	for _, c := range cases {
		if got := bashQuote(c.in); got != c.want {
			t.Errorf("bashQuote(%q) = %q want %q", c.in, got, c.want)
		}
		if strings.Contains(bashQuote(c.in), "\n") {
			t.Errorf("multi-line quote for %q", c.in)
		}
	}
	// round-trip through real bash
	s, _ := shellTestNew(t)
	for _, in := range []string{"a'b\"c\\d$e`f\ng\th", "\x01\x02 mixed é ✓", "!history !! $((1+1))"} {
		r := shellRun(t, s, "printf '%s' "+bashQuote(in))
		if shellOut(r) != in {
			t.Errorf("round trip %q -> %q", in, r.Output)
		}
	}
}

func TestShellConcurrentRunsSerialized(t *testing.T) {
	s, _ := shellTestNew(t)
	done := make(chan ShellResult, 8)
	for i := 0; i < 8; i++ {
		i := i
		go func() { done <- shellRun(t, s, "echo n"+strconv.Itoa(i)+"; (exit "+strconv.Itoa(i)+")") }()
	}
	for i := 0; i < 8; i++ {
		r := <-done
		code, err := strconv.Atoi(strings.TrimPrefix(shellOut(r), "n"))
		if err != nil || code != r.ExitCode {
			t.Errorf("mismatched result %+v", r)
		}
	}
}

// ---------- truncate.go ----------

func TestShellTruncateShortUnchanged(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	for _, s := range []string{"", "abc", strings.Repeat("x", 100)} {
		if got := truncateHeadTail(s, 100); got != s {
			t.Errorf("changed %q", s)
		}
	}
	if _, err := os.Stat(spillDir()); err == nil {
		ents, _ := os.ReadDir(spillDir())
		if len(ents) != 0 {
			t.Errorf("spilled without truncation")
		}
	}
}

var shellSpillRe = regexp.MustCompile(`Full output saved to (\S+) —`)

func TestShellTruncateHeadTailLines(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("line-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("-padding\n")
	}
	b.WriteString("FINAL ERROR")
	s := b.String()
	const mx = 2000
	got := truncateHeadTail(s, mx)
	i := strings.Index(got, "\n\n[… ")
	j := strings.LastIndex(got, "]\n\n")
	if i < 0 || j < i {
		t.Fatalf("no note in %q", got)
	}
	head, note, tail := got[:i], got[i:j+3], got[j+3:]
	if !strings.HasPrefix(s, head) || !strings.HasSuffix(s, tail) {
		t.Fatalf("head/tail not taken from source")
	}
	if len(head) > mx/5 || len(head) < mx/10 {
		t.Errorf("head len %d, want about 20%% of %d", len(head), mx)
	}
	if len(tail) > mx-mx/5 || len(tail) < (mx-mx/5)/2 {
		t.Errorf("tail len %d, want about 80%%", len(tail))
	}
	// line boundaries: head ends at a line end, tail starts at a line start
	if s[len(head)] != '\n' {
		t.Errorf("head not cut at line boundary: ...%q", head[max(0, len(head)-20):])
	}
	if s[len(s)-len(tail)-1] != '\n' {
		t.Errorf("tail not cut at line boundary: %q...", tail[:20])
	}
	if !strings.HasSuffix(tail, "FINAL ERROR") {
		t.Errorf("tail lost the end")
	}
	omitted := len(s) - len(head) - len(tail)
	if !strings.Contains(note, strconv.Itoa(omitted)+" bytes omitted") {
		t.Errorf("note %q missing omitted=%d", note, omitted)
	}
	if !strings.Contains(note, "1001 lines total") {
		t.Errorf("note %q missing line count", note)
	}
	m := shellSpillRe.FindStringSubmatch(note)
	if m == nil {
		t.Fatalf("no spill path in %q", note)
	}
	if !strings.HasPrefix(m[1], spillDir()) {
		t.Errorf("spill %q not under %q", m[1], spillDir())
	}
	full, err := os.ReadFile(m[1])
	if err != nil || string(full) != s {
		t.Fatalf("spill content mismatch (err %v, len %d vs %d)", err, len(full), len(s))
	}
	if fi, _ := os.Stat(m[1]); fi.Mode().Perm() != 0o600 {
		t.Errorf("spill perm %v", fi.Mode().Perm())
	}
}

func TestShellTruncateNoNewlines(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	s := strings.Repeat("a", 5000) + strings.Repeat("z", 5000)
	got := truncateHeadTail(s, 1000)
	if !strings.HasPrefix(got, strings.Repeat("a", 200)+"\n\n[… ") {
		t.Errorf("head should be exactly 20%% when no newline")
	}
	if !strings.HasSuffix(got, "]\n\n"+strings.Repeat("z", 800)) {
		t.Errorf("tail should be exactly 80%% when no newline")
	}
	if !strings.Contains(got, "9000 bytes omitted") {
		t.Errorf("omitted count wrong: %q", got[200:300])
	}
}

func TestShellTruncateFarNewlineIgnored(t *testing.T) {
	// A newline too close to the start of the head window (< headN/2) must not
	// shrink the head drastically; same for tail.
	t.Setenv("JAZZ_HOME", t.TempDir())
	s := "x\n" + strings.Repeat("a", 5000) + strings.Repeat("b", 5000) + "\ny"
	got := truncateHeadTail(s, 1000)
	if !strings.HasPrefix(got, "x\n"+strings.Repeat("a", 198)+"\n\n[") {
		t.Errorf("head shrank to early newline: %q", got[:40])
	}
	if !strings.HasSuffix(got, strings.Repeat("b", 797)+"\ny") {
		t.Errorf("tail shrank to late newline")
	}
}

func TestShellTruncateSpillsUnique(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	big := strings.Repeat("q\n", 5000)
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		m := shellSpillRe.FindStringSubmatch(truncateHeadTail(big+strconv.Itoa(i), 100))
		if m == nil {
			t.Fatal("no spill")
		}
		if seen[m[1]] {
			t.Fatalf("spill path reused: %s", m[1])
		}
		seen[m[1]] = true
	}
}

func TestShellTruncateSpillFailureStillTruncates(t *testing.T) {
	j := t.TempDir()
	t.Setenv("JAZZ_HOME", j)
	// make data dir a file so MkdirAll fails
	if err := os.WriteFile(filepath.Join(j, "data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := truncateHeadTail(strings.Repeat("line\n", 1000), 200)
	if strings.Contains(got, "Full output saved") {
		t.Errorf("claims spill that failed: %q", got)
	}
	if !strings.Contains(got, "bytes omitted") || len(got) > 400 {
		t.Errorf("not truncated: len %d", len(got))
	}
}

func TestShellCleanSpill(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	os.MkdirAll(spillDir(), 0o700)
	old := filepath.Join(spillDir(), "old.txt")
	nw := filepath.Join(spillDir(), "new.txt")
	os.WriteFile(old, []byte("o"), 0o600)
	os.WriteFile(nw, []byte("n"), 0o600)
	past := time.Now().Add(-73 * time.Hour)
	os.Chtimes(old, past, past)
	cleanSpill()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old spill not removed")
	}
	if _, err := os.Stat(nw); err != nil {
		t.Errorf("new spill removed")
	}
}

func TestShellClipLine(t *testing.T) {
	short := strings.Repeat("a", maxLineChars)
	if clipLine(short) != short {
		t.Errorf("clipped at limit")
	}
	long := strings.Repeat("b", maxLineChars+10)
	got := clipLine(long)
	if !strings.HasPrefix(got, short[:0]+strings.Repeat("b", maxLineChars)+"…") || !strings.Contains(got, strconv.Itoa(maxLineChars+10)+" bytes") {
		t.Errorf("got %q", got[maxLineChars-5:])
	}
}

func TestShellCwdWithNewline(t *testing.T) {
	s, home := shellTestNew(t)
	d := filepath.Join(home, "a\nb")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	r := shellRunT(t, s, "cd "+bashQuote("a\nb"), 3*time.Second)
	if r.Reset || r.ExitCode != 0 || s.Cwd != d {
		t.Fatalf("cd into dir with newline: %+v Cwd=%q", r, s.Cwd)
	}
}
