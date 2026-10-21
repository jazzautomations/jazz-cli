//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// ============================================================================
// Fake MCP stdio server (runs inside the test binary when JAZZ_FAKE_MCP=1).
//
// An init() hook is used instead of TestMain so this file cannot collide with a
// TestMain defined by another test file of the package; it runs even earlier,
// before the testing flags are parsed, and exits the process.
// ============================================================================

func init() {
	if os.Getenv("JAZZ_FAKE_MCP") == "1" {
		mcpUiFakeServerMain(os.Getenv("JAZZ_FAKE_MODE"))
		os.Exit(0)
	}
}

type mcpUiFake struct {
	mode        string
	wmu         sync.Mutex
	mu          sync.Mutex
	initParams  map[string]any
	initialized bool
	pingOK      bool
	rootsURI    string
	unknownErr  int
	cancelled   []any
	slow        map[string]chan struct{}
	waiters     map[string]chan rpcMsg
	srvReqDone  chan struct{}
	extraTool   bool
}

func (f *mcpUiFake) send(v any) {
	b, _ := json.Marshal(v)
	f.wmu.Lock()
	os.Stdout.Write(append(b, '\n'))
	f.wmu.Unlock()
}

func (f *mcpUiFake) reply(id json.RawMessage, result any) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *mcpUiFake) replyErr(id json.RawMessage, code int, msg string) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

// serverRequest sends a server->client request and waits for the client's reply.
func (f *mcpUiFake) serverRequest(id, method string) (rpcMsg, bool) {
	ch := make(chan rpcMsg, 1)
	idRaw, _ := json.Marshal(id)
	f.mu.Lock()
	f.waiters[string(idRaw)] = ch
	f.mu.Unlock()
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method})
	select {
	case m := <-ch:
		return m, true
	case <-time.After(10 * time.Second):
		return rpcMsg{}, false
	}
}

func textContent(s string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}
}

func mcpUiFakeServerMain(mode string) {
	if mode == "ignore_term" {
		signal.Ignore(syscall.SIGTERM)
	}
	f := &mcpUiFake{mode: mode, slow: map[string]chan struct{}{}, waiters: map[string]chan rpcMsg{}, srvReqDone: make(chan struct{})}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Method == "" { // reply to one of our requests
			f.mu.Lock()
			ch := f.waiters[string(msg.ID)]
			delete(f.waiters, string(msg.ID))
			f.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if len(msg.ID) == 0 { // notification
			switch msg.Method {
			case "notifications/initialized":
				f.mu.Lock()
				f.initialized = true
				f.mu.Unlock()
				go func() {
					defer close(f.srvReqDone)
					if r, ok := f.serverRequest("srv-ping", "ping"); ok && r.Error == nil && string(r.Result) == "{}" {
						f.mu.Lock()
						f.pingOK = true
						f.mu.Unlock()
					}
					if r, ok := f.serverRequest("srv-roots", "roots/list"); ok && r.Error == nil {
						var rl struct {
							Roots []struct{ URI string } `json:"roots"`
						}
						_ = json.Unmarshal(r.Result, &rl)
						if len(rl.Roots) == 1 {
							f.mu.Lock()
							f.rootsURI = rl.Roots[0].URI
							f.mu.Unlock()
						}
					}
					if r, ok := f.serverRequest("srv-unknown", "sampling/createMessage"); ok && r.Error != nil {
						f.mu.Lock()
						f.unknownErr = r.Error.Code
						f.mu.Unlock()
					}
					// a notification from the server must not be answered
					f.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
				}()
			case "notifications/cancelled":
				var p struct {
					RequestID any `json:"requestId"`
				}
				_ = json.Unmarshal(msg.Params, &p)
				f.mu.Lock()
				f.cancelled = append(f.cancelled, p.RequestID)
				key, _ := json.Marshal(p.RequestID)
				if ch := f.slow[string(key)]; ch != nil {
					close(ch)
					delete(f.slow, string(key))
				}
				f.mu.Unlock()
			}
			continue
		}
		go f.handle(msg)
	}
}

func (f *mcpUiFake) handle(msg rpcMsg) {
	switch msg.Method {
	case "initialize":
		switch f.mode {
		case "crash_init":
			os.Exit(3)
		case "rpcerr_init":
			f.replyErr(msg.ID, -32603, "init exploded")
			return
		}
		var p map[string]any
		_ = json.Unmarshal(msg.Params, &p)
		f.mu.Lock()
		f.initParams = p
		f.mu.Unlock()
		f.reply(msg.ID, map[string]any{"protocolVersion": mcpProtocol, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1"}})
	case "tools/list":
		if f.mode == "bad_list" {
			f.reply(msg.ID, "not-an-object")
			return
		}
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		switch p.Cursor {
		case "":
			f.reply(msg.ID, map[string]any{
				"tools": []any{
					map[string]any{"name": "echo", "description": "Echo text back. Second sentence.", "annotations": map[string]any{"readOnlyHint": true}, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}},
					map[string]any{"name": "fail", "description": ""},
					map[string]any{"name": "weird.tool name", "description": "has a weird name", "inputSchema": map[string]any{"properties": map[string]any{}}},
				},
				"nextCursor": "page-2",
			})
		case "page-2":
			names := []string{"slow", "status", "mixed", "structured", "empty", "big", "crash", "rpcerr", "bye", "addtool", "env"}
			var tl []any
			for _, n := range names {
				tl = append(tl, map[string]any{"name": n, "description": "tool " + n, "inputSchema": map[string]any{"type": "object"}})
			}
			f.mu.Lock()
			if f.extraTool {
				tl = append([]any{map[string]any{"name": "added", "description": "added later"}}, tl...)
			}
			f.mu.Unlock()
			f.reply(msg.ID, map[string]any{"tools": tl})
		default:
			f.replyErr(msg.ID, -32602, "bad cursor "+p.Cursor)
		}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		f.call(msg.ID, p.Name, p.Arguments)
	default:
		f.replyErr(msg.ID, -32601, "unknown method "+msg.Method)
	}
}

func (f *mcpUiFake) call(id json.RawMessage, name string, args map[string]any) {
	switch name {
	case "echo":
		f.reply(id, textContent(fmt.Sprintf("echo: %v", args["text"])))
	case "weird.tool name":
		f.reply(id, textContent("called "+name))
	case "fail":
		r := textContent("boom")
		r["isError"] = true
		f.reply(id, r)
	case "slow":
		ch := make(chan struct{})
		f.mu.Lock()
		f.slow[string(id)] = ch
		f.mu.Unlock()
		select {
		case <-ch: // cancelled: per spec, do not answer
		case <-time.After(30 * time.Second):
			f.reply(id, textContent("slow done"))
		}
	case "status":
		select {
		case <-f.srvReqDone:
		case <-time.After(10 * time.Second):
		}
		f.mu.Lock()
		ci, _ := f.initParams["clientInfo"].(map[string]any)
		caps, _ := f.initParams["capabilities"].(map[string]any)
		_, hasRoots := caps["roots"]
		st := map[string]any{
			"initialized": f.initialized, "ping": f.pingOK, "rootsURI": f.rootsURI, "unknownErr": f.unknownErr,
			"cancelled": f.cancelled, "protocol": f.initParams["protocolVersion"], "client": ci["name"], "rootsCap": hasRoots,
			"env": os.Getenv("MCPUI_EXPANDED"), "cwd": func() string { d, _ := os.Getwd(); return d }(),
		}
		f.mu.Unlock()
		b, _ := json.Marshal(st)
		f.reply(id, textContent(string(b)))
	case "mixed":
		f.reply(id, map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "first"},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///a.txt", "text": "resource body"}},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///b.bin", "blob": "AAAA"}},
			map[string]any{"type": "resource_link", "uri": "file:///c.txt", "name": "c"},
			map[string]any{"type": "image", "mimeType": "image/png", "data": "iVBORw0KGgo="},
		}})
	case "structured":
		f.reply(id, map[string]any{"content": []any{}, "structuredContent": map[string]any{"answer": 42}})
	case "empty":
		f.reply(id, map[string]any{"content": []any{}})
	case "big":
		var b strings.Builder
		for i := 0; b.Len() < 100<<10; i++ {
			fmt.Fprintf(&b, "line %05d %s\n", i, strings.Repeat("x", 60))
		}
		b.WriteString("THE-END")
		f.reply(id, textContent(b.String()))
	case "crash":
		os.Exit(7)
	case "rpcerr":
		f.replyErr(id, -32000, "kaput")
	case "bye":
		f.reply(id, textContent("goodbye"))
		os.Exit(0)
	case "addtool":
		f.mu.Lock()
		f.extraTool = true
		f.mu.Unlock()
		f.reply(id, textContent("ok"))
		f.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
	case "added":
		f.reply(id, textContent("added ok"))
	case "env":
		f.reply(id, textContent(os.Getenv("OPENAI_API_KEY")+"|"+os.Getenv("MCPUI_PROJ")+"|"+os.Getenv("MCPUI_SAFE")))
	default:
		f.replyErr(id, -32602, "unknown tool "+name)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mcpUiFakeConf(t *testing.T, mode string) MCPServerConf {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return MCPServerConf{Command: exe, Args: []string{"-test.run=^$"}, Env: map[string]string{"JAZZ_FAKE_MCP": "1", "JAZZ_FAKE_MODE": mode}}
}

func mcpUiWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// mcpUiStart writes the servers into $JAZZ_HOME/mcp.json and starts the manager.
func mcpUiStart(t *testing.T, servers map[string]MCPServerConf) (*MCPManager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	mcpUiWriteJSON(t, filepath.Join(home, "mcp.json"), map[string]any{"mcpServers": servers})
	root := t.TempDir()
	m := StartMCP(root)
	t.Cleanup(m.Close)
	m.Wait(60 * time.Second)
	if !m.Done() {
		t.Fatal("MCP manager not ready after 60s")
	}
	return m, root
}

