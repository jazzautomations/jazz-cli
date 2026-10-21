package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Shell is a persistent bash: cwd, env, functions and activated venvs survive
// across calls. Design from deepseek-harness: marker-delimited commands, exit code
// always reported, and "reset, never repair" on timeout/cancel/exit.
type Shell struct {
	mu      sync.Mutex
	home    string // project dir: where a fresh shell starts
	inst    *shellInst
	pgid    atomic.Int32 // process group of the live shell (for lock-free kill)
	Cwd     string
	maxKeep int // bytes of output kept per command (head + tail)
	Env     []string
	d       shellDialect // which shell and how to talk to it (shell_dialect.go)

	// gen counts commands run in the persistent shell; the exported-env
	// snapshot that one-shot read-only commands use is valid for one
	// generation (partools.go)
	gen      atomic.Int64
	ptMu     sync.Mutex
	ptEnvVal []string
	ptEnvGen int64
}

type shellInst struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   chan []byte
	dead  chan struct{}
	code  int
}

func NewShell(home string) *Shell {
	return &Shell{home: home, Cwd: home, maxKeep: 8 << 20, d: detectShell()}
}

// Dialect reports the shell this Shell runs (bash, sh or PowerShell).
func (s *Shell) Dialect() shellDialect { return s.d }

func (s *Shell) start() error {
	argv := s.d.persistentArgv()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = s.home
	env := s.Env
	if env == nil {
		env = scrubEnv(os.Environ())
	}
	cmd.Env = append(env,
		"TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "MANPAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true",
		"DEBIAN_FRONTEND=noninteractive", "PIP_NO_INPUT=1", "NPM_CONFIG_YES=true", "CI=1", "JAZZ_CLI=1", "NO_COLOR=1",
	)
	procPrepare(cmd, false)
	sbWrapCmd(cmd) // the whole shell (and all it runs) lives in the OS sandbox
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	stdin, err := cmd.StdinPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	procAttach(cmd)
	pw.Close()
	inst := &shellInst{cmd: cmd, stdin: stdin, out: make(chan []byte, 256), dead: make(chan struct{})}
	s.inst = inst
	s.pgid.Store(int32(cmd.Process.Pid))
	s.Cwd = s.home
	safeGo(func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				c := make([]byte, n)
				copy(c, buf[:n])
				select {
				case inst.out <- c:
				default:
					select {
					case inst.out <- c:
					case <-inst.dead: // shell gone and nobody reading: drop instead of leaking
						pr.Close()
						close(inst.out)
						return
					}
				}
			}
			if err != nil {
				pr.Close()
				close(inst.out)
				return
			}
		}
	})
	safeGo(func() {
		err := cmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			inst.code = ee.ExitCode()
		}
		procForget(cmd.Process.Pid)
		close(inst.dead)
	})
	// sane defaults for a non-interactive session
	_, errFd := exec.LookPath("fd")
	_, errFdfind := exec.LookPath("fdfind")
	fmt.Fprintln(stdin, s.d.initLine(errFd == nil, errFdfind == nil))
	return nil
}

// killGroup kills the live shell's process group without taking the mutex.
func (s *Shell) killGroup() {
	if pg := s.pgid.Load(); pg > 0 {
		procKillTree(int(pg))
	}
}

func (s *Shell) kill() {
	s.killGroup()
	if s.inst != nil {
		if s.inst.cmd.Process != nil {
			_ = s.inst.cmd.Process.Kill()
		}
		select {
		case <-s.inst.dead:
		case <-time.After(3 * time.Second):
		}
	}
	s.inst = nil
	s.pgid.Store(0)
	s.Cwd = s.home
}

// Close kills the shell and everything it started. Safe to call while a command runs.
func (s *Shell) Close() {
	s.killGroup()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kill()
}

