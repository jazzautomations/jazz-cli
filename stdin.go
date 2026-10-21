package main

// stdin.go: the prompt read from a piped stdin in -p mode, without ever
// blocking the run. Before, `jazz -p "x"` with an open stdin pipe that never
// sent data nor closed (a CI step, `ssh host jazz -p ...`, a parent process
// that keeps the pipe) hung forever in io.ReadAll, before --timeout was armed.
//
// Now: with "-" as an argument or --stdin, stdin is read to the end (the user
// asked for it). Otherwise, only when stdin is not a terminal, and only if
// data shows up within a short wait; after the first byte, reading stops at
// EOF or after an idle gap, so a pipe left open never holds the run.

import (
	"io"
	"time"
)

const (
	siMax       = 4 << 20                // bytes of prompt taken from stdin
	siWaitArg   = 300 * time.Millisecond // first-byte wait when the prompt came as an argument
	siWaitNoArg = 3 * time.Second        // first-byte wait when stdin is the only prompt
	siIdle      = time.Second            // gap after the last chunk that ends the read
)

// readStdinPrompt reads r as described above. force = "-" / --stdin: read to
// EOF (still capped at siMax). first = how long to wait for the first byte,
// idle = the gap that ends the read once data has arrived.
func readStdinPrompt(r io.Reader, force bool, first, idle time.Duration) string {
	if force {
		b, _ := io.ReadAll(io.LimitReader(r, siMax))
		return string(b)
	}
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 16)
	go func() { // may stay blocked on a pipe nobody closes: the process exits anyway
		lr := io.LimitReader(r, siMax)
		for {
			buf := make([]byte, 32<<10)
			n, err := lr.Read(buf)
			ch <- chunk{buf[:n], err}
			if err != nil {
				return
			}
		}
	}()
	var out []byte
	wait := first
	for {
		select {
		case c := <-ch:
			out = append(out, c.b...)
			if c.err != nil {
				return string(out)
			}
			if len(out) > 0 {
				wait = idle
			}
		case <-time.After(wait):
			return string(out)
		}
	}
}