func mcpUiSt(s *MCPServer) string { st, _, _ := s.State(); return st }
func mcpUiErr(s *MCPServer) error { _, err, _ := s.State(); return err }
func mcpUiN(s *MCPServer) int     { _, _, n := s.State(); return n }

func mcpUiServer(t *testing.T, m *MCPManager, name string) *MCPServer {
	t.Helper()
	for _, s := range m.Servers {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %q not found", name)
	return nil
}

func mcpUiStatus(t *testing.T, m *MCPManager, tool string) map[string]any {
	t.Helper()
	out := m.Call(context.Background(), tool, "{}")
	if out.IsErr {
		t.Fatalf("status call failed: %s", out.Text)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(out.Text), &st); err != nil {
		t.Fatalf("status not JSON: %v: %q", err, out.Text)
	}
	return st
}

// ============================================================================
// MCP: pure helpers
// ============================================================================

func TestMcpUiToolNameSanitization(t *testing.T) {
	long := strings.Repeat("a", 100)
	cases := []struct{ server, tool, want string }{
		{"fs", "read_file", "mcp__fs__read_file"},
		{"my server", "get.thing", "mcp__my_server__get_thing"},
		{"git-hub", "list-prs", "mcp__git-hub__list-prs"},
		{"café", "naïve/tool", "mcp__caf___na_ve_tool"},
		{"a:b", "x y\tz", "mcp__a_b__x_y_z"},
		{"s", long, ("mcp__s__" + long)[:64]},
		{"", "", "mcp____"},
	}
	for _, c := range cases {
		got := mcpToolName(c.server, c.tool)
		if got != c.want {
			t.Errorf("mcpToolName(%q,%q) = %q, want %q", c.server, c.tool, got, c.want)
		}
		if len(got) > 64 {
			t.Errorf("name %q longer than 64", got)
		}
		if badName.MatchString(strings.ReplaceAll(got, "__", "")) {
			t.Errorf("name %q still has invalid chars", got)
		}
	}
}

func TestMcpUiToolNameCollisionsAreUnique(t *testing.T) {
	long := strings.Repeat("x", 70)
	m := &MCPManager{ready: make(chan struct{})}
	m.Servers = []*MCPServer{{Name: "srv", Status: "ready", Tools: []mcpTool{{Name: "get.user"}, {Name: "get_user"}, {Name: long + "A"}, {Name: long + "B"}}}}
	close(m.ready)
	seen := map[string]bool{}
	specs := m.Specs()
	if len(specs) != 4 {
		t.Fatalf("want 4 specs, got %d", len(specs))
	}
	for _, s := range specs {
		if seen[s.Name] {
			t.Errorf("duplicate tool name %q in Specs()", s.Name)
		}
		if len(s.Name) > 64 {
			t.Errorf("name too long: %q", s.Name)
		}
		if !m.Has(s.Name) {
			t.Errorf("Has(%q) = false", s.Name)
		}
		seen[s.Name] = true
	}
	if !seen["mcp__srv__get_user"] || !seen["mcp__srv__get_user_2"] {
		t.Errorf("want get_user and get_user_2, got %v", seen)
	}
}

func TestMcpUiExpandEnv(t *testing.T) {
	t.Setenv("MCPUI_SET", "value")
	t.Setenv("MCPUI_EMPTY", "")
	os.Unsetenv("MCPUI_UNSET")
	cases := []struct{ in, want string }{
		{"${MCPUI_SET}", "value"},
		{"pre-${MCPUI_SET}-post", "pre-value-post"},
		{"${MCPUI_UNSET}", ""},
		{"${MCPUI_UNSET:-fallback}", "fallback"},
		{"${MCPUI_EMPTY:-fallback}", "fallback"},
		{"${MCPUI_SET:-fallback}", "value"},
		{"${MCPUI_UNSET:-}", ""},
		{"$MCPUI_SET", "$MCPUI_SET"},
		{"${1BAD}", "${1BAD}"},
		{"${MCPUI_SET}${MCPUI_UNSET:-x}", "valuex"},
		{"no refs", "no refs"},
	}
	for _, c := range cases {
		if got := expandEnv(c.in); got != c.want {
			t.Errorf("expandEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMcpUiLoadConfigMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	root := t.TempDir()
	mcpUiWriteJSON(t, filepath.Join(home, "mcp.json"), map[string]any{"mcpServers": map[string]any{
		"a": map[string]any{"command": "global-a"},
		"b": map[string]any{"command": "global-b"},
	}})
	mcpUiWriteJSON(t, filepath.Join(root, ".mcp.json"), map[string]any{"mcpServers": map[string]any{
		"b": map[string]any{"url": "http://x", "headers": map[string]string{"K": "V"}, "timeout": 5},
		"c": map[string]any{"command": "proj-c", "disabled": true},
	}})
	// untrusted project file: only the global servers load (bug 2)
	got := loadMCPConfig(root)
	if len(got) != 2 || got["b"].Command != "global-b" {
		t.Fatalf("untrusted project .mcp.json must be ignored: %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err := Trust(root, "mcp", raw); err != nil {
		t.Fatal(err)
	}
	got = loadMCPConfig(root)
	if len(got) != 3 {
		t.Fatalf("want 3 servers, got %d: %+v", len(got), got)
	}
	if got["a"].Command != "global-a" {
		t.Errorf("a: %+v", got["a"])
	}
	if got["b"].Command != "global-b" || got["b"].URL != "" {
		t.Errorf("project must never override a global server: %+v", got["b"])
	}
	if !got["c"].Disabled {
		t.Errorf("c should be disabled: %+v", got["c"])
	}
	// any change to the file needs trust again
	mcpUiWriteJSON(t, filepath.Join(root, ".mcp.json"), map[string]any{"mcpServers": map[string]any{
		"c": map[string]any{"command": "proj-c-changed"},
	}})
	if got := loadMCPConfig(root); len(got) != 2 {
		t.Errorf("changed project file must need trust again: %+v", got)
	}
	Trust(root, "mcp", []byte("{not json"))

	// invalid project file is ignored, global still loads
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{not json"), 0o644)
	got = loadMCPConfig(root)
	if len(got) != 2 || got["b"].Command != "global-b" {
		t.Errorf("invalid project file must be ignored: %+v", got)
	}

	// nothing configured
	t.Setenv("JAZZ_HOME", t.TempDir())
	if got := loadMCPConfig(t.TempDir()); len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

func TestMcpUiIsHTTP(t *testing.T) {
	cases := []struct {
		c    MCPServerConf
		want bool
	}{
		{MCPServerConf{Command: "x"}, false},
		{MCPServerConf{URL: "http://x"}, true},
		{MCPServerConf{Type: "http", URL: "http://x"}, true},
		{MCPServerConf{Type: "streamable-http", URL: "http://x"}, true},
		{MCPServerConf{Command: "x", URL: "http://x"}, false},
		{MCPServerConf{Type: "http", Command: "x", URL: "http://x"}, true},
	}
	for i, c := range cases {
		s := &MCPServer{Conf: c.c}
		if got := s.isHTTP(); got != c.want {
			t.Errorf("case %d %+v: isHTTP=%v want %v", i, c.c, got, c.want)
		}
	}
}

func TestMcpUiSpecsDefaults(t *testing.T) {
	m := &MCPManager{ready: make(chan struct{})}
	m.Servers = []*MCPServer{
		{Name: "ok", Status: "ready", Tools: []mcpTool{
			{Name: "nil_schema", Description: "Does a thing."},
			{Name: "no_props", Description: "", InputSchema: map[string]any{"type": "object"}},
			{Name: "full", Description: "Full", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []any{"q"}}},
			{Name: "no_type", Description: "nt", InputSchema: map[string]any{"properties": map[string]any{"a": map[string]any{}}}},
		}},
		{Name: "down", Status: "failed", Tools: []mcpTool{{Name: "hidden"}}},
		{Name: "off", Status: "disabled", Tools: []mcpTool{{Name: "hidden2"}}},
		{Name: "slowpoke", Status: "connecting", Tools: []mcpTool{{Name: "hidden3"}}},
	}
	if m.Specs() != nil {
		t.Fatal("Specs() before ready must be nil")
	}
	if m.Has("mcp__ok__full") {
		t.Fatal("Has() before ready must be false")
	}
	close(m.ready)
	specs := m.Specs()
	if len(specs) != 4 {
		t.Fatalf("want 4 specs (only ready server), got %d: %+v", len(specs), specs)
	}
	by := map[string]ToolSpec{}
	for _, s := range specs {
		by[s.Name] = s
		if s.Params["type"] != "object" {
			t.Errorf("%s: type = %v", s.Name, s.Params["type"])
		}
		if _, ok := s.Params["properties"].(map[string]any); !ok {
			t.Errorf("%s: properties missing: %+v", s.Name, s.Params)
		}
		if !strings.HasPrefix(s.Description, "[MCP ok] ") {
			t.Errorf("%s: description %q lacks [MCP ok] prefix", s.Name, s.Description)
		}
	}
	if by["mcp__ok__nil_schema"].Description != "[MCP ok] Does a thing." {
		t.Errorf("desc: %q", by["mcp__ok__nil_schema"].Description)
	}
	if by["mcp__ok__no_props"].Description != "[MCP ok] no_props" {
		t.Errorf("empty description must fall back to tool name: %q", by["mcp__ok__no_props"].Description)
	}
	full := by["mcp__ok__full"].Params
	if props := full["properties"].(map[string]any); props["q"] == nil {
		t.Errorf("existing properties lost: %+v", full)
	}
	if full["required"] == nil {
		t.Errorf("required lost: %+v", full)
	}
	if props := by["mcp__ok__no_type"].Params["properties"].(map[string]any); props["a"] == nil {
		t.Errorf("existing properties overwritten: %+v", by["mcp__ok__no_type"].Params)
	}
	// nil manager is safe
	var nm *MCPManager
	if nm.Specs() != nil || nm.Has("x") || !nm.Done() {
		t.Error("nil manager should be inert")
	}
	nm.Wait(time.Millisecond)
	nm.Close()
}

func TestMcpUiCallUnknownTool(t *testing.T) {
	m := &MCPManager{ready: make(chan struct{})}
	close(m.ready)
	out := m.Call(context.Background(), "mcp__nope__x", "{}")
	if !out.IsErr || !strings.Contains(out.Text, "not found") {
		t.Errorf("unexpected: %+v", out)
	}
}

// ============================================================================
// MCP: stdio transport against the fake server
// ============================================================================

func TestMcpUiStdioHandshakeAndPagination(t *testing.T) {
	t.Setenv("MCPUI_TOKEN_SRC", "expanded-ok")
	conf := mcpUiFakeConf(t, "")
	conf.Env["MCPUI_EXPANDED"] = "${MCPUI_TOKEN_SRC}"
	m, root := mcpUiStart(t, map[string]MCPServerConf{"fake": conf})
	s := mcpUiServer(t, m, "fake")
	if mcpUiSt(s) != "ready" {
		t.Fatalf("status %q err %v", mcpUiSt(s), mcpUiErr(s))
	}
	var names []string
	for _, tl := range s.tools() {
		names = append(names, tl.Name)
	}
	want := "echo,fail,weird.tool name,slow,status,mixed,structured,empty,big,crash,rpcerr,bye,addtool,env"
	if strings.Join(names, ",") != want {
		t.Fatalf("tools across pages = %v, want %s", names, want)
	}
	specs := m.Specs()
	if len(specs) != len(names) {
		t.Fatalf("specs %d != tools %d", len(specs), len(names))
	}
	for _, sp := range specs {
		if !m.Has(sp.Name) {
			t.Errorf("Has(%q) = false", sp.Name)
		}
	}
	if !m.Has("mcp__fake__weird_tool_name") {
		t.Error("sanitized name not registered")
	}
	if m.Has("mcp__fake__weird.tool name") {
		t.Error("raw name must not be registered")
	}

	st := mcpUiStatus(t, m, "mcp__fake__status")
	if st["initialized"] != true {
		t.Error("client never sent notifications/initialized")
	}
	if st["protocol"] != mcpProtocol {
		t.Errorf("protocolVersion = %v", st["protocol"])
	}
	if st["client"] != "jazz-cli" {
		t.Errorf("clientInfo.name = %v", st["client"])
	}
	if st["rootsCap"] != true {
		t.Error("roots capability not declared")
	}
	if st["ping"] != true {
		t.Error("client did not answer server->client ping with {}")
	}
	if st["rootsURI"] != "file://"+root {
		t.Errorf("roots/list uri = %v, want file://%s", st["rootsURI"], root)
	}
	if st["unknownErr"] != float64(-32601) {
		t.Errorf("unsupported server request should get -32601, got %v", st["unknownErr"])
	}
	if st["env"] != "expanded-ok" {
		t.Errorf("env ${VAR} not expanded for server: %v", st["env"])
	}
	if cwd, _ := st["cwd"].(string); cwd != root {
		if a, _ := filepath.EvalSymlinks(cwd); a != mcpUiEval(root) {
			t.Errorf("server cwd = %q, want project root %q", cwd, root)
		}
	}
	// the server's stderr log lives under JAZZ_HOME/data
	if _, err := os.Stat(filepath.Join(dataDir(), "mcp-fake.log")); err != nil {
		t.Errorf("stderr log not created: %v", err)
	}
}

func mcpUiEval(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

func TestMcpUiStdioCallContentConversion(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	ctx := context.Background()

	out := m.Call(ctx, "mcp__fake__echo", `{"text":"olá mundo"}`)
	if out.IsErr || out.Text != "echo: olá mundo" {
		t.Errorf("echo: %+v", out)
	}
	if !strings.HasPrefix(out.Display.Summary, "1 linhas · ") || len(out.Display.Preview) != 1 {
		t.Errorf("echo display: %+v", out.Display)
	}

	// lenient args: fenced JSON with trailing comma
	out = m.Call(ctx, "mcp__fake__echo", "```json\n{\"text\":\"fenced\",}\n```")
	if out.IsErr || out.Text != "echo: fenced" {
		t.Errorf("lenient args: %+v", out)
	}
	// empty args become {}
	out = m.Call(ctx, "mcp__fake__echo", "")
	if out.IsErr || out.Text != "echo: <nil>" {
		t.Errorf("empty args: %+v", out)
	}
	// invalid args never reach the server
	out = m.Call(ctx, "mcp__fake__echo", "this is not json")
	if !out.IsErr || !strings.Contains(out.Text, "Validation failed") {
		t.Errorf("invalid args: %+v", out)
	}

	out = m.Call(ctx, "mcp__fake__weird_tool_name", "{}")
	if out.IsErr || out.Text != "called weird.tool name" {
		t.Errorf("sanitized tool must call original name: %+v", out)
	}

	out = m.Call(ctx, "mcp__fake__fail", "{}")
	if !out.IsErr || out.Text != "boom" {
		t.Errorf("isError result: %+v", out)
	}

	out = m.Call(ctx, "mcp__fake__mixed", "{}")
	wantMixed := "first\nresource body\n[resource file:///b.bin]\n[resource link]\n[image attached (image/png)]"
	if out.IsErr || out.Text != wantMixed {
		t.Errorf("mixed content:\n got %q\nwant %q", out.Text, wantMixed)
	}
	if out.Image == nil || out.Image.Mime != "image/png" || out.Image.Data == "" {
		t.Errorf("MCP image content must become ToolOutput.Image (G9): %+v", out.Image)
	}
	if len(out.Display.Preview) != 3 || !strings.HasPrefix(out.Display.Summary, "5 linhas") {
		t.Errorf("mixed display: %+v", out.Display)
	}

	out = m.Call(ctx, "mcp__fake__structured", "{}")
	if out.IsErr || out.Text != `{"answer":42}` {
		t.Errorf("structuredContent fallback: %+v", out)
	}

	out = m.Call(ctx, "mcp__fake__empty", "{}")
	if out.IsErr || out.Text != "(no content)" {
		t.Errorf("empty content: %+v", out)
	}

	out = m.Call(ctx, "mcp__fake__rpcerr", "{}")
	if !out.IsErr || !strings.Contains(out.Text, "kaput (code -32000)") || !strings.Contains(out.Text, "MCP fake/rpcerr failed") {
		t.Errorf("json-rpc error: %+v", out)
	}
	if !strings.HasPrefix(out.Display.Summary, "falhou: ") {
		t.Errorf("error summary: %q", out.Display.Summary)
	}
}

func TestMcpUiStdioTruncation(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	out := m.Call(context.Background(), "mcp__fake__big", "{}")
	if out.IsErr {
		t.Fatalf("big: %s", out.Text[:min(200, len(out.Text))])
	}
	if len(out.Text) >= 100<<10 || len(out.Text) > maxToolBytes+1024 {
		t.Errorf("not truncated: %d bytes", len(out.Text))
	}
	if !strings.Contains(out.Text, "bytes omitted from the middle") {
		t.Error("missing truncation note")
	}
	if !strings.HasPrefix(out.Text, "line 00000") || !strings.HasSuffix(out.Text, "THE-END") {
		t.Error("head/tail not kept")
	}
	// full output spilled under JAZZ_HOME
	ents, _ := os.ReadDir(filepath.Join(dataDir(), "spill"))
	if len(ents) == 0 {
		t.Error("full output was not spilled")
	}
	if !strings.Contains(out.Text, filepath.Join(dataDir(), "spill")) {
		t.Error("spill path not mentioned")
	}
	if !strings.Contains(out.Display.Summary, " linhas · ") || len(out.Display.Preview) != 3 {
		t.Errorf("display: %+v", out.Display)
	}
}

func TestMcpUiStdioCancellation(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	t0 := time.Now()
	out := m.Call(ctx, "mcp__fake__slow", "{}")
	if el := time.Since(t0); el > 10*time.Second {
		t.Fatalf("cancel took %v", el)
	}
	if !out.IsErr || !strings.Contains(out.Text, "context canceled") {
		t.Fatalf("cancelled call: %+v", out)
	}
	s := mcpUiServer(t, m, "fake")
	s.mu.Lock()
	np := len(s.pending)
	s.mu.Unlock()
	if np != 0 {
		t.Errorf("pending map leaked %d entries", np)
	}
	// server got notifications/cancelled with the numeric request id, and still works
	st := mcpUiStatus(t, m, "mcp__fake__status")
	c, _ := st["cancelled"].([]any)
	if len(c) != 1 {
		t.Fatalf("server saw cancellations %v", st["cancelled"])
	}
	if _, ok := c[0].(float64); !ok {
		t.Errorf("requestId should be the numeric JSON-RPC id, got %#v", c[0])
	}
	if out := m.Call(context.Background(), "mcp__fake__echo", `{"text":"still alive"}`); out.IsErr || out.Text != "echo: still alive" {
		t.Errorf("server not usable after cancel: %+v", out)
	}
	if mcpUiSt(s) != "ready" {
		t.Errorf("status after cancel = %q", mcpUiSt(s))
	}
}

func TestMcpUiStdioTimeout(t *testing.T) {
	conf := mcpUiFakeConf(t, "")
	conf.Timeout = 1
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": conf})
	t0 := time.Now()
	out := m.Call(context.Background(), "mcp__fake__slow", "{}")
	el := time.Since(t0)
	if !out.IsErr || !strings.Contains(out.Text, "deadline exceeded") {
		t.Fatalf("timeout: %+v", out)
	}
	if el < 900*time.Millisecond || el > 10*time.Second {
		t.Errorf("timeout elapsed %v, want ~1s", el)
	}
}

func TestMcpUiStdioConcurrentCalls(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("echo: n%d", i)
			out := m.Call(context.Background(), "mcp__fake__echo", fmt.Sprintf(`{"text":"n%d"}`, i))
			if out.IsErr || out.Text != want {
				errs <- fmt.Sprintf("call %d: got %+v", i, out)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestMcpUiStdioServerCrash(t *testing.T) {
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
	s := mcpUiServer(t, m, "fake")
	out := m.Call(context.Background(), "mcp__fake__crash", "{}")
	if !out.IsErr || !strings.Contains(out.Text, "servidor encerrou") {
		t.Fatalf("crash call: %+v", out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for mcpUiSt(s) != "failed" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if mcpUiSt(s) != "failed" || mcpUiErr(s) == nil {
		t.Fatalf("status after crash = %q err=%v", mcpUiSt(s), mcpUiErr(s))
	}
	if len(m.Specs()) == 0 {
		t.Error("specs must stay byte-stable for the prompt cache even after a crash")
	}
	if m.Has("mcp__fake__echo") {
		t.Error("Has() must be false for a crashed server")
	}
	// a further call must fail fast instead of hanging
	done := make(chan ToolOutput, 1)
	go func() { done <- m.Call(context.Background(), "mcp__fake__echo", `{"text":"x"}`) }()
	select {
	case out := <-done:
		if !out.IsErr {
			t.Errorf("call to dead server succeeded: %+v", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call to dead server hung")
	}
}

func TestMcpUiStdioReplyThenExit(t *testing.T) {
	for i := 0; i < 5; i++ {
		m, _ := mcpUiStart(t, map[string]MCPServerConf{"fake": mcpUiFakeConf(t, "")})
		out := m.Call(context.Background(), "mcp__fake__bye", "{}")
		if out.IsErr || out.Text != "goodbye" {
			t.Fatalf("iteration %d: reply written before exit was lost: %+v", i, out)
		}
		m.Close()
	}
}

func TestMcpUiStdioConnectFailures(t *testing.T) {
	exe, _ := os.Executable()
	m, _ := mcpUiStart(t, map[string]MCPServerConf{
		"crashy":  mcpUiFakeConf(t, "crash_init"),
		"errinit": mcpUiFakeConf(t, "rpcerr_init"),
		"badlist": mcpUiFakeConf(t, "bad_list"),
		"missing": {Command: filepath.Join(t.TempDir(), "does-not-exist")},
		"legacy":  {Type: "sse", URL: "http://127.0.0.1:1/sse"},
		"off":     {Command: exe, Disabled: true},
		"ok":      mcpUiFakeConf(t, ""),
	})
	want := map[string]struct{ status, err string }{
		"crashy":  {"failed", "initialize: servidor encerrou"},
		"errinit": {"failed", "init exploded (code -32603)"},
		"badlist": {"failed", "tools/list inválido"},
		"missing": {"failed", "não consegui iniciar"},
		"legacy":  {"failed", "SSE legado"},
		"off":     {"disabled", ""},
		"ok":      {"ready", ""},
	}
	// servers are sorted by name
	var order []string
	for _, s := range m.Servers {
		order = append(order, s.Name)
	}
	if strings.Join(order, ",") != "badlist,crashy,errinit,legacy,missing,off,ok" {
		t.Errorf("servers not sorted: %v", order)
	}
	for _, s := range m.Servers {
		w := want[s.Name]
		if mcpUiSt(s) != w.status {
			t.Errorf("%s: status %q want %q (err %v)", s.Name, mcpUiSt(s), w.status, mcpUiErr(s))
			continue
		}
		if w.err != "" && (mcpUiErr(s) == nil || !strings.Contains(mcpUiErr(s).Error(), w.err)) {
			t.Errorf("%s: err %v, want containing %q", s.Name, mcpUiErr(s), w.err)
		}
	}
	for _, sp := range m.Specs() {
		if !strings.HasPrefix(sp.Name, "mcp__ok__") {
			t.Errorf("tool from non-ready server exposed: %s", sp.Name)
		}
	}
	if len(m.Specs()) != 14 {
		t.Errorf("want 14 tools from ok, got %d", len(m.Specs()))
	}
}

// ============================================================================
// MCP: Streamable HTTP transport
// ============================================================================

type mcpUiHTTP struct {
	mu        sync.Mutex
	problems  []string
	methods   []string
	sessions  []string
	cancelled []any
	slowGone  chan struct{}
}

func (h *mcpUiHTTP) problem(f string, a ...any) {
	h.mu.Lock()
	h.problems = append(h.problems, fmt.Sprintf(f, a...))
	h.mu.Unlock()
}

func (h *mcpUiHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var msg rpcMsg
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		h.problem("bad body: %v", err)
		w.WriteHeader(400)
		return
	}
	h.mu.Lock()
	h.methods = append(h.methods, msg.Method)
	h.sessions = append(h.sessions, r.Header.Get("Mcp-Session-Id"))
	h.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok-123" {
		h.problem("%s: auth header %q", msg.Method, r.Header.Get("Authorization"))
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		h.problem("%s: content-type %q", msg.Method, ct)
	}
	if acc := r.Header.Get("Accept"); !strings.Contains(acc, "application/json") || !strings.Contains(acc, "text/event-stream") {
		h.problem("%s: accept %q", msg.Method, acc)
	}
	if msg.Method == "initialize" {
		if r.Header.Get("Mcp-Session-Id") != "" {
			h.problem("initialize carried a session id")
		}
		w.Header().Set("Mcp-Session-Id", "sess-42")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": mcpProtocol, "capabilities": map[string]any{}}})
		return
	}
	if sid := r.Header.Get("Mcp-Session-Id"); sid != "sess-42" {
		h.problem("%s: session id %q", msg.Method, sid)
		w.WriteHeader(404)
		return
	}
	if pv := r.Header.Get("MCP-Protocol-Version"); pv != mcpProtocol {
		h.problem("%s: protocol header %q", msg.Method, pv)
	}
	if len(msg.ID) == 0 { // notification
		if msg.Method == "notifications/cancelled" {
			var p struct {
				RequestID any `json:"requestId"`
			}
			json.Unmarshal(msg.Params, &p)
			h.mu.Lock()
			h.cancelled = append(h.cancelled, p.RequestID)
			h.mu.Unlock()
		}
		w.WriteHeader(202)
		return
	}
	sse := func(events ...string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			io.WriteString(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
	}
	jsonResp := func(v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(v)
	}
	result := func(v any) map[string]any { return map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": v} }
	mj := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	switch msg.Method {
	case "tools/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		json.Unmarshal(msg.Params, &p)
		if p.Cursor == "" {
			// SSE with an interleaved server request, a comment and a notification before the answer
			sse(
				"event: message\ndata: "+mj(map[string]any{"jsonrpc": "2.0", "id": 9999, "method": "ping"})+"\n\n",
				": keep-alive\n\n",
				"data: "+mj(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{}})+"\n\n",
				"event: message\ndata: "+mj(result(map[string]any{"tools": []any{map[string]any{"name": "echo"}, map[string]any{"name": "sse"}}, "nextCursor": "p2"}))+"\n\n",
			)
			return
		}
		jsonResp(result(map[string]any{"tools": []any{
			map[string]any{"name": "wrongid"}, map[string]any{"name": "sseempty"}, map[string]any{"name": "rpcerr"},
			map[string]any{"name": "http500"}, map[string]any{"name": "slow"}, map[string]any{"name": "ssemulti"},
		}}))
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		json.Unmarshal(msg.Params, &p)
		switch p.Name {
		case "echo":
			jsonResp(result(textContent(fmt.Sprintf("http echo: %v", p.Arguments["text"]))))
		case "sse":
			sse("data: " + mj(result(textContent("from sse"))) + "\n\n")
		case "ssemulti":
			// one JSON message split across several data: lines
			b, _ := json.MarshalIndent(result(textContent("multi-line data")), "", " ")
			var ev strings.Builder
			for _, l := range strings.Split(string(b), "\n") {
				ev.WriteString("data: " + l + "\n")
			}
			sse(ev.String() + "\n")
		case "wrongid":
			jsonResp(map[string]any{"jsonrpc": "2.0", "id": 123456, "result": textContent("nope")})
		case "sseempty":
			sse("data: " + mj(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress"}) + "\n\n")
		case "rpcerr":
			jsonResp(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32001, "message": "http kaput"}})
		case "http500":
			w.WriteHeader(500)
			io.WriteString(w, "internal oops")
		case "slow":
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
			if h.slowGone != nil {
				close(h.slowGone)
			}
		}
	default:
		jsonResp(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "nope"}})
	}
}

func mcpUiStartHTTP(t *testing.T, h *mcpUiHTTP, typ string) (*MCPManager, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("MCPUI_TOK", "tok-123")
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"web": {Type: typ, URL: srv.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer ${MCPUI_TOK}"}, Timeout: 30}})
	return m, srv
}

func TestMcpUiHTTPStreamable(t *testing.T) {
	for _, typ := range []string{"http", ""} {
		t.Run("type="+typ, func(t *testing.T) {
			h := &mcpUiHTTP{}
			m, _ := mcpUiStartHTTP(t, h, typ)
			s := mcpUiServer(t, m, "web")
			if mcpUiSt(s) != "ready" {
				t.Fatalf("status %q err %v", mcpUiSt(s), mcpUiErr(s))
			}
			if mcpUiN(s) != 8 {
				t.Fatalf("want 8 tools over two pages (SSE + JSON), got %d", mcpUiN(s))
			}
			ctx := context.Background()
			cases := []struct {
				tool, args string
				isErr      bool
				contains   string
			}{
				{"echo", `{"text":"hi"}`, false, "http echo: hi"},
				{"sse", `{}`, false, "from sse"},
				{"ssemulti", `{}`, false, "multi-line data"},
				{"wrongid", `{}`, true, "resposta inesperada"},
				{"sseempty", `{}`, true, "stream terminou sem resposta"},
				{"rpcerr", `{}`, true, "http kaput (code -32001)"},
				{"http500", `{}`, true, "HTTP 500: internal oops"},
			}
			for _, c := range cases {
				out := m.Call(ctx, "mcp__web__"+c.tool, c.args)
				if out.IsErr != c.isErr || !strings.Contains(out.Text, c.contains) {
					t.Errorf("%s: got %+v, want isErr=%v containing %q", c.tool, out, c.isErr, c.contains)
				}
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, p := range h.problems {
				t.Error("server saw:", p)
			}
			if h.methods[0] != "initialize" || h.methods[1] != "notifications/initialized" {
				t.Errorf("handshake order: %v", h.methods)
			}
			for i, sid := range h.sessions[1:] {
				if sid != "sess-42" {
					t.Errorf("request %d (%s) did not reuse Mcp-Session-Id: %q", i+1, h.methods[i+1], sid)
				}
			}
		})
	}
}

func TestMcpUiHTTPCancellation(t *testing.T) {
	h := &mcpUiHTTP{slowGone: make(chan struct{})}
	m, _ := mcpUiStartHTTP(t, h, "http")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	t0 := time.Now()
	out := m.Call(ctx, "mcp__web__slow", "{}")
	if !out.IsErr || !strings.Contains(out.Text, "context canceled") {
		t.Fatalf("cancel: %+v", out)
	}
	if time.Since(t0) > 10*time.Second {
		t.Fatal("cancel too slow")
	}
	select {
	case <-h.slowGone:
	case <-time.After(10 * time.Second):
		t.Error("server handler never saw the request abort")
	}
}

func TestMcpUiHTTPCancelNotification(t *testing.T) {
	h := &mcpUiHTTP{slowGone: make(chan struct{})}
	m, _ := mcpUiStartHTTP(t, h, "http")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	m.Call(ctx, "mcp__web__slow", "{}")
	time.Sleep(500 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.cancelled) != 1 {
		t.Errorf("server got %d notifications/cancelled, want 1", len(h.cancelled))
	}
}

func TestMcpUiHTTPConnectFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()
	m, _ := mcpUiStart(t, map[string]MCPServerConf{"web": {URL: srv.URL}})
	s := mcpUiServer(t, m, "web")
	if mcpUiSt(s) != "failed" || mcpUiErr(s) == nil || !strings.Contains(mcpUiErr(s).Error(), "HTTP 401") || !strings.Contains(mcpUiErr(s).Error(), "initialize") {
		t.Errorf("status %q err %v", mcpUiSt(s), mcpUiErr(s))
	}
}

// ============================================================================
// UI: theme, glyphs, banner
// ============================================================================

func mcpUiClearThemeEnv(t *testing.T) {
	for _, k := range []string{"TERM", "NO_COLOR", "COLORTERM", "TERM_PROGRAM", "JAZZ_ASCII"} {
		t.Setenv(k, "")
	}
}

func TestMcpUiDetectTheme(t *testing.T) {
	cases := []struct {
		env                           map[string]string
		color, c256, truecolor, ascii bool
	}{
		{map[string]string{"TERM": "linux"}, true, false, false, true},
		{map[string]string{"TERM": "vt100"}, true, false, false, true},
		{map[string]string{"TERM": "vt220"}, true, false, false, true},
		{map[string]string{"TERM": "xterm"}, true, false, false, false},
		{map[string]string{"TERM": "xterm-256color"}, true, true, false, false},
		{map[string]string{"TERM": "xterm-kitty"}, true, true, false, false},
		{map[string]string{"TERM": "alacritty"}, true, true, false, false},
		{map[string]string{"TERM": "xterm", "TERM_PROGRAM": "iTerm.app"}, true, true, false, false},
		{map[string]string{"TERM": "xterm", "COLORTERM": "truecolor"}, true, true, true, false},
		{map[string]string{"TERM": "xterm", "COLORTERM": "24bit"}, true, true, true, false},
		{map[string]string{"TERM": "dumb", "COLORTERM": "truecolor"}, false, false, false, false},
		{map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"}, false, false, false, false},
		{map[string]string{"TERM": "xterm", "JAZZ_ASCII": "1"}, true, false, false, true},
		{map[string]string{"TERM": "linux", "NO_COLOR": "1"}, false, false, false, true},
	}
	for i, c := range cases {
		mcpUiClearThemeEnv(t)
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		th := DetectTheme()
		if th.color != c.color || th.c256 != c.c256 || th.truecolor != c.truecolor || th.ascii != c.ascii {
			t.Errorf("case %d %v: got %+v", i, c.env, th)
		}
	}
}

func TestMcpUiThemeColors(t *testing.T) {
	c := rgb{255, 0, 128}
	cases := []struct {
		th   Theme
		want string
	}{
		{Theme{}, ""},
		{Theme{color: true}, "\x1b[35m"},
		{Theme{color: true, c256: true}, fmt.Sprintf("\x1b[38;5;%dm", 16+36*5+0+128*5/255)},
		{Theme{color: true, c256: true, truecolor: true}, "\x1b[38;2;255;0;128m"},
	}
	for i, k := range cases {
		if got := k.th.fg(c, 35); got != k.want {
			t.Errorf("case %d fg = %q want %q", i, got, k.want)
		}
	}
	if (Theme{}).sgr("1") != "" || (Theme{color: true}).sgr("1;2") != "\x1b[1;2m" {
		t.Error("sgr")
	}
}

func TestMcpUiGlyphFallbacks(t *testing.T) {
	isASCII := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				return false
			}
		}
		return true
	}
	all := func(g glyphs) []string {
		return append([]string{g.bot, g.tool, g.res, g.prompt, g.ok, g.fail, g.warn, g.dot, g.todoOpen, g.todoDoing, g.todoDone, g.arrow, g.bar, g.sep}, g.spin...)
	}
	ag := Theme{ascii: true}.G()
	for _, s := range all(ag) {
		if s == "" || !isASCII(s) {
			t.Errorf("ascii glyph %q is empty or not ASCII", s)
		}
	}
	ug := Theme{}.G()
	nonASCII := 0
	for _, s := range all(ug) {
		if s == "" {
			t.Error("empty unicode glyph")
		}
		if !isASCII(s) {
			nonASCII++
		}
	}
	if nonASCII == 0 {
		t.Error("unicode theme should use unicode glyphs")
	}
	if len(ag.spin) < 2 || len(ug.spin) < 2 {
		t.Error("spinners need frames")
	}
	// TERM=linux selects the ASCII theme end-to-end
	mcpUiClearThemeEnv(t)
	t.Setenv("TERM", "linux")
	if g := DetectTheme().G(); g.prompt != ">" || g.ok != "ok" {
		t.Errorf("TERM=linux glyphs: %+v", g)
	}
}

func TestMcpUiBanner(t *testing.T) {
	info := []string{"jazz-cli v0.1.0", "modelo x", "pasta ~/p", "modo auto"}
	plain := Banner(Theme{ascii: true}, info)
	if strings.Contains(plain, "\x1b") {
		t.Error("no-color banner must not contain escapes")
	}
	if !strings.Contains(plain, "~ improvisa código no terminal") || strings.Contains(plain, "♪") {
		t.Error("ascii banner must use ~ instead of ♪")
	}
	uni := Banner(Theme{color: true, truecolor: true, c256: true}, info)
	if !strings.Contains(uni, "♪") || !strings.Contains(uni, "\x1b[38;2;") {
		t.Error("truecolor banner lacks note or gradient")
	}
	if termWidth() >= 64 {
		lines := strings.Split(plain, "\n")
		// "", 6 art lines, tagline, "", ""
		if len(lines) < 8 {
			t.Fatalf("banner lines: %q", lines)
		}
		for i, inf := range info {
			if !strings.HasSuffix(lines[2+i], "   "+inf) {
				t.Errorf("info %d not on art line %d: %q", i, 2+i, lines[2+i])
			}
		}
		if !strings.HasSuffix(plain, "\n\n") {
			t.Error("banner should end with a blank line")
		}
		if strings.Count(stripANSI(strings.Join(lines[1:7], "")), "█") == 0 {
			t.Error("art missing")
		}
	}
}

// ============================================================================
// UI: stripANSI / clipVisible / commonPrefix
// ============================================================================

func TestMcpUiStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[1;31mred\x1b[0m", "red"},
		{"\x1b[38;2;10;20;30mrgb\x1b[m", "rgb"},
		{"\x1b[38;5;200mx\x1b[K", "x"},
		{"a\tb", "a\tb"},
		{"a\r\nb", "ab"},
		{"bell\x07", "bell"},
		{"\x1b[12", ""},
		{"x\x1b", "x"},
		{"ação ✓", "ação ✓"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestMcpUiStripANSINonCSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b(B\x1b[mtext", "text"},
		{"\x1b]8;;http://x.y\x07link\x1b]8;;\x07", "link"},
		{"\x1b]0;title\x1b\\after", "after"},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestMcpUiClipVisible(t *testing.T) {
	cases := []struct {
		in      string
		w       int
		wantVis string
		clipped bool
	}{
		{"hello", 10, "hello", false},
		{"hello", 5, "hello", false},
		{"hello!", 5, "hell…", true},
		{"hello world", 6, "hello…", true},
		{"\x1b[31mhello\x1b[0m", 5, "hello", false},
		{"\x1b[31mhello world\x1b[0m", 6, "hello…", true},
		{"ação", 4, "ação", false},
		{"ação!", 4, "açã…", true},
		{"anything", 0, "anything", false},
		{"anything", -3, "anything", false},
		{"", 5, "", false},
		{"ab", 1, "…", true},
	}
	for _, c := range cases {
		got := clipVisible(c.in, c.w)
		if vis := stripANSI(got); vis != c.wantVis {
			t.Errorf("clipVisible(%q,%d) visible = %q want %q (raw %q)", c.in, c.w, vis, c.wantVis, got)
		}
		if c.clipped && !strings.HasSuffix(got, "…\x1b[0m") {
			t.Errorf("clipVisible(%q,%d) = %q should end with reset after ellipsis", c.in, c.w, got)
		}
		if c.w > 0 && utf8.RuneCountInString(stripANSI(got)) > c.w {
			t.Errorf("clipVisible(%q,%d) = %q exceeds width", c.in, c.w, got)
		}
	}
	// escapes before the clip point survive
	got := clipVisible("\x1b[1mbold text here\x1b[0m", 4)
	if !strings.HasPrefix(got, "\x1b[1m") {
		t.Errorf("leading escape lost: %q", got)
	}
}

func TestMcpUiClipVisibleMultibyteExactFit(t *testing.T) {
	in := "olá\x1b[0m"
	if got := clipVisible(in, 3); stripANSI(got) != "olá" {
		t.Errorf("clipVisible(%q,3) = %q, want unchanged", in, got)
	}
}

func TestMcpUiCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"/model"}, "/model"},
		{[]string{"/model", "/mode"}, "/mode"},
		{[]string{"/mode", "/model"}, "/mode"},
		{[]string{"/resume", "/review", "/reset"}, "/re"},
		{[]string{"/a", "/b"}, "/"},
		{[]string{"abc", ""}, ""},
		{[]string{"x", "y"}, ""},
		{[]string{"/ação", "/açaí"}, "/aç"},
	}
	for _, c := range cases {
		if got := commonPrefix(c.in); got != c.want {
			t.Errorf("commonPrefix(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestMcpUiCommonPrefixUTF8(t *testing.T) {
	got := commonPrefix([]string{"/ãa", "/âb"})
	if !utf8.ValidString(got) || got != "/" {
		t.Errorf("commonPrefix = %q, want %q", got, "/")
	}
}

// ============================================================================
// UI: streaming markdown
// ============================================================================

func mcpUiMD(th Theme, chunks []string, first bool) string {
	m := &mdRenderer{t: th, lineStart: true, indent: "  ", first: first}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(m.Feed(c))
	}
	b.WriteString(m.Flush())
	return b.String()
}

// visible strips ANSI per line (stripANSI drops newlines).
func mcpUiVisible(s string) string {
	ls := strings.Split(s, "\n")
	for i := range ls {
		ls[i] = stripANSI(ls[i])
	}
	return strings.Join(ls, "\n")
}

func TestMcpUiMarkdownPlain(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"first line not indented", "hello\nworld", "hello\n  world"},
		{"heading", "# Title\ntext", "Title\n  text"},
		{"heading h3", "intro\n### Sub section", "intro\n  Sub section"},
		{"bullets", "- a\n- b\n* c\n+ d", "• a\n  • b\n  • c\n  • d"},
		{"nested bullet keeps pad", "x\n  - nested", "x\n    • nested"},
		{"bold and code", "**bold** and `code`", "bold and code"},
		{"single star literal", "2 * 3 = 6", "2 * 3 = 6"},
		{"inner star literal", "a*b", "a*b"},
		{"star at eol", "a *\nb", "a *\n  b"},
		{"trailing star flushed", "x*", "x*"},
		{"stars in code literal", "`a*b**c`", "a*b**c"},
		{"dash not bullet", "-x and --flag", "-x and --flag"},
		{"lone dash line", "-\nx", "-\n  x"},
		{"lone hash line", "#\nx", "#\n  x"},
		{"empty first line", "\nx", "\n  x"},
		{"blank lines", "a\n\nb", "a\n\n  b"},
		{"leading backtick inline code", "`cmd` runs", "cmd runs"},
		{"partial fence at end", "``", "``"},
		{"heading with code", "## Use `go test`", "Use go test"},
		{"bold bullet", "- **x** y", "• x y"},
		{"unicode", "ação ✓ — ok", "ação ✓ — ok"},
		{"fence",
			"```go\n# not heading\n- not bullet\n**not bold**\nx := 1\n```\nafter",
			"╭─ go\n  │ # not heading\n  │ - not bullet\n  │ **not bold**\n  │ x := 1\n  ╰── \n  after"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mcpUiMD(Theme{}, []string{c.in}, true)
			if got != c.want {
				t.Errorf("render(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
	// not first: first line is indented too
	if got := mcpUiMD(Theme{}, []string{"a\nb"}, false); got != "  a\n  b" {
		t.Errorf("non-first render = %q", got)
	}
}

func TestMcpUiMarkdownStyles(t *testing.T) {
	th := Theme{color: true}
	bold, mag, cyan, reset := "\x1b[1m", "\x1b[35m", "\x1b[36m", "\x1b[0m"

	out := mcpUiMD(th, []string{"# Title\nplain"}, true)
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], bold) || !strings.Contains(lines[0], mag) || !strings.HasSuffix(lines[0], reset) {
		t.Errorf("heading not bold magenta / not reset: %q", lines[0])
	}
	if strings.Contains(lines[1], bold) || strings.Contains(lines[1], mag) {
		t.Errorf("heading style leaked to next line: %q", lines[1])
	}

	out = mcpUiMD(th, []string{"a **b** c"}, true)
	if !strings.Contains(out, "a "+reset+bold+"b"+reset+" c") {
		t.Errorf("bold: %q", out)
	}
	out = mcpUiMD(th, []string{"run `ls` now"}, true)
	if !strings.Contains(out, "run "+reset+cyan+"ls"+reset+" now") {
		t.Errorf("code span: %q", out)
	}
	// code span is closed at end of line even if unbalanced
	out = mcpUiMD(th, []string{"open `code\nnext"}, true)
	if l := strings.Split(out, "\n")[1]; strings.Contains(l, cyan) {
		t.Errorf("unbalanced code span leaked: %q", l)
	}

	// fenced code: '#' lines are code, not headings; content colored as code
	out = mcpUiMD(th, []string{"# Real\n```sh\n# comment\n## also\nls\n```\ndone"}, true)
	ls := strings.Split(out, "\n")
	if len(ls) != 7 {
		t.Fatalf("lines: %q", ls)
	}
	for _, i := range []int{2, 3} {
		if strings.Contains(ls[i], bold) || strings.Contains(ls[i], mag) {
			t.Errorf("line inside fence styled as heading: %q", ls[i])
		}
		if !strings.Contains(ls[i], cyan) {
			t.Errorf("line inside fence not code-colored: %q", ls[i])
		}
	}
	if stripANSI(ls[2]) != "  │ # comment" || stripANSI(ls[3]) != "  │ ## also" {
		t.Errorf("hashes inside fence lost: %q %q", ls[2], ls[3])
	}
	if strings.Contains(ls[6], cyan) {
		t.Errorf("code color leaked after closing fence: %q", ls[6])
	}
	if !strings.Contains(ls[0], bold) {
		t.Errorf("real heading not bold: %q", ls[0])
	}
	if !strings.HasSuffix(out, reset) {
		t.Error("Flush must reset style")
	}

	// bullet glyph colored gold, text after uses normal style
	out = mcpUiMD(th, []string{"- item"}, true)
	if !strings.Contains(out, "\x1b[33m•") {
		t.Errorf("bullet: %q", out)
	}
}

func TestMcpUiMarkdownChunkInvariance(t *testing.T) {
	doc := "# Plano\nVou **mexer** em `main.go` agora.\n\n- passo um\n  - sub *item*\n* outro\n\n```go\n# isso é código\nfunc main() { x := 2 * 3 }\n```\n## Fim\n2 * 3 = 6, a*b, **ação** ✓\n-\n#\n``\ntexto final *"
	for _, th := range []Theme{{}, {color: true}, {color: true, truecolor: true, c256: true}, {ascii: true}} {
		whole := mcpUiMD(th, []string{doc}, true)
		var runes []string
		for _, r := range doc {
			runes = append(runes, string(r))
		}
		perRune := mcpUiMD(th, runes, true)
		if perRune != whole {
			t.Errorf("theme %+v: rune-by-rune differs\nwhole %q\nrunes %q", th, whole, perRune)
		}
		if mcpUiVisible(perRune) != mcpUiVisible(whole) {
			t.Errorf("theme %+v: visible output differs", th)
		}
		// irregular chunk sizes on rune boundaries, splitting markers like ``` ** ##
		var chunks []string
		rs := []rune(doc)
		for i, n := 0, 1; i < len(rs); n = n%5 + 1 {
			j := min(len(rs), i+n)
			chunks = append(chunks, string(rs[i:j]))
			i = j
		}
		if got := mcpUiMD(th, chunks, true); mcpUiVisible(got) != mcpUiVisible(whole) {
			t.Errorf("theme %+v: chunked visible output differs\nwhole %q\n got %q", th, mcpUiVisible(whole), mcpUiVisible(got))
		}
	}
	// sanity on the visible text of the document
	vis := mcpUiVisible(mcpUiMD(Theme{}, []string{doc}, true))
	for _, want := range []string{"Plano\n", "Vou mexer em main.go agora.", "• passo um", "  │ # isso é código", "func main() { x := 2 * 3 }", "Fim", "2 * 3 = 6, a*b, ação ✓", "texto final *"} {
		if !strings.Contains(vis, want) {
			t.Errorf("visible output lacks %q:\n%s", want, vis)
		}
	}
}

func TestMcpUiMarkdownResetBetweenMessages(t *testing.T) {
	m := &mdRenderer{t: Theme{}, lineStart: true, indent: "  ", first: true}
	a := m.Feed("**unclosed bold and `code") + m.Flush()
	if stripANSI(a) != "unclosed bold and code" {
		t.Errorf("first message: %q", a)
	}
	if m.bold || m.code || m.fence || m.header || !m.lineStart || m.indent != "  " {
		t.Errorf("Flush must reset state but keep indent: %+v", m)
	}
	m.first = true
	b := m.Feed("# next") + m.Flush()
	if b != "next" {
		t.Errorf("second message after reset: %q", b)
	}
	// unclosed fence does not leak into the next message
	m.first = true
	m.Feed("```\ncode")
	m.Flush()
	m.first = true
	if got := m.Feed("# head") + m.Flush(); got != "head" {
		t.Errorf("fence leaked into next message: %q", got)
	}
}

func TestMcpUiMarkdownHashWithoutSpace(t *testing.T) {
	if got := mcpUiMD(Theme{}, []string{"x\n#include <stdio.h>"}, true); got != "x\n  #include <stdio.h>" {
		t.Errorf("got %q", got)
	}
}

func TestMcpUiMarkdownASCIIBullet(t *testing.T) {
	mcpUiClearThemeEnv(t)
	t.Setenv("TERM", "linux")
	out := mcpUiMD(DetectTheme(), []string{"- item\n- two"}, true)
	for i := 0; i < len(out); i++ {
		if out[i] >= 0x80 {
			t.Fatalf("ASCII theme produced non-ASCII output: %q", out)
		}
	}
}

func TestMcpUiMarkdownASCIIFence(t *testing.T) {
	mcpUiClearThemeEnv(t)
	t.Setenv("TERM", "linux")
	th := DetectTheme()
	if !th.ascii {
		t.Fatal("TERM=linux must select ascii")
	}
	got := mcpUiVisible(mcpUiMD(th, []string{"```\nx\n```"}, true))
	if got != "+- \n  | x\n  +-- " {
		t.Errorf("ascii fence = %q", got)
	}
}

// ============================================================================
// prompt.go: instructions, frontmatter, skills, hardware
// ============================================================================

func mcpUiWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mcpUiInstPaths(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "<project_instructions path=") {
			p := strings.TrimSuffix(strings.TrimPrefix(l, "<project_instructions path="), ">")
			var q string
			json.Unmarshal([]byte(p), &q)
			out = append(out, q)
		}
	}
	return out
}

func TestMcpUiInstructionsPriorityNoGit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	cases := []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"AGENTS.override.md": "O", "AGENTS.md": "A", "CLAUDE.md": "C", "JAZZ.md": "J"}, "AGENTS.override.md"},
		{map[string]string{"AGENTS.md": "A", "CLAUDE.md": "C", "JAZZ.md": "J"}, "AGENTS.md"},
		{map[string]string{"CLAUDE.md": "C", "JAZZ.md": "J"}, "CLAUDE.md"},
		{map[string]string{"JAZZ.md": "J"}, "JAZZ.md"},
		{map[string]string{}, ""},
	}
	for i, c := range cases {
		parent := t.TempDir()
		mcpUiWrite(t, filepath.Join(parent, "AGENTS.md"), "parent outside project")
		root := filepath.Join(parent, "proj")
		os.MkdirAll(root, 0o755)
		if gitRoot(root) != "" {
			t.Skip("temp dir is inside a git repo")
		}
		for n, body := range c.files {
			mcpUiWrite(t, filepath.Join(root, n), body)
		}
		got := loadInstructions(root)
		paths := mcpUiInstPaths(got)
		if c.want == "" {
			if got != "" {
				t.Errorf("case %d: want empty, got %q", i, got)
			}
			continue
		}
		if len(paths) != 1 || paths[0] != filepath.Join(root, c.want) {
			t.Errorf("case %d: paths %v, want only %s (parent outside non-git project must be ignored)", i, paths, c.want)
		}
		if !strings.Contains(got, "\n"+c.files[c.want]+"\n</project_instructions>") {
			t.Errorf("case %d: body missing: %q", i, got)
		}
	}
}

func TestMcpUiInstructionsGlobalAndGitOrder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	mcpUiWrite(t, filepath.Join(home, "AGENTS.md"), "GLOBAL")
	base := mcpUiEval(t.TempDir())
	mcpUiWrite(t, filepath.Join(base, "AGENTS.md"), "OUTSIDE REPO")
	repo := filepath.Join(base, "repo")
	os.MkdirAll(repo, 0o755)
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	mcpUiWrite(t, filepath.Join(repo, "AGENTS.md"), "REPO")
	mcpUiWrite(t, filepath.Join(repo, "a", "CLAUDE.md"), "A-DIR")
	mcpUiWrite(t, filepath.Join(repo, "a", "b", "c", "AGENTS.override.md"), "C-DIR")
	mcpUiWrite(t, filepath.Join(repo, "a", "b", "c", "AGENTS.md"), "C-DIR-SHADOWED")
	mcpUiWrite(t, filepath.Join(repo, "a", "b", "c", "d", "AGENTS.md"), "BELOW CWD")
	root := filepath.Join(repo, "a", "b", "c")
	got := loadInstructions(root)
	want := []string{
		filepath.Join(home, "AGENTS.md"),
		filepath.Join(repo, "AGENTS.md"),
		filepath.Join(repo, "a", "CLAUDE.md"),
		filepath.Join(root, "AGENTS.override.md"),
	}
	if p := mcpUiInstPaths(got); strings.Join(p, "|") != strings.Join(want, "|") {
		t.Errorf("paths\n got %v\nwant %v", p, want)
	}
	for _, bad := range []string{"OUTSIDE REPO", "C-DIR-SHADOWED", "BELOW CWD"} {
		if strings.Contains(got, bad) {
			t.Errorf("must not include %q", bad)
		}
	}
	if !strings.HasSuffix(got, "</project_instructions>\n") {
		t.Errorf("trailing format: %q", got[max(0, len(got)-40):])
	}
}

func TestMcpUiInstructionsSymlinkedRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("JAZZ_HOME", t.TempDir())
	base := mcpUiEval(t.TempDir())
	mcpUiWrite(t, filepath.Join(base, "AGENTS.md"), "OUTSIDE REPO")
	repo := filepath.Join(base, "real", "repo")
	os.MkdirAll(repo, 0o755)
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	mcpUiWrite(t, filepath.Join(repo, "AGENTS.md"), "REPO")
	link := filepath.Join(base, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skip(err)
	}
	got := loadInstructions(link)
	if strings.Contains(got, "OUTSIDE REPO") {
		t.Errorf("instructions from outside the git repo leaked in: %v", mcpUiInstPaths(got))
	}
	if !strings.Contains(got, "REPO") {
		t.Errorf("repo instructions missing: %q", got)
	}
}

func TestMcpUiInstructionsCap(t *testing.T) {
	const capB = 32 << 10
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	root := t.TempDir()
	if gitRoot(root) != "" {
		t.Skip("temp dir inside git repo")
	}

	// a single oversized file is cut to 32 KiB
	big := strings.Repeat("y", 40<<10)
	mcpUiWrite(t, filepath.Join(root, "AGENTS.md"), big)
	got := loadInstructions(root)
	if n := strings.Count(got, "y"); n != capB {
		t.Errorf("single file kept %d bytes, want %d", n, capB)
	}
	if !strings.Contains(got, "[… truncated]") {
		t.Error("missing truncation marker")
	}

	// global + project share the budget
	mcpUiWrite(t, filepath.Join(home, "AGENTS.md"), strings.Repeat("g", 20<<10))
	mcpUiWrite(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("p", 20<<10))
	got = loadInstructions(root)
	if !strings.Contains(got, strings.Repeat("g", 20<<10)) {
		t.Error("global file should be complete")
	}
	remaining := capB - (20 << 10)
	if !strings.Contains(got, strings.Repeat("p", remaining)+"\n[… truncated]") {
		t.Error("project file should be cut exactly to the remaining budget and marked")
	}
	if strings.Contains(got, strings.Repeat("p", remaining+1)) {
		t.Error("project content exceeds remaining budget")
	}
	// a third, deeper file never gets in once the budget is spent: covered by single-dir layout

	// small files are untouched
	mcpUiWrite(t, filepath.Join(home, "AGENTS.md"), "small global")
	mcpUiWrite(t, filepath.Join(root, "AGENTS.md"), "small project")
	got = loadInstructions(root)
	if strings.Contains(got, "truncated") || !strings.Contains(got, "small global") || !strings.Contains(got, "small project") {
		t.Errorf("small files: %q", got)
	}

	// whitespace-only files are ignored
	mcpUiWrite(t, filepath.Join(home, "AGENTS.md"), "  \n\t\n")
	got = loadInstructions(root)
	if ps := mcpUiInstPaths(got); len(ps) != 1 || ps[0] != filepath.Join(root, "AGENTS.md") {
		t.Errorf("whitespace global should be skipped: %v", ps)
	}
}

func TestMcpUiReadFrontmatter(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, content, wantName, wantDesc string
	}{
		{"basic", "---\nname: deploy\ndescription: Deploy the app\n---\nbody", "deploy", "Deploy the app"},
		{"quoted", "---\nname: \"q\"\ndescription: 'single quoted'\n---\n", "q", "single quoted"},
		{"crlf", "---\r\nname: win\r\ndescription: windows file\r\n---\r\nbody", "win", "windows file"},
		{"colon in value", "---\ndescription: use: when x\n---\n", "", "use: when x"},
		{"extra keys and spaces", "---\n  name :  spaced  \nlicense: MIT\ndescription:d\n---", "spaced", "d"},
		{"no frontmatter", "name: x\ndescription: y\n", "", ""},
		{"unterminated", "---\nname: x\ndescription: y\n", "", ""},
		{"empty", "", "", ""},
		{"empty frontmatter", "---\n---\nbody", "", ""},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name+".md")
		mcpUiWrite(t, p, c.content)
		n, d := readFrontmatter(p)
		if n != c.wantName || d != c.wantDesc {
			t.Errorf("%s: got (%q,%q) want (%q,%q)", c.name, n, d, c.wantName, c.wantDesc)
		}
	}
	if n, d := readFrontmatter(filepath.Join(dir, "missing.md")); n != "" || d != "" {
		t.Error("missing file should give empty")
	}
}

func TestMcpUiSkillsBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if skillsBlock(root) != "" {
		t.Fatal("no skills should give empty block")
	}
	mcpUiWrite(t, filepath.Join(home, "skills", "alpha", "SKILL.md"), "---\nname: alpha\ndescription: Global alpha\n---\n")
	mcpUiWrite(t, filepath.Join(root, ".jazz", "skills", "beta", "SKILL.md"), "---\ndescription: Dir-named beta\n---\n")
	mcpUiWrite(t, filepath.Join(root, ".claude", "skills", "gamma", "SKILL.md"), "---\nname: gamma\ndescription: "+strings.Repeat("z", 300)+"\n---\n")
	mcpUiWrite(t, filepath.Join(root, ".claude", "skills", "nodesc", "SKILL.md"), "---\nname: nodesc\n---\n")
	mcpUiWrite(t, filepath.Join(root, ".claude", "skills", "nofile", "README.md"), "x")
	mcpUiWrite(t, filepath.Join(root, ".claude", "skills", "loose.md"), "---\nname: loose\ndescription: not a dir\n---\n")
	mcpUiWrite(t, filepath.Join(root, ".jazz", "skills", "dup", "SKILL.md"), "---\nname: alpha\ndescription: Duplicate alpha\n---\n")
	b := skillsBlock(root)
	if !strings.HasPrefix(b, "<skills>\n") || !strings.HasSuffix(b, "</skills>\n") {
		t.Fatalf("framing: %q", b)
	}
	if !strings.Contains(b, "- alpha: Global alpha ("+filepath.Join(home, "skills", "alpha", "SKILL.md")+")") {
		t.Errorf("alpha missing: %s", b)
	}
	if !strings.Contains(b, "- beta: Dir-named beta (") {
		t.Errorf("name should fall back to dir name: %s", b)
	}
	if !strings.Contains(b, "- gamma: "+strings.Repeat("z", 120)+"… (") {
		t.Errorf("description not capped at 120: %s", b)
	}
	for _, bad := range []string{"nodesc", "nofile", "loose", "Duplicate alpha"} {
		if strings.Contains(b, bad) {
			t.Errorf("should not list %q: %s", bad, b)
		}
	}
	if strings.Count(b, "\n- ") != 3 {
		t.Errorf("want 3 skills: %s", b)
	}

	// at most 12 skills (built-ins are picked by the harness, not listed)
	for i := 0; i < 50; i++ {
		mcpUiWrite(t, filepath.Join(root, ".jazz", "skills", fmt.Sprintf("s%02d", i), "SKILL.md"), fmt.Sprintf("---\ndescription: d%d\n---\n", i))
	}
	if n := strings.Count(skillsBlock(root), "\n- "); n != 12 {
		t.Errorf("skills capped at 12, got %d", n)
	}
}

// ~/.claude/skills (often dozens of big skills for other agents) is listed
// only when the config opts in with skills.claude_dir.
func TestSkillsClaudeHomeDirOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("HOME", home)
	root := t.TempDir()
	mcpUiWrite(t, filepath.Join(home, ".claude", "skills", "ecc", "SKILL.md"), "---\nname: ecc\ndescription: From claude home\n---\n")
	defer skConfigure(nil)
	skConfigure(&Config{})
	if b := skillsBlock(root); strings.Contains(b, "ecc") {
		t.Fatalf("~/.claude/skills must be opt-in: %s", b)
	}
	skConfigure(&Config{Skills: SkillsConf{ClaudeDir: true}})
	if b := skillsBlock(root); !strings.Contains(b, "- ecc: From claude home") {
		t.Fatalf("skills.claude_dir should list ~/.claude/skills: %s", b)
	}
}