// bashQuote renders s as a bash $'…' literal (safe for any bytes, one physical line).
func bashQuote(s string) string {
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\'':
			b.WriteString(`\'`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

type ShellResult struct {
	Output   string
	ExitCode int
	Note     string // reset / timeout / interrupt diagnostics
	Reset    bool
	Duration time.Duration
}

// outBuf keeps the head and the tail of a stream, dropping the middle past a cap.
type outBuf struct {
	head, tail []byte
	headCap    int
	tailCap    int
	total      int
}

func (o *outBuf) write(p []byte) {
	o.total += len(p)
	if len(o.head) < o.headCap {
		n := o.headCap - len(o.head)
		if n > len(p) {
			n = len(p)
		}
		o.head = append(o.head, p[:n]...)
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	o.tail = append(o.tail, p...)
	if len(o.tail) > 2*o.tailCap {
		o.tail = append(o.tail[:0:0], o.tail[len(o.tail)-o.tailCap:]...)
	}
}

// upTo returns the stream content before absolute offset end, and the number
// of bytes that were dropped from the middle.
func (o *outBuf) upTo(end int) (string, int) {
	if end <= len(o.head) {
		return string(o.head[:end]), 0
	}
	tailStart := o.total - len(o.tail)
	if tailStart < len(o.head) {
		tailStart = len(o.head)
	}
	tailOff := o.total - len(o.tail)
	from := tailStart - tailOff
	to := end - tailOff
	if to > len(o.tail) {
		to = len(o.tail)
	}
	if from < 0 {
		from = 0
	}
	if from > to {
		from = to
	}
	if to-from > o.tailCap { // tail may hold up to 2*tailCap between trims
		from = to - o.tailCap
	}
	dropped := tailOff + from - len(o.head)
	if dropped < 0 {
		dropped = 0
	}
	if dropped == 0 {
		return string(o.head) + string(o.tail[from:to]), 0
	}
	return string(o.head) + fmt.Sprintf("\n[… %d bytes omitted …]\n", dropped) + string(o.tail[from:to]), dropped
}

// Run executes one command. onChunk (optional) receives live output for the UI.
func (s *Shell) Run(ctx context.Context, command string, timeout time.Duration, onChunk func([]byte)) ShellResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.gen.Add(1)
	t0 := time.Now()
	if s.inst == nil {
		if err := s.start(); err != nil {
			return ShellResult{Output: "failed to start " + s.d.Name() + ": " + err.Error(), ExitCode: -1}
		}
	}
	inst := s.inst
	nb := make([]byte, 8)
	_, _ = rand.Read(nb)
	nonce := hex.EncodeToString(nb)
	marker := "__JZ_" + nonce + "_"
	mb := []byte(marker)
	// line: "\n" marker code "_" pwd "_" marker "E\n"  — pwd may contain anything but the nonce
	// the exit code may be negative on Windows (NTSTATUS crash codes)
	re := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(marker) + `(-?\d+)_(.*?)_` + regexp.QuoteMeta(marker) + `E\r?\n`)
	line := s.d.wrapLine(command, marker)
	if _, err := io.WriteString(inst.stdin, line); err != nil {
		s.kill()
		return ShellResult{Output: "", ExitCode: -1, Reset: true, Note: "[shell was dead and has been reset — run the command again]", Duration: time.Since(t0)}
	}

	ob := &outBuf{headCap: s.maxKeep / 4, tailCap: s.maxKeep - s.maxKeep/4}
	var win []byte // sliding window used only to find the marker line
	winOff := 0    // absolute offset of win[0]
	const maxLine = 8192
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case chunk, ok := <-inst.out:
			if !ok {
				<-inst.dead
				code := inst.code
				out, _ := ob.upTo(ob.total)
				s.inst = nil
				s.pgid.Store(0)
				s.Cwd = s.home
				return ShellResult{Output: strings.TrimSuffix(out, "\n"), ExitCode: code, Reset: true,
					Note: fmt.Sprintf("[shell exited: code %d — the next call starts a fresh shell in %s]", code, s.home), Duration: time.Since(t0)}
			}
			ob.write(chunk)
			win = append(win, chunk...)
			if i := bytes.Index(win, mb); i >= 0 {
				if loc := re.FindSubmatchIndex(win[i:]); loc != nil {
					code, _ := strconv.Atoi(string(win[i+loc[2] : i+loc[3]]))
					cwd := string(win[i+loc[4] : i+loc[5]])
					out, dropped := ob.upTo(winOff + i)
					out = strings.TrimSuffix(out, "\n")
					if onChunk != nil {
						onChunk(nil)
					}
					if s.d.Win {
						cwd = filepath.Clean(cwd) // C:/x from pwd -W -> C:\x, like the project root
					}
					s.Cwd = cwd
					r := ShellResult{Output: out, ExitCode: code, Duration: time.Since(t0)}
					if dropped > 0 {
						r.Note = fmt.Sprintf("[%d bytes of output were discarded from the middle (over %d MB)]", dropped, s.maxKeep>>20)
					}
					return r
				}
				// marker seen but line incomplete: keep from the marker on
				if i > 0 {
					win = win[i:]
					winOff += i
				}
			} else if len(win) > maxLine {
				cut := len(win) - len(mb) - 1
				win = win[cut:]
				winOff += cut
			}
			if onChunk != nil {
				onChunk(chunk)
			}
		case <-timer.C:
			out, _ := ob.upTo(ob.total)
			s.kill()
			return ShellResult{Output: out, ExitCode: -1, Reset: true, Duration: time.Since(t0),
				Note: fmt.Sprintf("[Command timed out after %s and was killed. The shell was reset: the next call starts fresh in %s (cwd and env lost). For long jobs use background:true.]", timeout.Round(time.Second), s.home)}
		case <-ctx.Done():
			out, _ := ob.upTo(ob.total)
			s.kill()
			return ShellResult{Output: out, ExitCode: -1, Reset: true, Duration: time.Since(t0),
				Note: "[Interrupted by the user. The shell was reset: the next call starts fresh.]"}
		}
	}
}

