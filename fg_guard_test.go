package main

import (
	"strings"
	"testing"
)

func TestFgKind(t *testing.T) {
	server := []string{"python3 -m http.server 8000", "cd app && npm run dev", "uvicorn app:app --port 8000", "flask run", "python manage.py runserver", "docker compose up", "php -S 0.0.0.0:8080"}
	follow := []string{"ping 8.8.8.8", "tail -f /var/log/syslog", "journalctl -f -u ssh", "watch -n1 date"}
	none := []string{"ping -c 4 8.8.8.8", "tail -n 50 x.log", "npm test", "npm run build", "docker compose up -d", "timeout 10 python3 -m http.server", "grep uvicorn requirements.txt", "ps aux | grep http.server", "python3 -m http.server --help", "top -b -n 1", "python3 app.py", "pip install uvicorn"}
	for _, c := range server {
		if k, _ := fgKind(c); k != "server" {
			t.Errorf("%q: kind %q, want server", c, k)
		}
	}
	for _, c := range follow {
		if k, _ := fgKind(c); k != "follow" {
			t.Errorf("%q: kind %q, want follow", c, k)
		}
	}
	for _, c := range none {
		if k, _ := fgKind(c); k != "" {
			t.Errorf("%q: kind %q, want none", c, k)
		}
	}
}

// A server in the foreground becomes a background job (the shell is free).
func TestBashServerGoesBackground(t *testing.T) {
	srv := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"python3 -m http.server 0 --bind 127.0.0.1"}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{srv}}, fakeResp{Text: "subiu"}), nil)
	defer h.ag.tools.KillJobs()
	if err := h.run(t, "sobe um servidor http aqui"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[1].toolResults()["b1"]
	if !strings.Contains(res, "started it as a background job") {
		t.Errorf("tool result: %q", res)
	}
}

// The same bash call with the same big output: the second result is a stub.
func TestRepeatedBigResultStubbed(t *testing.T) {
	cmd := fakeCall{ID: "b1", Name: "bash", Args: `{"command":"seq 1 400"}`}
	cmd2 := fakeCall{ID: "b2", Name: "bash", Args: `{"command":"seq 1 400"}`}
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{cmd}}, fakeResp{Calls: []fakeCall{cmd2}}, fakeResp{Text: "ok"}), nil)
	if err := h.run(t, "conta"); err != nil {
		t.Fatal(err)
	}
	r1 := h.fs.requests()[1].toolResults()["b1"]
	r2 := h.fs.requests()[2].toolResults()["b2"]
	if !strings.Contains(r1, "\n200\n") || !strings.Contains(r2, "byte-for-byte unchanged") || strings.Contains(r2, "\n200\n") {
		t.Errorf("first result must be full, second a stub:\n%.200s\n---\n%.300s", r1, r2)
	}
}
