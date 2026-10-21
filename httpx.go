package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var userAgent = "jazz-cli/" + version

// One shared client: keep-alive reuses the TLS session, which matters a lot on a slow CPU.
var httpClient = &http.Client{
	Transport: auTransport("provider", &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   25 * time.Second,
		ResponseHeaderTimeout: 240 * time.Second,
	}),
}

var errStreamIdle = errors.New("o modelo parou de responder (stream sem dados por muito tempo)")

// errFirstByte: no streamed data before the first-byte deadline (WithFirstByte).
// It wraps errStreamIdle, so everything that handles a stall still matches it.
var errFirstByte = fmt.Errorf("%w: nada chegou antes do prazo do primeiro byte", errStreamIdle)

type pvFirstByteKey struct{}

// WithFirstByte makes postStream give up when no SSE data line arrives within d
// of sending the request (keep-alive comments do not count). d <= 0 = no deadline.
func WithFirstByte(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, pvFirstByteKey{}, d)
}

// Bounds for streamed responses (bug 60): one SSE line/event and one whole stream.
// A 2 GB i386 box must not be run out of memory by a broken proxy.
var (
	pvMaxSSELine   = 8 << 20
	pvMaxSSEStream = int64(64 << 20)
)

// postStream sends a JSON POST and returns the open response, with a derived context
// that is cancelled if the stream stays silent for `idle`. Call the returned
// touch(data) on every received line (data = it carried payload, not a keep-alive)
// and done() when finished. With WithFirstByte on ctx, the stream must deliver its
// first data line within that deadline or it fails with errFirstByte.
//
// Non-200 answers become *APIError with RetryAfter/Daily filled from headers and body (G4).
// A 200 answer that is JSON instead of SSE (bug 66) is either an error (APIError) or a
// non-streamed OpenAI completion, which is re-encoded as a one-chunk SSE stream.
func postStream(ctx context.Context, url string, headers map[string]string, body any, idle time.Duration) (resp *http.Response, touch func(data bool), done func(), idleCtx context.Context, err error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	fb, _ := ctx.Value(pvFirstByteKey{}).(time.Duration)
	sctx, cancel := context.WithCancelCause(ctx)
	var gotData atomic.Bool
	first := idle + 60*time.Second // first byte may take longer
	if fb > 0 {
		first = fb
	}
	timer := time.AfterFunc(first, func() {
		if fb > 0 && !gotData.Load() {
			cancel(errFirstByte)
			return
		}
		cancel(errStreamIdle)
	})
	touch = func(data bool) {
		if data && !gotData.Load() {
			gotData.Store(true)
			pvPhaseOf(ctx)("gerando")
		}
		if fb > 0 && !gotData.Load() {
			return // keep-alives and headers do not satisfy the first-byte deadline
		}
		timer.Reset(idle)
	}
	done = func() { timer.Stop(); cancel(nil) }
	phase := pvPhaseOf(ctx)
	var connected atomic.Bool
	phase("conectando")
	stopWatch := pvConnectWatch(sctx, url, &connected, phase, cancel)
	defer stopWatch()
	req, err := http.NewRequestWithContext(pvTrace(sctx, phase, &connected), "POST", url, bytes.NewReader(b))
	if err != nil {
		done()
		return nil, nil, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	dbg("request", map[string]any{"url": url, "bytes": len(b)})
	resp, err = httpClient.Do(req)
	if err != nil {
		// Check idleness BEFORE done() cancels sctx, or every dial/DNS/TLS
		// failure would look like an idle stream (bug 17).
		idleHit := ctx.Err() == nil && sctx.Err() != nil
		done()
		if ctx.Err() != nil {
			return nil, nil, nil, nil, ctx.Err()
		}
		if idleHit {
			if context.Cause(sctx) == error(errNoNet) {
				return nil, nil, nil, nil, errNoNet
			}
			return nil, nil, nil, nil, pvIdleCause(sctx)
		}
		return nil, nil, nil, nil, &APIError{Status: 0, Body: err.Error()}
	}
	dbg("status", map[string]any{"url": url, "status": resp.StatusCode})
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		done()
		return nil, nil, nil, nil, pvNewAPIError(resp.StatusCode, resp.Header, raw)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "json") && !strings.Contains(ct, "event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, int64(pvMaxSSELine)))
		resp.Body.Close()
		sse, aerr := pvJSONAsSSE(resp.Header, raw)
		if aerr != nil {
			done()
			return nil, nil, nil, nil, aerr
		}
		resp.Body = io.NopCloser(bytes.NewReader(sse))
	}
	resp.Body = &pvCapReader{r: resp.Body, left: pvMaxSSEStream}
	touch(false)
	return resp, touch, done, sctx, nil
}

