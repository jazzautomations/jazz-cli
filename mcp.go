package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const mcpProtocol = "2025-06-18"

// pfMCPMaxLine caps one JSON-RPC line from a stdio server (bug 60): a runaway
// server must not be able to run a 2 GB machine out of memory.
const pfMCPMaxLine = 32 << 20

type MCPServerConf struct {
	Type     string            `json:"type,omitempty"` // stdio (default when command set) | http
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	Timeout  int               `json:"timeout,omitempty"` // seconds per call (default 120)

	DisableResources bool `json:"disable_resources,omitempty"` // do not expose this server's resources (mcp_resource)
	DisablePrompts   bool `json:"disable_prompts,omitempty"`   // do not expose this server's prompts (mcp_prompt)

	EnabledTools  []string `json:"enabled_tools,omitempty"`  // globs; empty = all
	DisabledTools []string `json:"disabled_tools,omitempty"` // globs; applied after EnabledTools
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type MCPServer struct {
	Name    string
	Conf    MCPServerConf
	Project bool // defined in the project's .mcp.json (restricted env expansion)

	// Status, Err and Tools are written only under stMu; readers outside this
	// file must use State() (bugs 29, 43).
	Tools  []mcpTool
	cat    mcpCatalog // resources + prompts (mcp_res.go), also under stMu
	Status string     // connecting | ready | failed | disabled
	Err    error
	stMu   sync.Mutex

	root string

	mu      sync.Mutex // guards nextID, pending, cmd, stdin, session
	nextID  int64
	pending map[int64]chan rpcMsg
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	session string // http Mcp-Session-Id
	pid     int
	wch     chan []byte   // stdin writes go through one writer goroutine (bug 63)
	done    chan struct{} // closed when the stdio process has exited
	inited  atomic.Bool
	expand  func(string) string // env expansion for this server's config

	onListChanged func() // set by the manager: expose newly announced tools
}

// State returns a consistent snapshot of the server's status, last error and tool count.
func (s *MCPServer) State() (status string, err error, tools int) {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return s.Status, s.Err, len(s.Tools)
}

// ToolNames returns a snapshot of the server's raw tool names (for /mcp).
func (s *MCPServer) ToolNames() []string {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	out := make([]string, len(s.Tools))
	for i, t := range s.Tools {
		out[i] = t.Name
	}
	return out
}

func (s *MCPServer) setState(status string, err error) {
	s.stMu.Lock()
	s.Status, s.Err = status, err
	s.stMu.Unlock()
}

func (s *MCPServer) tools() []mcpTool {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return append([]mcpTool(nil), s.Tools...)
}

// alive reports whether the stdio process is still running (always true for HTTP).
func (s *MCPServer) alive() bool {
	s.mu.Lock()
	d := s.done
	s.mu.Unlock()
	if d == nil {
		return s.isHTTP()
	}
	select {
	case <-d:
		return false
	default:
		return true
	}
}

type pfMCPRoute struct {
	srv      *MCPServer
	name     string
	readOnly bool // the server annotated the tool readOnlyHint=true
}

type MCPManager struct {
	Servers []*MCPServer
	ready   chan struct{}

	mu       sync.Mutex
	built    bool
	specs    []ToolSpec                     // append-only once built (prompt cache)
	byTool   map[string]pfMCPRoute          // exposed name -> server tool
	exposed  map[*MCPServer]map[string]bool // raw tool names already in specs
	baseline map[string]string              // exposed name -> hash of its first description+schema (rug-pull, ASI04)
	meta     mcpMeta                        // mcp_resource / mcp_prompt (mcp_res.go)
	closed   bool
}

// mcpToolHash fingerprints a tool's description and input schema, so a server
// that mutates them after approval (a "rug-pull") is detected.
func mcpToolHash(t mcpTool) string {
	schema, _ := json.Marshal(t.InputSchema)
	return pfHash([]byte(t.Description + "\x00" + string(schema)))
}

// Poisoned reports whether the live description/schema of an exposed MCP tool
// differs from the one first shown (baseline). A changed description must be
// re-approved before the tool runs (agent.go permit).
func (m *MCPManager) Poisoned(tool string) (bool, string) {
	if m == nil {
		return false, ""
	}
	if pfMCPMeta(tool) {
		return m.metaPoisoned()
	}
	m.mu.Lock()
	want, ok := m.baseline[tool]
	route, routed := m.byTool[tool]
	m.mu.Unlock()
	if !ok || !routed {
		return false, ""
	}
	for _, t := range route.srv.tools() {
		if t.Name == route.name {
			if mcpToolHash(t) != want {
				return true, pfMCPDesc(t.Description)
			}
			return false, ""
		}
	}
	return false, ""
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandEnv resolves ${VAR} and ${VAR:-default} (used for user-level config).
func expandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		p := envRef.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(p[1]); ok && v != "" {
			return v
		}
		return p[3]
	})
}