func TestMcpUiHardwareNote(t *testing.T) {
	note := hardwareNote()
	cpus, mem := cpuCount(), memTotalMB()
	slow := !(cpus > 2 && (mem == 0 || mem >= 4096))
	if slow != (note != "") {
		t.Fatalf("cpus=%d mem=%d: note=%q", cpus, mem, note)
	}
	if note != "" {
		for _, want := range []string{"slow machine", runtime.GOARCH, fmt.Sprintf("%d CPU", cpus), fmt.Sprintf("%d MB RAM", mem), "never find/rg over /"} {
			if !strings.Contains(note, want) {
				t.Errorf("note lacks %q: %s", want, note)
			}
		}
		if strings.Contains(note, "(  ") || strings.Contains(note, "( ") {
			t.Errorf("badly formatted model: %s", note)
		}
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/meminfo"); err == nil && mem <= 0 {
			t.Errorf("memTotalMB failed to parse /proc/meminfo")
		}
	}
	t.Logf("cpus=%d mem=%dMB note=%q", cpus, mem, note)
}

// ============================================================================
// main.go helpers
// ============================================================================

func TestMcpUiReorderFlags(t *testing.T) {
	cases := []struct{ in, want []string }{
		{nil, nil},
		{[]string{"fix it"}, []string{"fix it"}},
		{[]string{"fix it", "-m", "x/y"}, []string{"-m", "x/y", "fix it"}},
		{[]string{"-p", "do", "-v"}, []string{"-p", "-v", "do"}},
		{[]string{"a", "--model", "m", "b", "-C", "/d", "-r", "id"}, []string{"--model", "m", "-C", "/d", "-r", "id", "a", "b"}},
		{[]string{"--model=x", "word"}, []string{"--model=x", "word"}},
		{[]string{"word", "--", "-m", "lit"}, []string{"word", "-m", "lit"}},
		{[]string{"-", "x"}, []string{"-", "x"}},
		{[]string{"x", "-m"}, []string{"-m", "x"}},
		{[]string{"login", "chatgpt", "--yolo"}, []string{"--yolo", "login", "chatgpt"}},
		{[]string{"login", "--browser"}, []string{"login", "--browser"}},
		{[]string{"trust", "--yes"}, []string{"trust", "--yes"}},
		{[]string{"provider", "add", "x", "--url", "u", "--model", "m", "-C", "/d"}, []string{"-C", "/d", "provider", "add", "x", "--url", "u", "--model", "m"}},
		{[]string{"fix", "trust", "-m", "x"}, []string{"-m", "x", "fix", "trust"}},
		{[]string{"-p", "--output-format", "json", "--allow-tools", "bash,edit", "go"}, []string{"-p", "--output-format", "json", "--allow-tools", "bash,edit", "go"}},
	}
	for _, c := range cases {
		got := reorderFlags(c.in)
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("reorderFlags(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestMcpUiFriendlyErr(t *testing.T) {
	cases := []struct {
		err      error
		contains []string
	}{
		{&APIError{Status: 401, Body: "bad key. more"}, []string{"credencial recusada", "bad key", "/login"}},
		{fmt.Errorf("wrap: %w", &APIError{Status: 403, Body: "forbidden"}), []string{"credencial recusada", "forbidden"}},
		{&APIError{Status: 402, Body: "x"}, []string{"sem crédito"}},
		{&APIError{Status: 429, Body: "slow down"}, []string{"limite de requisições", "slow down"}},
		{&APIError{Status: 0, Body: "dial tcp: refused"}, []string{"falha de rede", "dial tcp: refused"}},
		{&APIError{Status: 500, Body: "oops"}, []string{"HTTP 500: oops"}},
		{fmt.Errorf("turn: %w", errStreamIdle), []string{"parou de responder", "tente de novo"}},
		{errors.New("plain failure"), []string{"plain failure"}},
	}
	for _, c := range cases {
		got := friendlyErr(c.err)
		for _, w := range c.contains {
			if !strings.Contains(got, w) {
				t.Errorf("friendlyErr(%v) = %q, missing %q", c.err, got, w)
			}
		}
	}
	if got := friendlyErr(&APIError{Status: 401, Body: "bad key. more"}); strings.Contains(got, "more") {
		t.Errorf("only first sentence of body expected: %q", got)
	}
}

func TestMcpUiCustomCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	root := t.TempDir()
	if len(customCommands(root)) != 0 {
		t.Fatal("expected no commands")
	}
	mcpUiWrite(t, filepath.Join(home, "commands", "review.md"), "---\ndescription: \"Review the diff\"\nargument-hint: x\n---\n\nReview $ARGUMENTS carefully.\n")
	mcpUiWrite(t, filepath.Join(root, ".jazz", "commands", "review.md"), "project review shadowed")
	mcpUiWrite(t, filepath.Join(root, ".jazz", "commands", "plain.md"), "  Just do   the\nthing please, with a very long body that goes past sixty characters for sure\n")
	mcpUiWrite(t, filepath.Join(root, ".claude", "commands", "alpha.md"), "---\ndescription: from claude dir\n---\nalpha body")
	mcpUiWrite(t, filepath.Join(root, ".claude", "commands", "nodesc.md"), "---\nother: 1\n---\nbody only")
	mcpUiWrite(t, filepath.Join(root, ".claude", "commands", "notes.txt"), "ignored")
	os.MkdirAll(filepath.Join(root, ".claude", "commands", "dir.md"), 0o755)

	cmds := customCommands(root)
	var names []string
	by := map[string]customCmd{}
	for _, c := range cmds {
		names = append(names, c.name)
		by[c.name] = c
	}
	if strings.Join(names, ",") != "alpha,nodesc,plain,review" {
		t.Fatalf("names = %v", names)
	}
	if r := by["review"]; r.desc != "Review the diff" || r.body != "Review $ARGUMENTS carefully." {
		t.Errorf("review: %+v", r)
	}
	if p := by["plain"]; p.desc != oneLine(p.body, 60) || !strings.HasSuffix(p.desc, "…") || p.body != "  Just do   the\nthing please, with a very long body that goes past sixty characters for sure\n" {
		t.Errorf("plain: %+v", p)
	}
	if a := by["alpha"]; a.desc != "from claude dir" || a.body != "alpha body" {
		t.Errorf("alpha: %+v", a)
	}
	if n := by["nodesc"]; n.desc != "body only" || n.body != "body only" {
		t.Errorf("nodesc: %+v", n)
	}
}