// pvIdleCause is errFirstByte or errStreamIdle, whichever cancelled the stream.
func pvIdleCause(sctx context.Context) error {
	if c := context.Cause(sctx); errors.Is(c, errFirstByte) {
		return errFirstByte
	}
	return errStreamIdle
}

// pvCapReader stops a response after a byte budget (bug 60).
type pvCapReader struct {
	r    io.ReadCloser
	left int64
}

var errStreamTooBig = &APIError{Status: 0, Body: "stream grande demais (limite de 64 MB por resposta)"}

func (c *pvCapReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errStreamTooBig
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *pvCapReader) Close() error { return c.r.Close() }

// pvJSONAsSSE handles a 200 response that is plain JSON. An {"error":…} body becomes
// an APIError; an OpenAI chat completion becomes one SSE chunk + [DONE].
func pvJSONAsSSE(h http.Header, raw []byte) ([]byte, error) {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message struct {
				Content          string          `json:"content"`
				Reasoning        string          `json:"reasoning"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, &APIError{Status: 502, Body: "resposta não-SSE inválida: " + trimBody(raw)}
	}
	if len(v.Error) > 0 && string(v.Error) != "null" {
		st := 502
		var e struct {
			Code any `json:"code"`
		}
		if json.Unmarshal(v.Error, &e) == nil {
			if n := pvCodeInt(e.Code); n >= 400 {
				st = n
			}
		}
		return nil, pvNewAPIError(st, h, raw)
	}
	if len(v.Choices) == 0 {
		return nil, &APIError{Status: 502, Body: "resposta não-SSE sem choices: " + trimBody(raw)}
	}
	ch := v.Choices[0]
	var tcs []map[string]any
	if len(ch.Message.ToolCalls) > 0 {
		_ = json.Unmarshal(ch.Message.ToolCalls, &tcs)
		for i, tc := range tcs {
			tc["index"] = i
		}
	}
	delta := map[string]any{"content": ch.Message.Content, "reasoning": ch.Message.Reasoning, "reasoning_content": ch.Message.ReasoningContent}
	if len(tcs) > 0 {
		delta["tool_calls"] = tcs
	}
	chunk := map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": ch.FinishReason}}}
	if len(v.Usage) > 0 {
		chunk["usage"] = v.Usage
	}
	cb, _ := json.Marshal(chunk)
	return []byte("data: " + string(cb) + "\n\ndata: [DONE]\n\n"), nil
}

func pvCodeInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

// pvNewAPIError builds an APIError and fills RetryAfter / Daily (G4).
func pvNewAPIError(status int, h http.Header, raw []byte) *APIError {
	body := strings.TrimSpace(string(raw))
	ae := &APIError{Status: status, Body: body}
	ae.RetryAfter = pvRetryAfter(h, body, time.Now())
	ae.Daily = pvIsDaily(status, body)
	return ae
}

var pvRetryDelayRE = regexp.MustCompile(`"retryDelay"\s*:\s*"([0-9.]+)s"`)

// pvRetryAfter reads retry-after-ms, retry-after (seconds or HTTP date),
// x-ratelimit-reset* (epoch ms, epoch s, relative seconds or Go durations such as
// Groq's "2m59.56s"), OpenRouter's error.metadata.headers and Gemini's RetryInfo.
func pvRetryAfter(h http.Header, body string, now time.Time) time.Duration {
	if h != nil {
		if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
			if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
				return time.Duration(ms * float64(time.Millisecond))
			}
		}
		if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
			if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
				return time.Duration(s * float64(time.Second))
			}
			if t, err := http.ParseTime(v); err == nil && t.After(now) {
				return t.Sub(now)
			}
		}
		for _, k := range []string{"X-RateLimit-Reset", "X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens"} {
			if d := pvParseReset(h.Get(k), now); d > 0 {
				return d
			}
		}
	}
	if body != "" && body[0] == '{' {
		var e struct {
			Error struct {
				Metadata struct {
					Headers map[string]any `json:"headers"`
				} `json:"metadata"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(body), &e) == nil {
			for k, v := range e.Error.Metadata.Headers {
				lk := strings.ToLower(k)
				if lk == "x-ratelimit-reset" || lk == "retry-after" {
					if d := pvParseReset(fmt.Sprint(v), now); d > 0 {
						return d
					}
				}
			}
		}
		if m := pvRetryDelayRE.FindStringSubmatch(body); m != nil {
			if s, err := strconv.ParseFloat(m[1], 64); err == nil && s > 0 {
				return time.Duration(s * float64(time.Second))
			}
		}
	}
	return 0
}