// pfProjectEnvSafe are the variables a project .mcp.json may always expand.
var pfProjectEnvSafe = map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "PWD": true, "LANG": true, "SHELL": true}

// pfExpandProjectEnv is expandEnv for untrusted-origin (project) config: only
// variables in the safe set or in the user's global allowlist ("project_env" in
// the global mcp.json) are expanded, and never a provider key or anything that
// looks like a secret (bug 2). Other references expand to their default.
func pfExpandProjectEnv(allow map[string]bool) func(string) string {
	return func(s string) string {
		return envRef.ReplaceAllStringFunc(s, func(m string) string {
			p := envRef.FindStringSubmatch(m)
			name := p[1]
			ok := (pfProjectEnvSafe[name] || allow[name]) && !pfIsKeyEnv(name)
			if ok {
				if v, set := os.LookupEnv(name); set && v != "" {
					return v
				}
			}
			return p[3]
		})
	}
}

// pfMCPFile is one mcp.json file.
type pfMCPFile struct {
	MCPServers map[string]MCPServerConf `json:"mcpServers"`
	ProjectEnv []string                 `json:"project_env,omitempty"` // global file only
}

func pfReadMCPFile(p string) (pfMCPFile, bool) {
	var f pfMCPFile
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &f) != nil {
		return f, false
	}
	return f, true
}

// pfLoadMCP merges the global mcp.json with the project's .mcp.json. Project
// servers are used only when the file is trusted, and never replace a global
// server with the same name. project[name] marks project-defined servers.
func pfLoadMCP(root string) (conf map[string]MCPServerConf, project map[string]bool, allow map[string]bool) {
	conf, project, allow = map[string]MCPServerConf{}, map[string]bool{}, map[string]bool{}
	if g, ok := pfReadMCPFile(filepath.Join(configDir(), "mcp.json")); ok {
		for k, v := range g.MCPServers {
			conf[k] = v
		}
		for _, e := range g.ProjectEnv {
			allow[e] = true
		}
	}
	raw := pfProjectMCP(root)
	if raw == nil || !pfProjectMCPTrusted(root) {
		return
	}
	var f pfMCPFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	for k, v := range f.MCPServers {
		if _, dup := conf[k]; dup {
			continue // a project never overrides the user's own definition
		}
		conf[k] = v
		project[k] = true
	}
	return
}

func loadMCPConfig(root string) map[string]MCPServerConf {
	c, _, _ := pfLoadMCP(root)
	return c
}

// StartMCP connects to every configured server concurrently. Wait() blocks until done.
func StartMCP(root string) *MCPManager {
	m := &MCPManager{ready: make(chan struct{})}
	conf, project, allow := pfLoadMCP(root)
	var names []string
	for n := range conf {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := conf[n]
		s := &MCPServer{Name: n, Conf: c, Project: project[n], Status: "connecting", root: root, pending: map[int64]chan rpcMsg{}}
		if c.Disabled {
			s.Status = "disabled"
		}
		s.onListChanged = func() { m.addServerTools(s) }
		m.Servers = append(m.Servers, s)
	}
	safeGo(func() {
		var wg sync.WaitGroup
		for _, s := range m.Servers {
			if st, _, _ := s.State(); st == "disabled" {
				continue
			}
			wg.Add(1)
			safeGo(func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				expand := expandEnv
				if s.Project {
					expand = pfExpandProjectEnv(allow)
				}
				if err := s.connect(ctx, expand); err != nil {
					s.setState("failed", err)
					s.close()
					return
				}
				// the process may have died between connect() and here (bug 29)
				s.stMu.Lock()
				if s.alive() {
					s.Status, s.Err = "ready", nil
				} else {
					s.Status, s.Err = "failed", errors.New("servidor encerrou")
				}
				s.stMu.Unlock()
			})
		}
		wg.Wait()
		m.ensureBuilt()
		close(m.ready)
	})
	return m
}

