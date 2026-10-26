package main

// textcalls.go: recovering tool calls that models write as plain text, and the
// "text tools" mode for models without native function calling (G15, bug 69).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// acSpan is one tool call found in a text reply: text[start:end] is the markup.
type acSpan struct {
	start, end int
	name, args string
	fenced     bool // the call is a whole ``` block (format: fenced JSON)
}

var (
	reTCTagOpen   = lazyRx(`<tool_call>`)
	reTCFuncOpen  = lazyRx(`<function=([\w.:-]+)>`)
	reTCParam     = lazyRx(`(?s)<parameter=([\w.-]+)>\n?(.*?)\n?</parameter>`)
	reTCHarmony   = lazyRx(`to=functions\.([\w.-]+)`)
	reTCSpecial   = lazyRx(`<\|tool_call\|>|<\|python_tag\|>|\[TOOL_CALLS\]`)
	reTCMistralV3 = lazyRx(`^\s*([\w.-]+)\s*\[ARGS\]`)
	reTCFence     = lazyRx("(?m)^[ \t]*```")
	reTCTrailer   = lazyRx(`^\s*(?:</tool_call>|</function>|<\|call\|>|<\|end\|>|<\|eom_id\|>|<\|eot_id\|>|<\|/tool_call\|>|\s)*`)
)

var acTxtSeq atomic.Int64

func acTxtID() string {
	return fmt.Sprintf("txt_%d_%d", time.Now().UnixNano()%1e9, acTxtSeq.Add(1))
}

// acDecodeJSON decodes one JSON value starting at s[i] (after optional spaces)
// and returns it with the offset just past it.
func acDecodeJSON(s string, i int) (any, int, bool) {
	for i < len(s) && strings.ContainsRune(" \t\r\n", rune(s[i])) {
		i++
	}
	if i >= len(s) || (s[i] != '{' && s[i] != '[') {
		return nil, 0, false
	}
	dec := json.NewDecoder(strings.NewReader(s[i:]))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, 0, false
	}
	return v, i + int(dec.InputOffset()), true
}

// acCallFromObj reads {name, arguments} (and the common variants) from a decoded object.
func acCallFromObj(v any) (name, args string, ok bool) {
	m, isMap := v.(map[string]any)
	if !isMap {
		return "", "", false
	}
	if f, isF := m["function"].(map[string]any); isF { // OpenAI shape leaked as text
		m = f
	}
	for _, k := range []string{"name", "tool", "tool_name", "function", "recipient_name"} {
		if s, isS := m[k].(string); isS && strings.TrimSpace(s) != "" {
			name = strings.TrimSpace(s)
			break
		}
	}
	if name == "" {
		return "", "", false
	}
	var a any
	for _, k := range []string{"arguments", "parameters", "args", "input"} {
		if x, has := m[k]; has {
			a = x
			break
		}
	}
	switch x := a.(type) {
	case nil:
		args = "{}"
	case string:
		args = strings.TrimSpace(x)
		if args == "" {
			args = "{}"
		}
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "", "", false
		}
		args = string(b)
	}
	return name, args, true
}

// acCallsFromValue accepts one call object or an array of them.
func acCallsFromValue(v any) []acSpan {
	if arr, ok := v.([]any); ok {
		var out []acSpan
		for _, e := range arr {
			n, a, ok := acCallFromObj(e)
			if !ok {
				return nil
			}
			out = append(out, acSpan{name: n, args: a})
		}
		return out
	}
	if n, a, ok := acCallFromObj(v); ok {
		return []acSpan{{name: n, args: a}}
	}
	return nil
}

// acFenceRanges returns the [start,end) byte ranges of ``` fenced blocks.
func acFenceRanges(text string) [][2]int {
	locs := reTCFence.FindAllStringIndex(text, -1)
	var out [][2]int
	for i := 0; i+1 < len(locs); i += 2 {
		end := locs[i+1][1]
		if nl := strings.IndexByte(text[end:], '\n'); nl >= 0 {
			end += nl
		} else {
			end = len(text)
		}
		out = append(out, [2]int{locs[i][0], end})
	}
	if len(locs)%2 == 1 { // unterminated fence runs to the end
		out = append(out, [2]int{locs[len(locs)-1][0], len(text)})
	}
	return out
}

