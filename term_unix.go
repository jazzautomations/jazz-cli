//go:build !windows

package main

import (
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

// Terminal access, Unix flavour (termios ioctls + poll). term_windows.go has
// the console-API equivalent; the key parser in term.go is shared.

func isTTY(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

// termWidth is the terminal width in columns. It is cached per stdout fd and
// invalidated on SIGWINCH: the streaming path and every redraw ask for it, an
// ioctl each time is wasted work on a slow CPU.
func termWidth() int {
	if w := uiWidthOverride.Load(); w > 0 {
		return int(w)
	}
	fd := os.Stdout.Fd()
	if w := uiWCache.w.Load(); w > 0 && uiWCache.fd.Load() == uint64(fd) {
		return int(w)
	}
	ws, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80
	}
	uiWCache.fd.Store(uint64(fd))
	uiWCache.w.Store(int32(ws.Col))
	uiWCache.once.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGWINCH)
		safeGo(func() {
			for range c {
				uiWCache.w.Store(0)
				uiResizeNotify()
			}
		})
	})
	return int(ws.Col)
}

// termHeight is the terminal height in rows (24 when unknown); not cached.
func termHeight() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 {
		return 24
	}
	return int(ws.Row)
}

var uiWCache struct {
	w    atomic.Int32
	fd   atomic.Uint64
	once sync.Once
}

// uiWidthInvalidate drops the cached width (a resize handler that runs before
// the cache's own SIGWINCH handler must not read the old width).
func uiWidthInvalidate() { uiWCache.w.Store(0) }

// termVT reports whether the terminal understands ANSI/VT sequences. Every
// Unix terminal jazz supports does; the theme decides colors from TERM.
func termVT() bool { return true }

// uiTerm remembers the termios saved by enterCbreak so the editor can hand
// the terminal to a child process ($EDITOR) and take it back afterwards.
var uiTerm struct {
	mu       sync.Mutex
	fd       int
	old, raw *unix.Termios
}

// enterCbreak: no line buffering, no echo, no signals (we handle Ctrl+C ourselves),
// output processing stays on so "\n" still means CRLF.
func enterCbreak(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return func() {}, err
	}
	t := *old
	t.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	t.Iflag &^= unix.IXON
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return func() {}, err
	}
	uiTerm.mu.Lock()
	uiTerm.fd, uiTerm.old, uiTerm.raw = fd, old, &t
	uiTerm.mu.Unlock()
	os.Stdout.WriteString("\x1b[?2004h") // bracketed paste
	return func() {
		os.Stdout.WriteString("\x1b[?2004l\x1b[?25h") // bracketed paste off, cursor visible again
		_ = unix.IoctlSetTermios(fd, ioctlSetTermios, old)
	}, nil
}

// uiCooked runs f with the terminal restored to its original (cooked) mode and
// bracketed paste off, then puts cbreak mode back. Without a saved state it just runs f.
func uiCooked(f func()) {
	uiTerm.mu.Lock()
	fd, old, raw := uiTerm.fd, uiTerm.old, uiTerm.raw
	uiTerm.mu.Unlock()
	if old == nil {
		f()
		return
	}
	os.Stdout.WriteString("\x1b[?2004l")
	_ = unix.IoctlSetTermios(fd, ioctlSetTermios, old)
	defer func() {
		_ = unix.IoctlSetTermios(fd, ioctlSetTermios, raw)
		os.Stdout.WriteString("\x1b[?2004h")
	}()
	f()
}

// readFile reads a terminal with poll(2) so that Pause can take the terminal
// away between reads. Non-terminals are read plainly.
func (kr *KeyReader) readFile(f *os.File) {
	if !isTTY(f) {
		kr.readPlain(f)
		return
	}
	fd := int(f.Fd())
	buf := make([]byte, 4096)
	for {
		kr.gate.Lock()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 200)
		if err == unix.EINTR || err == nil && n == 0 {
			kr.gate.Unlock()
			continue
		}
		var rn int
		var rerr error
		if err != nil {
			rerr = err
		} else {
			rn, rerr = unix.Read(fd, buf)
			if rerr == unix.EINTR || rerr == unix.EAGAIN {
				rn, rerr = 0, nil
			} else if rerr == nil && rn == 0 {
				rerr = io.EOF
			}
		}
		kr.gate.Unlock()
		for i := 0; i < rn; i++ {
			kr.bytes <- buf[i]
		}
		if rerr != nil {
			close(kr.bytes)
			return
		}
	}
}

// uiWinchNotify subscribes c to terminal resizes (SIGWINCH).
func uiWinchNotify(c chan os.Signal) { signal.Notify(c, syscall.SIGWINCH) }