func (m *MCPManager) Wait(timeout time.Duration) {
	if m == nil {
		return
	}
	select {
	case <-m.ready:
	case <-time.After(timeout):
	}
}

func (m *MCPManager) Done() bool {
	if m == nil {
		return true
	}
	select {
	case <-m.ready:
		return true
	default:
		return false
	}
}

var badName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func mcpToolName(server, tool string) string {
	n := "mcp__" + badName.ReplaceAllString(server, "_") + "__" + badName.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// pfToolAllowed applies enabled_tools / disabled_tools (globs over the raw name).
func pfToolAllowed(c MCPServerConf, name string) bool {
	match := func(globs []string) bool {
		for _, g := range globs {
			if ok, _ := path.Match(g, name); ok || g == name {
				return true
			}
		}
		return false
	}
	if len(c.EnabledTools) > 0 && !match(c.EnabledTools) {
		return false
	}
	return !match(c.DisabledTools)
}

// ensureBuilt computes the spec list once the servers are connected.
func (m *MCPManager) ensureBuilt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.built {
		return
	}
	m.built = true
	m.byTool = map[string]pfMCPRoute{}
	m.exposed = map[*MCPServer]map[string]bool{}
	for _, s := range m.Servers {
		if st, _, _ := s.State(); st == "ready" {
			m.addToolsLocked(s)
		}
	}
	m.addMetaLocked()
}

// addServerTools exposes tools a server announced later (list_changed); new
// tools are only appended so the earlier specs stay byte-identical.
func (m *MCPManager) addServerTools(s *MCPServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.built {
		m.addToolsLocked(s)
	}
}

func (m *MCPManager) addToolsLocked(s *MCPServer) {
	if m.exposed[s] == nil {
		m.exposed[s] = map[string]bool{}
	}
	for _, t := range s.tools() {
		if m.exposed[s][t.Name] || !pfToolAllowed(s.Conf, t.Name) {
			continue
		}
		m.exposed[s][t.Name] = true
		base := mcpToolName(s.Name, t.Name)
		name := base
		for i := 2; ; i++ { // unique names (bug 28)
			if _, dup := m.byTool[name]; !dup {
				break
			}
			suf := "_" + strconv.Itoa(i)
			b := base
			if len(b)+len(suf) > 64 {
				b = b[:64-len(suf)]
			}
			name = b + suf
		}
		m.byTool[name] = pfMCPRoute{s, t.Name, t.Annotations.ReadOnlyHint}
		if m.baseline == nil {
			m.baseline = map[string]string{}
		}
		m.baseline[name] = mcpToolHash(t) // the description the user first sees
		schema := map[string]any{}
		for k, v := range t.InputSchema {
			schema[k] = v
		}
		if _, ok := schema["type"]; !ok {
			schema["type"] = "object"
		}
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = map[string]any{}
		}
		desc := pfMCPDesc(t.Description)
		if desc == "" {
			desc = t.Name
		}
		m.specs = append(m.specs, ToolSpec{Name: name, Description: "[MCP " + s.Name + "] " + desc, Params: schema})
	}
}

// Specs returns tool specs for every ready server (call after Wait). The list
// is fixed once built and only grows at the end, keeping the prompt cache.
func (m *MCPManager) Specs() []ToolSpec {
	if m == nil || !m.Done() {
		return nil
	}
	m.ensureBuilt()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.specs) == 0 {
		return nil
	}
	return append([]ToolSpec(nil), m.specs...)
}

func (m *MCPManager) route(tool string) (pfMCPRoute, bool) {
	if m == nil || !m.Done() {
		return pfMCPRoute{}, false
	}
	m.ensureBuilt()
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byTool[tool]
	return r, ok
}

// ReadOnly reports whether the server annotated an exposed MCP tool as
// read-only (MCP readOnlyHint). Unknown tools are not read-only.
func (m *MCPManager) ReadOnly(tool string) bool {
	if pfMCPMeta(tool) {
		return m.metaHas(tool) // reading a resource or a prompt changes nothing
	}
	r, ok := m.route(tool)
	return ok && r.readOnly
}