// ---------- background jobs ----------

type Job struct {
	ID     string
	PID    int
	Cmd    string
	Log    string
	Start  time.Time
	offset int64
}

func jobsDir() string { return filepath.Join(dataDir(), "jobs") }

// defaultMaxJobs is the background job cap when config max_jobs is unset.
const defaultMaxJobs = 8

func (t *Toolbox) startJob(ctx context.Context, cmd string) ToolOutput {
	limit := t.MaxJobs
	if limit <= 0 {
		limit = defaultMaxJobs
	}
	if n := t.RunningJobs(); n >= limit {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Refused: %d background jobs are already running (limit %d). Stop one with bash job:<id> action:\"kill\" before starting another.", n, limit),
			Display: Display{Summary: fmt.Sprintf("limite de %d jobs", limit)}}
	}
	if err := os.MkdirAll(jobsDir(), 0o700); err != nil {
		return ToolOutput{IsErr: true, Text: "cannot create jobs dir: " + err.Error()}
	}
	t.mu.Lock()
	t.nextJob++
	id := fmt.Sprintf("j%d", t.nextJob)
	t.mu.Unlock()
	// the pid keeps concurrent jazz processes (eval lanes) from sharing a log file
	log := filepath.Join(jobsDir(), fmt.Sprintf("%s-%d-%s.log", time.Now().Format("150405"), os.Getpid(), id))
	var pid int
	if runtime.GOOS == "windows" || sbCurrent().PidNamespace() {
		// no setsid and no reliable $! (MSYS pids are not Windows pids; under
		// bwrap $! is a pid inside the sandbox's pid namespace): start the job
		// ourselves in the shell's cwd, in its own process group and job
		// object (and its own sandbox), output to the log
		var env []string
		if runtime.GOOS != "windows" {
			env = t.Shell.exportedEnv(ctx)
		}
		p, err := t.Shell.startDetached(cmd, log, env)
		if err != nil {
			return ToolOutput{IsErr: true, Text: "failed to start background job: " + err.Error(), Display: Display{Summary: "falhou ao iniciar job"}}
		}
		pid = p
	} else {
		launcher := "setsid"
		if _, err := exec.LookPath("setsid"); err != nil {
			launcher = "nohup"
		}
		// run from the persistent shell so cwd/env apply; setsid detaches it from our process group
		run := fmt.Sprintf("( %s bash -c %s >%s 2>&1 </dev/null & echo $! )", launcher, bashQuote(cmd), bashQuote(log))
		r := t.Shell.Run(ctx, run, 20*time.Second, nil)
		pid, _ = strconv.Atoi(strings.TrimSpace(lastLine(r.Output)))
		if r.ExitCode != 0 || pid <= 0 {
			return ToolOutput{IsErr: true, Text: "failed to start background job: " + strings.TrimSpace(r.Output) + r.Note, Display: Display{Summary: "falhou ao iniciar job"}}
		}
	}
	j := &Job{ID: id, PID: pid, Cmd: cmd, Log: log, Start: time.Now()}
	t.mu.Lock()
	t.jobs[id] = j
	t.mu.Unlock()
	time.Sleep(300 * time.Millisecond) // give it a moment so an immediate crash shows up
	_, running := j.status()
	st := "running"
	if !running {
		st = "already exited"
	}
	head, _ := j.readNew(2000)
	text := fmt.Sprintf("Started background job %s (pid %d, %s). Log: %s\nUse bash with job:%q action:\"output\" to read new output, action:\"kill\" to stop it.", id, pid, st, log, id)
	if strings.TrimSpace(head) != "" {
		text += "\n--- first output ---\n" + head
	}
	return ToolOutput{Text: text, IsErr: !running && strings.TrimSpace(head) == "", Display: Display{Summary: fmt.Sprintf("job %s · pid %d · %s", id, pid, st)}}
}

