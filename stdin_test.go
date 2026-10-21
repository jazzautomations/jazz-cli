package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// An open pipe that never sends data nor closes must not hold the run.
func TestStdinOpenPipeDoesNotBlock(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	start := time.Now()
	if got := readStdinPrompt(r, false, 100*time.Millisecond, 100*time.Millisecond); got != "" {
		t.Fatalf("got %q", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("blocked %s", d)
	}
}

// Data on a pipe left open: taken after the idle gap, without waiting for EOF.
func TestStdinDataThenOpenPipe(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	go func() {
		w.Write([]byte("corrija "))
		time.Sleep(30 * time.Millisecond)
		w.Write([]byte("o bug"))
	}()
	start := time.Now()
	if got := readStdinPrompt(r, false, time.Second, 200*time.Millisecond); got != "corrija o bug" {
		t.Fatalf("got %q", got)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("blocked %s", d)
	}
}

func TestStdinClosedPipeAndForce(t *testing.T) {
	if got := readStdinPrompt(strings.NewReader("echo prompt\n"), false, time.Second, time.Second); got != "echo prompt\n" {
		t.Fatalf("closed pipe: %q", got)
	}
	if got := readStdinPrompt(strings.NewReader(""), false, time.Second, time.Second); got != "" {
		t.Fatalf("/dev/null: %q", got)
	}
	// "-" / --stdin: read to EOF even when the data is slow to come
	r, w := io.Pipe()
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte("tarde"))
		w.Close()
	}()
	if got := readStdinPrompt(r, true, 10*time.Millisecond, 10*time.Millisecond); got != "tarde" {
		t.Fatalf("force: %q", got)
	}
}