// Has reports whether tool is an exposed MCP tool whose server is usable.
func (m *MCPManager) Has(tool string) bool {
	if pfMCPMeta(tool) {
		return m.metaHas(tool)
	}
	r, ok := m.route(tool)
	if !ok {
		return false
	}
	st, _, _ := r.srv.State()
	return st == "ready"
}

func (m *MCPManager) Call(ctx context.Context, tool, rawArgs string) ToolOutput {
	if pfMCPMeta(tool) {
		return m.callMeta(ctx, tool, rawArgs)
	}
	e, ok := m.route(tool)
	if !ok {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("MCP tool %q not found", tool)}
	}
	if st, serr, _ := e.srv.State(); st != "ready" {
		msg := st
		if serr != nil {
			msg = serr.Error()
		}
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("MCP %s/%s is not available: %s", e.srv.Name, e.name, msg), Display: Display{Summary: "servidor indisponível"}}
	}
	args, _, err := parseArgs(rawArgs)
	if err != nil {
		return validationErr(tool, err.Error(), rawArgs)
	}
	to := e.srv.Conf.Timeout
	if to <= 0 {
		to = 120
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(to)*time.Second)
	defer cancel()
	t0 := time.Now()
	res, err := e.srv.request(cctx, "tools/call", map[string]any{"name": e.name, "arguments": args})
	if err != nil {
		return ToolOutput{IsErr: true, Text: redactSecrets(fmt.Sprintf("MCP %s/%s failed: %v", e.srv.Name, e.name, err)), Display: Display{Summary: "falhou: " + firstSentence(err.Error())}}
	}
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	_ = json.Unmarshal(res, &r)
	var b strings.Builder
	var img *ImagePart
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			b.WriteString(c.Text)
		case "resource":
			if c.Resource.Text != "" {
				b.WriteString(c.Resource.Text)
			} else {
				b.WriteString("[resource " + c.Resource.URI + "]")
			}
		case "resource_link":
			b.WriteString("[resource link]")
		case "image":
			if img == nil && c.Data != "" && strings.HasPrefix(c.MimeType, "image/") {
				img = &ImagePart{Mime: c.MimeType, Data: c.Data}
				fmt.Fprintf(&b, "[image attached (%s)]", c.MimeType)
			} else {
				fmt.Fprintf(&b, "[image content omitted (%s)]", c.MimeType)
			}
		default:
			fmt.Fprintf(&b, "[%s content omitted (%s)]", c.Type, c.MimeType)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 && len(r.Structured) > 0 {
		b.Write(r.Structured)
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		text = "(no content)"
	}
	lines := nonEmptyLines(text)
	prev := lines
	if len(prev) > 3 {
		prev = prev[:3]
	}
	return ToolOutput{Text: truncateHeadTail(redactSecrets(text), maxToolBytes), IsErr: r.IsError, Image: img,
		Display: Display{Summary: fmt.Sprintf("%d linhas · %s", len(lines), time.Since(t0).Round(100*time.Millisecond)), Preview: prev}}
}

// Close stops every server synchronously (bug 59): SIGTERM to each process
// group, wait up to 2 s in total, then SIGKILL whatever is still running.
func (m *MCPManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	for _, s := range m.Servers {
		s.terminate()
	}
	deadline := time.Now().Add(2 * time.Second)
	for _, s := range m.Servers {
		s.waitOrKill(deadline)
	}
}

// pfKillAll SIGKILLs every stdio server at once (crash path; no waiting).
func (m *MCPManager) pfKillAll() {
	if m == nil {
		return
	}
	for _, s := range m.Servers {
		s.mu.Lock()
		pid, d := s.pid, s.done
		s.mu.Unlock()
		if pid > 0 && d != nil && !pfClosed(d) {
			procKillTree(pid)
		}
	}
}

func pfClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// ---------- per-server transport ----------

func (s *MCPServer) isHTTP() bool {
	return s.Conf.URL != "" && s.Conf.Command == "" || s.Conf.Type == "http" || s.Conf.Type == "streamable-http"
}

