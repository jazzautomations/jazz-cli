package main

// pytest_detect.go: Python projects without a pytest marker file (pytest.ini,
// conftest.py, [tool.pytest]) used to get `python3 -m unittest` as their test
// command even when the tests are pytest-style (bare `def test_*` functions,
// `import pytest`, fixtures such as tmp_path). unittest finds nothing there
// (exit 5, NO TESTS RAN), so the end-of-turn suite said nothing and the model
// rewrote the user's tests into unittest (QA lane "python"). hgTestCommand now
// picks pytest for such tests when it is importable; when it is not, the
// environment line says so instead of pointing at a command that runs nothing.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	hgPyStyleMaxFiles = 40       // test files read when classifying the style
	hgPyStyleMaxBytes = 64 << 10 // bytes read per file
)

// hgPytestStyleRE: a line only pytest-style tests have — pytest imported or
// used, a test function at module level (unittest tests are methods), or a
// built-in fixture as a parameter.
var hgPytestStyleRE = lazyRx(`(?m)^(?:import\s+pytest\b|from\s+_?pytest\b)|^(?:async\s+)?def\s+test\w*\s*\(|^\s*@pytest\.|^\s*(?:async\s+)?def\s+\w+\s*\([^)]*\b(?:tmp_path|tmpdir|capsys|capfd|monkeypatch|caplog)\b`)

// hgPyTests: root has Python test files (test*.py), and whether they look
// pytest-style.
func hgPyTests(root string) (py, pytestStyle bool) {
	read := 0
	hgWalk(root, func(abs, rel string) {
		if pytestStyle || !strings.HasSuffix(abs, ".py") || hgKind(rel) != "test" || !strings.HasPrefix(filepath.Base(abs), "test") {
			return
		}
		py = true
		if read >= hgPyStyleMaxFiles {
			return
		}
		read++
		f, err := os.Open(abs)
		if err != nil {
			return
		}
		defer f.Close()
		buf := make([]byte, hgPyStyleMaxBytes)
		n, _ := io.ReadFull(f, buf)
		pytestStyle = hgPytestStyleRE.Match(buf[:n])
	})
	return py, pytestStyle
}

var (
	hgPytestOnce sync.Once
	hgPytestOK   bool
	// hgPytestProbe answers "is pytest importable by python3?" (tests replace it).
	hgPytestProbe = hgProbePytest
)

// hgPytestAvailable: the probe's answer, computed once per process.
func hgPytestAvailable() bool {
	hgPytestOnce.Do(func() { hgPytestOK = hgPytestProbe() })
	return hgPytestOK
}

// hgProbePytest runs `python3 -c "import pytest"` from a fresh empty
// directory, so a pytest.py planted in the project (or in a shared temp dir)
// is never what gets imported.
func hgProbePytest() bool {
	dir, err := os.MkdirTemp("", "jazz-pyprobe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = qgRun(ctx, dir, nil, "python3", "-c", "import pytest")
	return err == nil
}

// hgPyTestNote is the environment-line note for a project whose tests look
// pytest-style while pytest is not installed ("" otherwise).
func hgPyTestNote(root, testCmd string) string {
	if testCmd != "python3 -m unittest" {
		return ""
	}
	if _, ps := hgPyTests(root); !ps || hgPytestAvailable() {
		return ""
	}
	return "the tests look pytest-style (bare test functions / import pytest / fixtures) but pytest is not installed here, so unittest finds none of them; say so in your answer, and do not rewrite the user's tests into unittest unless asked"
}
