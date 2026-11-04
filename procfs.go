package main

// In-process system sensors over /proc: "feel" the machine on a tiny offline
// box (1 CPU / 2 GB / linux/386) without forking ss, ps, free or df. Pure
// stdlib, read-only, and every scan tolerates entries that vanish mid-read.
// Nothing here is Linux-gated at compile time — on other systems the calls
// simply fail or find nothing.

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pfListeningPorts lists TCP sockets in LISTEN state (st == 0A) by reading
// /proc/net/tcp and /proc/net/tcp6 directly — what forking `ss -ltn` would
// cost a process for. Inode identifies the socket inside a process's fd
// table (see pfPortOwner). A missing tcp6 file just means IPv6 is off; a
// missing tcp file means no procfs and is the only error case.
func pfListeningPorts() ([]struct {
	Proto string
	Port  int
	Inode uint64
}, error) {
	out, err := pfParseListen("/proc/net/tcp", "tcp4")
	if err != nil {
		return nil, err
	}
	if v6, err6 := pfParseListen("/proc/net/tcp6", "tcp6"); err6 == nil {
		out = append(out, v6...)
	}
	return out, nil
}

// pfParseListen parses one /proc/net/tcp{,6} file. Columns, whitespace-split:
// "N:", local_address "HEXIP:HEXPORT", rem_address, st (hex state), …, inode
// at index 9. Ports and inodes go through strconv so they stay exact on
// 32-bit builds.
func pfParseListen(path, proto string) ([]struct {
	Proto string
	Port  int
	Inode uint64
}, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []struct {
		Proto string
		Port  int
		Inode uint64
	}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		cols := strings.Fields(sc.Text())
		if len(cols) < 10 || cols[0] == "sl" || cols[3] != "0A" {
			continue // header, short line, or not LISTEN
		}
		i := strings.LastIndexByte(cols[1], ':')
		if i < 0 {
			continue
		}
		port, err1 := strconv.ParseUint(cols[1][i+1:], 16, 32)
		inode, err2 := strconv.ParseUint(cols[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, struct {
			Proto string
			Port  int
			Inode uint64
		}{proto, int(port), inode})
	}
	return out, sc.Err()
}

// pfProcesses snapshots the process table: /proc/<pid>/stat gives pid, comm
// (inside parens — comm may itself contain spaces or parens, so split on the
// LAST ')') and ppid; /proc/<pid>/cmdline gives argv with NULs turned into
// spaces (empty for kernel threads). Pids that die mid-scan are skipped.
func pfProcesses() ([]struct {
	PID     int
	PPID    int
	Comm    string
	Cmdline string
}, error) {
	des, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []struct {
		PID     int
		PPID    int
		Comm    string
		Cmdline string
	}
	for _, de := range des {
		pid, err := strconv.Atoi(de.Name())
		if err != nil {
			continue // not a pid directory
		}
		if p, ok := pfReadProc(pid); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// pfReadProc reads one process's stat+cmdline; ok=false when it went away.
func pfReadProc(pid int) (p struct {
	PID     int
	PPID    int
	Comm    string
	Cmdline string
}, ok bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return p, false
	}
	s := strings.TrimSpace(string(b))
	l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if l < 0 || r <= l {
		return p, false
	}
	rest := strings.Fields(s[r+1:]) // rest[0] = state, rest[1] = ppid
	if len(rest) < 2 {
		return p, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return p, false
	}
	cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
	p.PID, p.PPID, p.Comm = pid, ppid, s[l+1:r]
	p.Cmdline = strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
	return p, true
}

// pfMemInfo reads /proc/meminfo into a name→kB map (MemTotal, MemAvailable,
// Buffers, Cached, …). Values are kept raw — a few entries like HugePages_Total
// are counts rather than kB, same caveat as reading the file by hand.
func pfMemInfo() (map[string]uint64, error) {
	fh, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	m := make(map[string]uint64, 64)
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		f := strings.Fields(line[i+1:])
		if len(f) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			m[line[:i]] = v // trailing "kB" unit, when present, is dropped
		}
	}
	return m, sc.Err()
}

// pfPIDAlive reports whether /proc/<pid> exists. Cheap liveness check that
// needs no signal permission (unlike kill(pid, 0) which can EPERM).
func pfPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

// pfPortOwner resolves a listening port to its owner pid: take the port's
// socket inode (pfListeningPorts) and find the /proc/<pid>/fd/* symlink
// "socket:[inode]". ok=false means either nothing listens on port, or the
// owner is a process whose fd table we may not read (another user's).
func pfPortOwner(port int) (pid int, ok bool) {
	ports, err := pfListeningPorts()
	if err != nil {
		return 0, false
	}
	var inode uint64
	for _, p := range ports {
		if p.Port == port {
			inode = p.Inode
			break
		}
	}
	if inode == 0 {
		return 0, false
	}
	want := "socket:[" + strconv.FormatUint(inode, 10) + "]"
	des, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, de := range des {
		pid, err := strconv.Atoi(de.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", de.Name(), "fd"))
		if err != nil {
			continue // died, or not ours to read
		}
		for _, fd := range fds {
			l, err := os.Readlink(filepath.Join("/proc", de.Name(), "fd", fd.Name()))
			if err == nil && l == want {
				return pid, true
			}
		}
	}
	return 0, false
}