func (s *MCPServer) connect(ctx context.Context, expand func(string) string) error {
	if s.Conf.Type == "sse" {
		return errors.New("transporte SSE legado não suportado; use type \"http\" (Streamable HTTP)")
	}
	if expand == nil {
		expand = expandEnv
	}
	s.mu.Lock()
	s.expand = expand
	s.mu.Unlock()
	if !s.isHTTP() {
		if err := s.startStdio(expand); err != nil {
			return err
		}
	}
	ires, err := s.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      map[string]any{"name": "jazz-cli", "version": version},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	s.inited.Store(true)
	_ = s.notify(ctx, "notifications/initialized", nil)
	tools, err := s.listTools(ctx)
	if err != nil {
		return err
	}
	s.stMu.Lock()
	s.Tools = tools
	s.stMu.Unlock()
	s.parseCaps(ires)
	s.loadCatalog(ctx) // resources and prompts: best effort, never fails the connection
	return nil
}

func (s *MCPServer) listTools(ctx context.Context) ([]mcpTool, error) {
	var all []mcpTool
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := s.request(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		var tl struct {
			Tools      []mcpTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &tl); err != nil {
			return nil, fmt.Errorf("tools/list inválido: %w", err)
		}
		all = append(all, tl.Tools...)
		if tl.NextCursor == "" {
			break
		}
		cursor = tl.NextCursor
	}
	return all, nil
}

// refreshTools handles notifications/tools/list_changed (G18).
func (s *MCPServer) refreshTools() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tools, err := s.listTools(ctx)
	if err != nil {
		dbg("mcp", map[string]any{"server": s.Name, "event": "list_changed refresh failed", "err": err.Error()})
		return
	}
	s.stMu.Lock()
	s.Tools = tools
	s.stMu.Unlock()
	if s.onListChanged != nil {
		s.onListChanged()
	}
}

