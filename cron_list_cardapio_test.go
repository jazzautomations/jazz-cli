//go:build !windows

package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCardapioCronList drives cron_list through tb.Exec("script", ...): the crontab
// parser on synthetic user and system fixtures (schedule -> human, env lines ignored,
// commands cut at 80 chars) and the live read-only sweep of this box (shape only).
func TestCardapioCronList(t *testing.T) {
	tb, root := scTestBox(t) // skips if python3 is absent
	scWrite(t, root, "crontab_user.txt", scFixture(t, "crontab_user.txt"))
	scWrite(t, root, "crontab_system.txt", scFixture(t, "crontab_system.txt"))

	// user-format file: 8 jobs, SHELL/MAILTO lines are not jobs, sorted by schedule
	v, _ := scCall(t, tb, "cron_list", map[string]any{"path": "crontab_user.txt"})
	d := scDataOf(v)
	if !scOK(v) {
		t.Fatalf("user fixture not ok: %v", v)
	}
	jobs, _ := d["crontab"].([]any)
	if len(jobs) != 8 {
		t.Fatalf("crontab jobs = %d, want 8: %v", len(jobs), d["crontab"])
	}
	if sys, _ := d["system"].([]any); len(sys) != 0 {
		t.Errorf("a user file must not fill system: %v", sys)
	}
	if d["text"] != "crontab_user.txt: 8 jobs" || d["truncated"] != false || d["permission_denied"] != false {
		t.Errorf("text/flags: %v %v %v", d["text"], d["truncated"], d["permission_denied"])
	}
	want := map[string]string{
		"*/5 * * * *":  "every 5 min",
		"0 3 * * *":    "daily at 03:00",
		"15 * * * *":   "hourly at :15",
		"30 7 * * 1-5": "on weekdays at 07:30",
		"45 22 * * 0":  "weekly on Sun at 22:45",
		"0 0 1 * *":    "monthly on day 1 at 00:00",
		"@reboot":      "at boot",
		"@daily":       "daily at 00:00",
	}
	byRaw := map[string]map[string]any{}
	prev := ""
	for _, j := range jobs {
		m := j.(map[string]any)
		raw, _ := m["schedule"].(string)
		byRaw[raw] = m
		if raw < prev {
			t.Errorf("crontab not sorted by schedule: %q after %q", raw, prev)
		}
		prev = raw
		if m["source"] != "file:crontab_user.txt" {
			t.Errorf("source = %v", m["source"])
		}
		if cmd, _ := m["command"].(string); utf8.RuneCountInString(cmd) > 80 {
			t.Errorf("command not cut at 80 chars: %q", cmd)
		}
	}
	for raw, human := range want {
		m := byRaw[raw]
		if m == nil {
			t.Errorf("schedule %q missing", raw)
			continue
		}
		if m["human"] != human {
			t.Errorf("%q human = %q, want %q", raw, m["human"], human)
		}
	}
	if cmd, _ := byRaw["45 22 * * 0"]["command"].(string); !strings.HasSuffix(cmd, "…") || !strings.HasPrefix(cmd, "/home/ana/bin/weekly-digest.sh") {
		t.Errorf("long command must be clipped with an ellipsis: %q", cmd)
	}
	if byRaw["0 3 * * *"]["command"] != "/home/ana/bin/backup.sh --full >> /home/ana/backup.log 2>&1" {
		t.Errorf("command kept whole: %v", byRaw["0 3 * * *"]["command"])
	}

	// system format: the user field is split off, names in dow/mon fields understood
	v, _ = scCall(t, tb, "cron_list", map[string]any{"path": "crontab_system.txt", "format": "system"})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("system fixture not ok: %v", v)
	}
	sys, _ := d["system"].([]any)
	if len(sys) != 7 {
		t.Fatalf("system jobs = %d, want 7: %v", len(sys), d["system"])
	}
	if jobs, _ = d["crontab"].([]any); len(jobs) != 0 {
		t.Errorf("a system file must not fill crontab: %v", jobs)
	}
	users := map[string]int{}
	humans := map[string]string{}
	for _, j := range sys {
		m := j.(map[string]any)
		u, _ := m["user"].(string)
		users[u]++
		humans[fmt.Sprint(m["schedule"])] = fmt.Sprint(m["human"])
	}
	if users["root"] != 5 || users["ana"] != 1 || users["bruno"] != 1 {
		t.Errorf("users = %v", users)
	}
	for raw, human := range map[string]string{
		"*/10 8-18 * * 1-5": "every 10 min from 08 to 18 h on weekdays",
		"30 7-23 * * *":     "hourly at :30 from 07 to 23 h",
		"47 6 * * 7":        "weekly on Sun at 06:47",
		"52 6 1 * *":        "monthly on day 1 at 06:52",
		"0 4 15 6 *":        "yearly on Jun 15 at 04:00",
		"17 * * * *":        "hourly at :17",
	} {
		if humans[raw] != human {
			t.Errorf("%q human = %q, want %q", raw, humans[raw], human)
		}
	}
	if d["text"] != "crontab_system.txt: 7 jobs" {
		t.Errorf("system text = %v", d["text"])
	}

	// a shape the small grammar does not cover is "custom", never a guess
	scWrite(t, root, "odd.txt", "1 2 3 4 5 /bin/true\n0 9 * * mon,wed,fri /bin/true\n")
	v, _ = scCall(t, tb, "cron_list", map[string]any{"path": "odd.txt"})
	jobs, _ = scDataOf(v)["crontab"].([]any)
	if len(jobs) != 2 {
		t.Fatalf("odd jobs: %v", v)
	}
	if h0, h1 := jobs[1].(map[string]any)["human"], jobs[0].(map[string]any)["human"]; h0 != "custom" || h1 != "on Mon,Wed,Fri at 09:00" {
		t.Errorf("odd humans = %v / %v", h0, h1)
	}

	// missing file / bad format: clean failures
	v, _ = scCall(t, tb, "cron_list", map[string]any{"path": "nope.txt"})
	if scOK(v) || !strings.Contains(fmt.Sprint(v["error"]), "not found") {
		t.Errorf("missing path: %v", v)
	}

	// the live box: read-only sweep, always ok, every key of the contract present
	v, _ = scCall(t, tb, "cron_list", map[string]any{})
	d = scDataOf(v)
	if !scOK(v) {
		t.Fatalf("live cron_list not ok: %v", v)
	}
	for _, k := range []string{"crontab", "system", "timers", "periodic", "sources", "truncated", "permission_denied", "text"} {
		if _, ok := d[k]; !ok {
			t.Errorf("live data misses %q: %v", k, d)
		}
	}
	per, _ := d["periodic"].(map[string]any)
	for _, k := range []string{"hourly", "daily", "weekly", "monthly"} {
		if _, ok := per[k].([]any); !ok {
			t.Errorf("periodic.%s must be a list: %v", k, per)
		}
	}
	for _, tm := range mustList(d["timers"]) {
		m := tm.(map[string]any)
		if u, _ := m["unit"].(string); !strings.HasSuffix(u, ".timer") {
			t.Errorf("timer unit = %v", m["unit"])
		}
		for _, k := range []string{"next", "left", "last", "activates"} {
			if _, ok := m[k]; !ok {
				t.Errorf("timer misses %q: %v", k, m)
			}
		}
	}
	if text, _ := d["text"].(string); !strings.HasPrefix(text, "user crontab ") || !strings.Contains(text, " timers") {
		t.Errorf("live text = %q", text)
	}

	// user_only: only the user's crontab is consulted
	v, _ = scCall(t, tb, "cron_list", map[string]any{"user_only": true})
	d = scDataOf(v)
	if !scOK(v) || len(mustList(d["system"])) != 0 || len(mustList(d["timers"])) != 0 {
		t.Errorf("user_only must leave system/timers empty: %v", v)
	}
	if text, _ := d["text"].(string); !strings.HasPrefix(text, "user crontab ") || strings.Contains(text, "timers") {
		t.Errorf("user_only text = %q", text)
	}

	// limit: the total across lists is capped and truncated says so
	v, _ = scCall(t, tb, "cron_list", map[string]any{"limit": 1})
	d = scDataOf(v)
	n := len(mustList(d["crontab"])) + len(mustList(d["system"])) + len(mustList(d["timers"]))
	if n > 1 {
		t.Errorf("limit 1 returned %d entries", n)
	}
}

func mustList(v any) []any {
	l, _ := v.([]any)
	return l
}