// pvParseReset understands the many spellings of a rate-limit reset.
func pvParseReset(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		switch {
		case f <= 0:
			return 0
		case f < 1e9: // relative seconds
			return time.Duration(f * float64(time.Second))
		case f < 1e11: // epoch seconds
			if d := time.Unix(int64(f), 0).Sub(now); d > 0 {
				return d
			}
			return 0
		default: // epoch milliseconds
			if d := time.UnixMilli(int64(f)).Sub(now); d > 0 {
				return d
			}
			return 0
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

var pvDailyRE = regexp.MustCompile(`(?i)free-models-per-day|per[ -_]?day|perday|\(rpd\)|\(tpd\)|requests per day|tokens per day|daily (free )?(allocation|limit|quota)|quota_for_entity_exceeded|insufficient_quota|monthly limit|trial key.*month|neurons`)

// pvIsDaily reports a quota that will not come back within minutes.
func pvIsDaily(status int, body string) bool {
	if status != 429 && status != 402 && status != 403 {
		return false
	}
	return pvDailyRE.MatchString(body)
}

// readSSE parses a Server-Sent Events stream, calling fn(event, data) per event.
// Lines and events are bounded by pvMaxSSELine (bug 60).
func readSSE(r io.Reader, touch func(data bool), fn func(event, data string) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var event string
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		d := data.String()
		data.Reset()
		ev := event
		event = ""
		if debugOn {
			dbg("sse", map[string]any{"event": ev, "bytes": len(d)})
		}
		return fn(ev, d)
	}
	for {
		line, err := pvReadLine(br, pvMaxSSELine)
		if len(line) > 0 {
			touch(strings.HasPrefix(line, "data:"))
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if e := flush(); e != nil {
					return e
				}
			case strings.HasPrefix(line, ":"): // comment / keep-alive
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(line[5:], " "))
				if data.Len() > pvMaxSSELine {
					return &APIError{Status: 0, Body: "evento SSE grande demais"}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return flush()
			}
			return err
		}
	}
}

// pvReadLine reads one line (with its newline) but never more than max bytes.
func pvReadLine(br *bufio.Reader, max int) (string, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			return "", &APIError{Status: 0, Body: "linha SSE grande demais"}
		}
		if err == bufio.ErrBufferFull {
			buf = append(buf, chunk...)
			continue
		}
		if buf == nil {
			return string(chunk), err
		}
		buf = append(buf, chunk...)
		return string(buf), err
	}
}

// streamErr converts a read error into the most useful error for the caller.
func streamErr(parent, idleCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if idleCtx != nil && idleCtx.Err() != nil {
		return pvIdleCause(idleCtx)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return &APIError{Status: 0, Body: "stream interrompido: " + err.Error()}
}

func getJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	_, err := pvGetJSON(ctx, url, headers, out, 30*time.Second)
	return err
}

// pvGetJSON is getJSON with a timeout and the response headers.
func pvGetJSON(ctx context.Context, url string, headers map[string]string, out any, timeout time.Duration) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != 200 {
		return resp.Header, pvNewAPIError(resp.StatusCode, resp.Header, raw)
	}
	if out == nil {
		return resp.Header, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.Header, fmt.Errorf("resposta inválida: %w", err)
	}
	return resp.Header, nil
}

// pvPostJSON sends a small non-streaming JSON POST (probes, Ollama /api/show).
func pvPostJSON(ctx context.Context, url string, headers map[string]string, body, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return pvNewAPIError(resp.StatusCode, resp.Header, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("resposta inválida: %w", err)
		}
	}
	return nil
}