func (s *MCPServer) startStdio(expand func(string) string) error {
	name := expand(s.Conf.Command)
	var args []string
	for _, a := range s.Conf.Args {
		args = append(args, expand(a))
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = s.root
	if s.Conf.Cwd != "" {
		cmd.Dir = expand(s.Conf.Cwd)
	}
	// provider keys never reach MCP servers; a server gets only what its own
	// env block passes explicitly (bug 12)
	cmd.Env = scrubEnv(os.Environ())
	for k, v := range s.Conf.Env {
		cmd.Env = append(cmd.Env, k+"="+expand(v))
	}
	procPrepare(cmd, false)
	sbWrapCmd(cmd)
	if err := os.MkdirAll(dataDir(), 0o700); err == nil {
		if lf, err := os.OpenFile(filepath.Join(dataDir(), "mcp-"+badName.ReplaceAllString(s.Name, "_")+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			cmd.Stderr = lf
		}
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// Our own pipe (not StdoutPipe): Wait must not close the read end, so a
	// reply written right before the server exits is still read.
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("não consegui iniciar %q: %w", name, err)
	}
	procAttach(cmd)
	pw.Close()
	done := make(chan struct{})
	wch := make(chan []byte, 64)
	s.mu.Lock()
	s.cmd, s.stdin, s.pid, s.done, s.wch = cmd, in, cmd.Process.Pid, done, wch
	s.mu.Unlock()
	readDone := make(chan struct{})
	safeGo(func() {
		defer close(readDone)
		defer pr.Close()
		s.readLoop(pr)
	})
	safeGo(func() { // single writer: request() never blocks on the pipe while holding a lock (bug 63)
		for {
			select {
			case b := <-wch:
				if _, err := in.Write(b); err != nil {
					dbg("mcp", map[string]any{"server": s.Name, "event": "write failed", "err": err.Error()})
				}
			case <-done:
				return
			}
		}
	})
	safeGo(func() {
		_ = cmd.Wait()
		if f, ok := cmd.Stderr.(*os.File); ok {
			f.Close()
		}
		// drain what the server wrote before exiting (a grandchild may keep the
		// pipe open, so do not wait for EOF forever)
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
			pr.Close()
		}
		s.mu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.cmd = nil
		s.mu.Unlock()
		procForget(cmd.Process.Pid)
		close(done)
		s.stMu.Lock()
		if s.Status == "ready" || s.Status == "connecting" {
			s.Status, s.Err = "failed", errors.New("servidor encerrou")
		}
		s.stMu.Unlock()
	})
	return nil
}

var pfRPCIDRe = regexp.MustCompile(`"id"\s*:\s*(\d+)`)

func (s *MCPServer) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	skipping := false
	var head []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if !skipping {
			if len(buf)+len(chunk) > pfMCPMaxLine {
				skipping = true
				head = append([]byte(nil), buf[:min(len(buf), 4096)]...)
				buf = buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if skipping {
			s.dropOversized(head)
			skipping, head = false, nil
		} else if len(bytes.TrimSpace(buf)) > 0 {
			var msg rpcMsg
			if json.Unmarshal(buf, &msg) == nil {
				s.dispatch(msg, len(buf))
			}
		}
		buf = buf[:0]
		if err != nil {
			return
		}
	}
}

// dropOversized fails the request an oversized line answered (if its id is visible).
func (s *MCPServer) dropOversized(head []byte) {
	dbg("mcp", map[string]any{"server": s.Name, "event": "line over limit dropped", "limit": pfMCPMaxLine})
	m := pfRPCIDRe.FindSubmatch(head)
	if m == nil {
		return
	}
	id, _ := strconv.ParseInt(string(m[1]), 10, 64)
	s.mu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if ch != nil {
		msg := rpcMsg{}
		msg.Error = &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{-32000, fmt.Sprintf("resposta maior que %d MB descartada", pfMCPMaxLine>>20)}
		ch <- msg
	}
}

func (s *MCPServer) dispatch(msg rpcMsg, size int) {
	if debugOn {
		dbg("mcp", map[string]any{"server": s.Name, "dir": "recv", "method": msg.Method, "id": string(msg.ID), "bytes": size, "error": msg.Error})
	}
	if msg.Method != "" { // request or notification from the server
		if len(msg.ID) == 0 {
			s.onNotification(msg.Method, msg.Params)
			return
		}
		var result any = map[string]any{}
		var rerr map[string]any
		switch msg.Method {
		case "ping":
		case "roots/list":
			result = map[string]any{"roots": []map[string]any{{"uri": "file://" + s.root, "name": filepath.Base(s.root)}}}
		default:
			result = nil
			rerr = map[string]any{"code": -32601, "message": "method not supported by jazz-cli"}
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
		if rerr != nil {
			reply["error"] = rerr
		} else {
			reply["result"] = result
		}
		b, _ := json.Marshal(reply)
		safeGo(func() { // never block the reader on a write
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = s.send(ctx, b)
		})
		return
	}
	var id int64
	if json.Unmarshal(msg.ID, &id) != nil {
		return
	}
	s.mu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

// send queues one line for the stdio writer goroutine.
func (s *MCPServer) send(ctx context.Context, b []byte) error {
	s.mu.Lock()
	wch, done := s.wch, s.done
	s.mu.Unlock()
	if wch == nil || done == nil || pfClosed(done) {
		return errors.New("servidor não está rodando")
	}
	line := append(append([]byte(nil), b...), '\n')
	select {
	case wch <- line:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("servidor encerrou")
	}
}

func (s *MCPServer) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	if debugOn {
		dbg("mcp", map[string]any{"server": s.Name, "dir": "send", "method": method, "id": id, "params": params})
	}
	if s.isHTTP() {
		res, err := s.httpRequest(ctx, body, id)
		if err != nil && ctx.Err() != nil && s.inited.Load() { // bug 52: a dropped connection is not a cancellation
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.notify(c, "notifications/cancelled", map[string]any{"requestId": id, "reason": "cancelled by user"})
			cancel()
		}
		return res, err
	}
	ch := make(chan rpcMsg, 1)
	s.mu.Lock()
	if s.wch == nil || s.done == nil || pfClosed(s.done) {
		s.mu.Unlock()
		return nil, errors.New("servidor não está rodando")
	}
	s.pending[id] = ch
	s.mu.Unlock()
	b, _ := json.Marshal(body)
	if err := s.send(ctx, b); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		if s.inited.Load() {
			c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.notify(c, "notifications/cancelled", map[string]any{"requestId": id, "reason": "cancelled by user"})
			cancel()
		}
		return nil, ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return nil, errors.New("servidor encerrou")
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s (code %d)", msg.Error.Message, msg.Error.Code)
		}
		return msg.Result, nil
	}
}

