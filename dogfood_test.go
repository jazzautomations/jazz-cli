//go:build darwin || linux

package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDogfoodOffline drives the real interactive jazz against the real free
// model (keyless kilo default) in an 86x27 PTY, with the network "cut" by an
// HTTPS proxy on a dead port; after 30 s a working CONNECT proxy appears on
// that port (the network is back). It logs what the user sees, with times.
// Opt-in: JAZZ_DOGFOOD=1 (needs the internet).
func TestDogfoodOffline(t *testing.T) {
	if os.Getenv("JAZZ_DOGFOOD") != "1" {
		t.Skip("JAZZ_DOGFOOD=1 runs it (internet)")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens: every connection is refused
	s := ptyStartWith(t, "xterm-256color", nil, "HTTPS_PROXY=http://"+addr, "https_proxy=http://"+addr, "NO_PROXY=", "no_proxy=", "JAZZ_TEST_SANDBOX=0")
	t0 := time.Now()
	logs := []string{}
	var lmu sync.Mutex
	note := func(format string, a ...any) {
		lmu.Lock()
		logs = append(logs, fmt.Sprintf("[%6.2fs] ", time.Since(t0).Seconds())+fmt.Sprintf(format, a...))
		lmu.Unlock()
	}
	prompt := func(sc string) bool {
		return strings.Contains(sc, "? atalhos") && strings.Contains(sc, "▸▸ auto ·")
	}
	s.trust()
	s.waitFor("the input box", 20*time.Second, prompt)
	note("tela inicial:\n%s", s.screen())
	s.typeSlow("oi")
	tEnter := time.Now()
	s.write("\r")
	d := s.waitFor("the status line", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "esc pra interromper") || strings.Contains(sc, "sem rede")
	})
	note("status %s depois do Enter: %q", d.Round(time.Millisecond), ptyLineWith(s.screen(), "("))
	s.waitFor("the offline banner", 30*time.Second, func(sc string) bool {
		return strings.Contains(strings.Join(s.all(), "\n"), "sem rede → modo offline") && prompt(sc)
	})
	note("modo offline %s depois do Enter:\n%s", time.Since(tEnter).Round(time.Millisecond), strings.Join(s.all()[ptyIndex(s.all(), "› oi"):], "\n"))
	time.Sleep(5 * time.Second)
	note("rodapé com contagem: %q", ptyLineWith(s.screen(), "nova tentativa em"))
	// a request while offline: the numbered menu
	s.typeSlow("por que o build quebra?")
	s.write("\r")
	s.waitFor("the menu", 10*time.Second, func(sc string) bool { return strings.Contains(sc, "o que dá pra fazer") && prompt(sc) })
	note("pedido offline:\n%s", strings.Join(s.all()[ptyIndex(s.all(), "› por que o build"):], "\n"))
	// the network comes back: a CONNECT proxy on the dead port
	back := time.Now()
	stop := dfProxy(t, addr)
	defer stop()
	note("proxy no ar (rede de volta)")
	s.waitFor("the online notice", 45*time.Second, func(sc string) bool { return strings.Contains(sc, "rede de volta → modo online") })
	note("aviso de volta %s depois da rede voltar: %q", time.Since(back).Round(time.Second), ptyLineWith(s.screen(), "rede de volta"))
	s.typeSlow("responda apenas: tudo certo")
	tReq := time.Now()
	s.write("\r")
	s.waitFor("the answer", 180*time.Second, func(sc string) bool {
		return strings.Contains(sc, "●") && strings.Contains(strings.ToLower(sc), "tudo certo") && prompt(sc)
	})
	note("resposta do modelo em %s:\n%s", time.Since(tReq).Round(time.Millisecond), s.screen())
	// online: sample the status line while a real request runs
	s.typeSlow("explique em duas frases o que é um mutex")
	tReq = time.Now()
	s.write("\r")
	seen := map[string]bool{}
	for time.Since(tReq) < 120*time.Second {
		sc := s.screen()
		if l := ptyLineWith(sc, "esc pra interromper"); l == "" {
			if l = ptyLineWith(sc, "esc cancela]"); l == "" {
				l = ptyLineWith(sc, "tokens")
			}
			if l != "" && !seen[l] {
				seen[l] = true
				note("status: %q", l)
			}
		} else if !seen[l] {
			seen[l] = true
			note("status: %q", l)
		}
		all := s.all()
		if i := ptyIndex(all, "› explique em duas frases"); i > 0 && prompt(sc) && strings.Contains(strings.Join(all[i:], "\n"), "●") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	note("resposta em %s:\n%s", time.Since(tReq).Round(time.Millisecond), s.screen())
	s.write("\x04")
	lmu.Lock()
	t.Log("\n" + strings.Join(logs, "\n"))
	lmu.Unlock()
}

func ptyLineWith(sc, sub string) string {
	for _, l := range strings.Split(sc, "\n") {
		if strings.Contains(l, sub) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// dfProxy serves HTTP CONNECT on addr (a minimal HTTPS proxy).
func dfProxy(t *testing.T, addr string) func() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		up, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() { io.Copy(up, c); up.Close() }()
		go func() { io.Copy(c, up); c.Close() }()
	})}
	go srv.Serve(ln)
	return func() { srv.Close() }
}

func ptyIndex(lines []string, prefix string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], prefix) {
			return i
		}
	}
	return 0
}