func lastLine(s string) string {
	lines := nonEmptyLines(s)
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// startDetached starts command as a background job outside the persistent
// shell (Windows, bwrap sandbox): same dialect, cwd and environment as the shell, stdin from
// the null device, stdout+stderr appended to log. Returns the leader's pid.
// env (optional) replaces the environment: the persistent shell's own
// exported variables, captured for jobs under a bwrap sandbox.
func (s *Shell) startDetached(command, log string, env []string) (int, error) {
	lf, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	argv := s.d.oneShotArgv(command)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = s.Cwd
	if cmd.Dir == "" {
		cmd.Dir = s.home
	}
	base := env
	if base == nil {
		base = s.Env
	}
	if base == nil {
		base = scrubEnv(os.Environ())
	}
	cmd.Env = append(base, "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "CI=1", "JAZZ_CLI=1", "NO_COLOR=1")
	cmd.Stdout, cmd.Stderr = lf, lf
	procPrepare(cmd, true)
	sbWrapCmd(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	procAttach(cmd)
	pid := cmd.Process.Pid
	safeGo(func() { _ = cmd.Wait() }) // reap; the job handle stays for status/kill
	return pid, nil
}

// exportedEnv captures the persistent shell's environment (exports, an
// activated venv) so a job started outside it still sees them. nil on error.
func (s *Shell) exportedEnv(ctx context.Context) []string {
	r := s.Run(ctx, "env -0", 10*time.Second, nil)
	if r.ExitCode != 0 || r.Reset {
		return nil
	}
	var env []string
	for _, kv := range strings.Split(r.Output, "\x00") {
		kv = strings.TrimPrefix(kv, "\n")
		if k, _, ok := strings.Cut(kv, "="); ok && k != "" && !strings.ContainsAny(k, " \n") {
			env = append(env, kv)
		}
	}
	if len(env) == 0 {
		return nil
	}
	return scrubEnv(env)
}

func (j *Job) status() (int, bool) {
	if !procAlive(j.PID) {
		return -1, false
	}
	return 0, true
}

func (j *Job) readNew(max int) (string, error) {
	f, err := os.Open(j.Log)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, _ := f.Stat()
	if fi.Size() < j.offset {
		j.offset = 0
	}
	if _, err := f.Seek(j.offset, io.SeekStart); err != nil {
		return "", err
	}
	b, _ := io.ReadAll(io.LimitReader(f, int64(max)))
	j.offset += int64(len(b))
	if fi.Size() > j.offset && len(b) >= max {
		return string(b) + fmt.Sprintf("\n[… %d more bytes pending; call output again]", fi.Size()-j.offset), nil
	}
	return string(b), nil
}

func (t *Toolbox) jobAction(id, action string) ToolOutput {
	t.mu.Lock()
	j := t.jobs[id]
	var all []*Job
	for _, x := range t.jobs {
		all = append(all, x)
	}
	t.mu.Unlock()
	if action == "" {
		action = "output"
	}
	if action == "list" {
		if len(all) == 0 {
			return ToolOutput{Text: "No background jobs.", Display: Display{Summary: "sem jobs"}}
		}
		sort.Slice(all, func(a, b int) bool { return all[a].Start.Before(all[b].Start) })
		var b strings.Builder
		for _, x := range all {
			_, running := x.status()
			st := "exited"
			if running {
				st = "running"
			}
			fmt.Fprintf(&b, "%s  pid %d  %s  %s  %s\n", x.ID, x.PID, st, time.Since(x.Start).Round(time.Second), oneLine(x.Cmd, 80))
		}
		return ToolOutput{Text: b.String(), Display: Display{Summary: fmt.Sprintf("%d jobs", len(all))}}
	}
	if j == nil {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Unknown job %q. Use action:\"list\" to see jobs.", id), Display: Display{Summary: "job desconhecido"}}
	}
	switch action {
	case "kill", "stop":
		procKillPID(j.PID, false)
		time.Sleep(500 * time.Millisecond)
		if _, running := j.status(); running {
			procKillPID(j.PID, true)
		}
		procForget(j.PID)
		out, _ := j.readNew(8000)
		t.mu.Lock()
		delete(t.jobs, id)
		t.mu.Unlock()
		text := fmt.Sprintf("Job %s (pid %d) killed.", id, j.PID)
		if strings.TrimSpace(out) != "" {
			text += "\n--- last output ---\n" + truncateHeadTail(out, 8000)
		}
		return ToolOutput{Text: text, Display: Display{Summary: "job " + id + " morto"}}
	default:
		out, err := j.readNew(maxToolBytes)
		if err != nil {
			return ToolOutput{IsErr: true, Text: "cannot read job log: " + err.Error()}
		}
		_, running := j.status()
		st := "still running"
		if !running {
			st = "exited"
		}
		text := fmt.Sprintf("Job %s (pid %d): %s, %s elapsed.", id, j.PID, st, time.Since(j.Start).Round(time.Second))
		if strings.TrimSpace(out) == "" {
			text += " No new output."
		} else {
			text += "\n--- new output ---\n" + redactSecrets(out)
		}
		lines := nonEmptyLines(out)
		prev := lines
		if len(prev) > 4 {
			prev = prev[len(prev)-4:]
		}
		return ToolOutput{Text: text, Display: Display{Summary: fmt.Sprintf("job %s · %s · %d linhas novas", id, st, len(lines)), Preview: prev}}
	}
}

// KillJobs stops every background job (on exit/clear).
func (t *Toolbox) KillJobs() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for id, j := range t.jobs {
		if _, running := j.status(); running {
			procKillPID(j.PID, false)
			n++
		}
		if runtime.GOOS == "windows" {
			procKillTree(j.PID) // the job object also holds what the leader left running
		}
		procForget(j.PID)
		delete(t.jobs, id)
	}
	return n
}

// RunningJobs counts live background jobs.
func (t *Toolbox) RunningJobs() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, j := range t.jobs {
		if _, running := j.status(); running {
			n++
		}
	}
	return n
}