func (s *MCPServer) notify(ctx context.Context, method string, params any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		body["params"] = params
	}
	if s.isHTTP() {
		_, err := s.httpRequest(ctx, body, -1)
		return err
	}
	b, _ := json.Marshal(body)
	return s.send(ctx, b)
}

func (s *MCPServer) expandFn() func(string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expand != nil {
		return s.expand
	}
	if s.Project {
		return pfExpandProjectEnv(nil)
	}
	return expandEnv
}

func (s *MCPServer) httpRequest(ctx context.Context, body map[string]any, id int64) (json.RawMessage, error) {
	expand := s.expandFn()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", expand(s.Conf.URL), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", userAgent)
	if s.inited.Load() {
		req.Header.Set("MCP-Protocol-Version", mcpProtocol)
	}
	s.mu.Lock()
	if s.session != "" {
		req.Header.Set("Mcp-Session-Id", s.session)
	}
	s.mu.Unlock()
	for k, v := range s.Conf.Headers {
		req.Header.Set(k, expand(v))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.mu.Lock()
		s.session = sid
		s.mu.Unlock()
	}
	if debugOn {
		dbg("mcp", map[string]any{"server": s.Name, "dir": "http", "id": id, "status": resp.StatusCode, "content_type": resp.Header.Get("Content-Type")})
	}
	if id < 0 { // notification
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, nil
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimBody(raw))
	}
	handle := func(raw []byte) (json.RawMessage, bool, error) {
		var msg rpcMsg
		if json.Unmarshal(raw, &msg) != nil {
			return nil, false, nil
		}
		var got int64
		if json.Unmarshal(msg.ID, &got) != nil || got != id || msg.Method != "" {
			if msg.Method != "" && len(msg.ID) == 0 {
				s.onNotification(msg.Method, msg.Params)
			}
			return nil, false, nil
		}
		if msg.Error != nil {
			return nil, true, fmt.Errorf("%s (code %d)", msg.Error.Message, msg.Error.Code)
		}
		return msg.Result, true, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var result json.RawMessage
		var rerr error
		found := false
		errStop := errors.New("stop")
		err := readSSE(resp.Body, func(bool) {}, func(_, data string) error {
			r, ok, e := handle([]byte(data))
			if ok {
				result, rerr, found = r, e, true
				return errStop
			}
			return nil
		})
		if found {
			return result, rerr
		}
		if err != nil && err != errStop {
			return nil, err
		}
		return nil, errors.New("stream terminou sem resposta")
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, pfMCPMaxLine))
	r, ok, e := handle(raw)
	if !ok && e == nil {
		return nil, fmt.Errorf("resposta inesperada: %s", trimBody(raw))
	}
	return r, e
}

// terminate closes stdin and sends SIGTERM to the server's process group.
func (s *MCPServer) terminate() {
	s.mu.Lock()
	pid, d, in := s.pid, s.done, s.stdin
	s.stdin = nil
	s.mu.Unlock()
	if in != nil {
		in.Close()
	}
	if pid > 0 && d != nil && !pfClosed(d) {
		procTermTree(pid)
	}
}

// waitOrKill waits for the process to exit until deadline, then SIGKILLs its
// group. The kill is sent only while the process is known to be alive, so a
// reused pgid is never hit.
func (s *MCPServer) waitOrKill(deadline time.Time) {
	s.mu.Lock()
	pid, d := s.pid, s.done
	s.mu.Unlock()
	if pid <= 0 || d == nil {
		return
	}
	select {
	case <-d:
		return
	case <-time.After(time.Until(deadline)):
	}
	if !pfClosed(d) {
		procKillTree(pid)
		select {
		case <-d:
		case <-time.After(time.Second):
		}
	}
}

// close stops one server (after a failed connect) and waits briefly.
func (s *MCPServer) close() {
	s.terminate()
	s.waitOrKill(time.Now().Add(2 * time.Second))
}

// pfMCPDesc strips control characters from a server-supplied tool description
// and caps it at about 300 characters (bug 31: descriptions are untrusted text
// that goes into every request).
func pfMCPDesc(d string) string {
	d = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, d)
	d = strings.Join(strings.Fields(d), " ")
	if r := []rune(d); len(r) > 300 {
		d = string(r[:299]) + "…"
	}
	return d
}
