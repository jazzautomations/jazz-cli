package main

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// TestLazyRxAllCompile keeps the old "bad pattern fails at start" guarantee:
// every package-level lazy regexp must compile.
func TestLazyRxAllCompile(t *testing.T) {
	if len(lazyRxAll) < 100 {
		t.Fatalf("only %d lazy regexps registered; the init-time registry is broken", len(lazyRxAll))
	}
	for _, l := range lazyRxAll {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("pattern %q: %v", l.src, r)
				}
			}()
			if l.R().String() != l.src {
				t.Errorf("String mismatch for %q", l.src)
			}
		}()
	}
}

// TestInitMainBudget is the regression lock of item 1 of the low-level study:
// the package init (everything before main() runs, on every command) must
// stay under 1 MB and 5k allocations. It re-runs this test binary with
// GODEBUG=inittrace=1 and reads the line of this package.
func TestInitMainBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	out, _ := cmd.CombinedOutput()
	re := regexp.MustCompile(`(?m)^init jazzcli @[\d.]+ m?s, [\d.]+ m?s clock, (\d+) bytes, (\d+) allocs`)
	m := re.FindStringSubmatch(string(out))
	if m == nil {
		t.Skipf("no inittrace line for jazzcli in:\n%.400s", out)
	}
	bytes, _ := strconv.Atoi(m[1])
	allocs, _ := strconv.Atoi(m[2])
	t.Logf("init jazzcli: %d bytes, %d allocs", bytes, allocs)
	if bytes > 1<<20 || allocs > 5000 {
		t.Fatalf("package init too heavy: %d bytes, %d allocs (budget 1 MB / 5k); a package-level regexp.MustCompile or big literal crept back — use lazyRx", bytes, allocs)
	}
}
