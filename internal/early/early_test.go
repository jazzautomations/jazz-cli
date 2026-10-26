package early

import (
	"runtime/debug"
	"testing"
)

func TestShortArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"version"}, true},
		{[]string{"--version"}, true},
		{[]string{"-p", "git status"}, true},
		{[]string{"--offline", "-p", "x"}, true},
		{[]string{"--print", "x"}, true},
		{[]string{}, false},
		{[]string{"sessions"}, false},
		{[]string{"eval", "offline"}, false},
		{[]string{"script", "version"}, false},
		{[]string{"--", "-p"}, false},
	} {
		if got := ShortArgs(c.args); got != c.want {
			t.Errorf("ShortArgs(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestOffRestore(t *testing.T) {
	if userGOGC {
		t.Skip("GOGC set by the environment")
	}
	Off()
	if !IsOff() {
		t.Fatal("Off did not take")
	}
	if p := debug.SetGCPercent(-1); p != -1 {
		t.Fatalf("GC percent while off = %d", p)
	}
	Restore()
	Restore() // idempotent
	if IsOff() {
		t.Fatal("still off")
	}
	if p := debug.SetGCPercent(percent); p != percent {
		t.Fatalf("restored percent = %d, want %d", p, percent)
	}
}