func acInRanges(pos int, rs [][2]int) bool {
	for _, r := range rs {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}

// acFindTextCalls scans every supported text format and returns the calls in
// order of appearance (non-overlapping). Names are not validated here.
func acFindTextCalls(text string) []acSpan {
	var spans []acSpan
	add := func(s acSpan) {
		for _, o := range spans {
			if s.start < o.end && o.start < s.end {
				return
			}
		}
		spans = append(spans, s)
	}

	// 1. <tool_call>{json}</tool_call> (closing tag optional at the very end)
	for _, loc := range reTCTagOpen.FindAllStringIndex(text, -1) {
		v, end, ok := acDecodeJSON(text, loc[1])
		if !ok {
			continue
		}
		cs := acCallsFromValue(v)
		if len(cs) == 0 {
			continue
		}
		rest := text[end:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		switch {
		case strings.HasPrefix(trimmed, "</tool_call>"):
			end += len(rest) - len(trimmed) + len("</tool_call>")
		case strings.TrimSpace(rest) == "":
			end = len(text)
		default:
			continue
		}
		for i := range cs {
			cs[i].start, cs[i].end = loc[0], end
			if i > 0 {
				cs[i].start = end // zero-width extra calls of an array share the span
			}
			add(cs[i])
		}
	}

	// 2. <function=name>{json}</function> or Qwen XML <parameter=k>v</parameter>
	for _, loc := range reTCFuncOpen.FindAllStringSubmatchIndex(text, -1) {
		name := text[loc[2]:loc[3]]
		closeIdx := strings.Index(text[loc[1]:], "</function>")
		var body string
		end := len(text)
		if closeIdx >= 0 {
			body = text[loc[1] : loc[1]+closeIdx]
			end = loc[1] + closeIdx + len("</function>")
		} else {
			body = text[loc[1]:]
		}
		args := ""
		if v, e, ok := acDecodeJSON(body, 0); ok && strings.TrimSpace(body[e:]) == "" {
			if _, isMap := v.(map[string]any); isMap {
				args = strings.TrimSpace(body)
			}
		} else if ps := reTCParam.FindAllStringSubmatch(body, -1); len(ps) > 0 {
			m := map[string]any{}
			for _, p := range ps {
				var jv any
				if json.Unmarshal([]byte(p[2]), &jv) == nil {
					if _, isStr := jv.(string); !isStr {
						m[p[1]] = jv
						continue
					}
				}
				m[p[1]] = p[2]
			}
			b, _ := json.Marshal(m)
			args = string(b)
		} else if strings.TrimSpace(body) == "" {
			args = "{}"
		}
		if args == "" {
			continue
		}
		add(acSpan{start: loc[0], end: end, name: name, args: args})
	}

	// 3. special tokens: [TOOL_CALLS] (Mistral), <|tool_call|>, <|python_tag|> (Llama)
	for _, loc := range reTCSpecial.FindAllStringIndex(text, -1) {
		after := loc[1]
		if text[loc[0]:loc[1]] == "[TOOL_CALLS]" {
			if m := reTCMistralV3.FindStringSubmatchIndex(text[after:]); m != nil { // [TOOL_CALLS]name[ARGS]{...}
				name := text[after+m[2] : after+m[3]]
				if v, end, ok := acDecodeJSON(text, after+m[1]); ok {
					if _, isMap := v.(map[string]any); isMap {
						b, _ := json.Marshal(v)
						add(acSpan{start: loc[0], end: end, name: name, args: string(b)})
					}
				}
				continue
			}
		}
		v, end, ok := acDecodeJSON(text, after)
		if !ok {
			continue
		}
		for i, c := range acCallsFromValue(v) {
			c.start, c.end = loc[0], end
			if i > 0 {
				c.start = end
			}
			add(c)
		}
	}

	// 4. gpt-oss harmony leak: to=functions.name ... <|message|>{json}
	for _, loc := range reTCHarmony.FindAllStringSubmatchIndex(text, -1) {
		name := text[loc[2]:loc[3]]
		brace := strings.IndexByte(text[loc[1]:], '{')
		if brace < 0 || brace > 200 {
			continue
		}
		v, end, ok := acDecodeJSON(text, loc[1]+brace)
		if !ok {
			continue
		}
		if _, isMap := v.(map[string]any); !isMap {
			continue
		}
		b, _ := json.Marshal(v)
		start := loc[0]
		if i := strings.LastIndex(text[:start], "<|start|>"); i >= 0 && start-i < 60 {
			start = i
		} else if i := strings.LastIndex(text[:start], "<|channel|>"); i >= 0 && start-i < 60 {
			start = i
		}
		add(acSpan{start: start, end: end, name: name, args: string(b)})
	}

	// 5. a ``` fenced block (json / tool_call / none) holding exactly one call object
	for _, r := range acFenceRanges(text) {
		block := text[r[0]:r[1]]
		nl := strings.IndexByte(block, '\n')
		if nl < 0 {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(strings.TrimLeft(block[:nl], " \t`")))
		if lang != "" && lang != "json" && lang != "tool_call" && lang != "tool_code" && lang != "jsonc" {
			continue
		}
		inner := strings.TrimSuffix(strings.TrimRight(block[nl+1:], " \t\r\n"), "```")
		v, e, ok := acDecodeJSON(inner, 0)
		if !ok || strings.TrimSpace(inner[e:]) != "" {
			continue
		}
		for i, c := range acCallsFromValue(v) {
			c.start, c.end, c.fenced = r[0], r[1], true
			if i > 0 {
				c.start = r[1]
			}
			add(c)
		}
	}

	// 6. the whole message is one call object (or an array of them)
	if len(spans) == 0 {
		t := strings.TrimSpace(text)
		if v, e, ok := acDecodeJSON(t, 0); ok && strings.TrimSpace(t[e:]) == "" {
			for i, c := range acCallsFromValue(v) {
				c.start, c.end = 0, len(text)
				if i > 0 {
					c.start = len(text)
				}
				spans = append(spans, c)
			}
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return spans
}

// parseTextToolCalls turns text-format tool calls into ToolCalls.
//
// known maps a name to the tool name to use, or "" when the tool does not exist
// (nil accepts every name verbatim). With strict set (models that have native
// function calling, bug 69) a call only counts when it is not inside a ```
// code example and nothing but whitespace follows the last call: an answer that
// merely shows an example of the syntax is left alone.
func parseTextToolCalls(text string, known func(string) string, strict bool) ([]ToolCall, string) {
	spans := acFindTextCalls(text)
	if len(spans) == 0 {
		return nil, strings.TrimSpace(text)
	}
	fences := acFenceRanges(text)
	var keep []acSpan
	for _, s := range spans {
		if strict && !s.fenced && acInRanges(s.start, fences) {
			continue
		}
		name := s.name
		if known != nil {
			name = known(s.name)
			if name == "" {
				continue
			}
		}
		s.name = name
		keep = append(keep, s)
	}
	if len(keep) == 0 {
		return nil, strings.TrimSpace(text)
	}
	if strict {
		last := 0
		for _, s := range keep {
			last = max(last, s.end)
		}
		tail := reTCTrailer.ReplaceAllString(text[last:], "")
		if strings.TrimSpace(tail) != "" {
			return nil, strings.TrimSpace(text)
		}
	}
	var b strings.Builder
	var calls []ToolCall
	pos := 0
	for _, s := range keep {
		if s.start > pos {
			b.WriteString(text[pos:s.start])
		}
		pos = max(pos, s.end)
		calls = append(calls, ToolCall{ID: acTxtID(), Name: s.name, Args: s.args})
	}
	b.WriteString(text[min(pos, len(text)):])
	rest := reTCTrailer.ReplaceAllString(strings.TrimSpace(b.String()), "")
	rest = strings.TrimSpace(strings.NewReplacer("<|call|>", "", "<|end|>", "", "<|eom_id|>", "", "<|eot_id|>", "", "<tool_call>", "", "</tool_call>", "").Replace(rest))
	return calls, rest
}

// extractTextToolCalls recovers text tool calls in every supported format,
// without name validation or the strict checks (see parseTextToolCalls).
func extractTextToolCalls(text string) ([]ToolCall, string) {
	return parseTextToolCalls(text, nil, false)
}

// ---------- text tools mode ----------

const acTextToolsHead = `<tool_calling>
Native function calling is not available. To call a tool, write exactly:
<tool_call>{"name": "TOOL_NAME", "arguments": {"param": "value"}}</tool_call>
Rules: one <tool_call> block per call (several blocks are fine), valid JSON only, and nothing after the last block. Then stop and wait: the results come back in the next user message as <tool_result name="..." id="...">...</tool_result>. When you need no tool, just answer normally.
Available tools (parameters marked * are required):
`

// acTextToolsBlock describes the tools for a model that has no native function
// calling. It depends only on specs, so it is byte-stable for a session.
func acTextToolsBlock(specs []ToolSpec) string {
	var b strings.Builder
	b.WriteString(acTextToolsHead)
	for _, s := range specs {
		var params []string
		req := map[string]bool{}
		props := map[string]any{}
		if s.Params != nil {
			if r, ok := s.Params["required"].([]string); ok {
				for _, k := range r {
					req[k] = true
				}
			}
			if p, ok := s.Params["properties"].(map[string]any); ok {
				props = p
			}
		}
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			typ := ""
			if pm, ok := props[k].(map[string]any); ok {
				typ, _ = pm["type"].(string)
			}
			p := k
			if req[k] {
				p += "*"
			}
			if typ != "" && typ != "string" {
				p += ":" + typ
			}
			params = append(params, p)
		}
		d := strings.TrimSpace(acSanitizeDesc(s.Description))
		if i := strings.Index(d, ". "); i > 0 && i < 220 {
			d = d[:i+1]
		} else if len(d) > 220 {
			d = d[:220] + "…"
		}
		fmt.Fprintf(&b, "- %s(%s): %s\n", s.Name, strings.Join(params, ", "), d)
	}
	b.WriteString("</tool_calling>\n")
	return b.String()
}

// acTextHistory rewrites a native-tools history for a text-tools request:
// assistant tool calls become <tool_call> text and tool results become user
// messages. Consecutive user messages are merged. The input is not modified.
func acTextHistory(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	push := func(m Message) {
		if n := len(out); n > 0 && m.Role == "user" && out[n-1].Role == "user" {
			prev := &out[n-1]
			prev.Content = strings.TrimSpace(prev.Content + "\n\n" + m.Content)
			prev.Images = append(append([]ImagePart(nil), prev.Images...), m.Images...)
			return
		}
		out = append(out, m)
	}
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) == 0 {
				push(m)
				continue
			}
			c := m
			var b strings.Builder
			b.WriteString(strings.TrimSpace(m.Content))
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Args)
				if !json.Valid(args) {
					q, _ := json.Marshal(tc.Args)
					args = q
				}
				body, _ := json.Marshal(struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}{tc.Name, args})
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString("<tool_call>" + string(body) + "</tool_call>")
			}
			c.Content, c.ToolCalls = b.String(), nil
			push(c)
		case "tool":
			push(Message{Role: "user", Content: fmt.Sprintf("<tool_result name=%q id=%q>\n%s\n</tool_result>", m.ToolName, m.ToolCallID, m.Content), Images: m.Images})
		default:
			push(m)
		}
	}
	return out
}

// acSanitizeDesc strips control characters (other than newline/tab) from text
// that comes from a third party, such as an MCP tool description.
func acSanitizeDesc(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e || r == 0x202d || r == 0x2066 || r == 0x2067 || r == 0x2068 || r == 0x2069 {
			return -1
		}
		return r
	}, s)
}

// acToolsUnsupported reports whether an HTTP error says the endpoint or model
// does not accept the tools parameter (then the agent switches to text tools).
func acToolsUnsupported(ae *APIError) bool {
	if ae == nil || (ae.Status != 400 && ae.Status != 404 && ae.Status != 422 && ae.Status != 500 && ae.Status != 501) {
		return false
	}
	b := strings.ToLower(ae.Body)
	if !strings.Contains(b, "tool") && !strings.Contains(b, "function call") && !strings.Contains(b, "function_call") {
		return false
	}
	for _, k := range []string{"not support", "unsupported", "does not support", "doesn't support", "no endpoints found that support tool", "--jinja", "not supported", "not enabled", "not available"} {
		if strings.Contains(b, k) {
			return true
		}
	}
	return false
}
