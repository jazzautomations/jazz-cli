//go:build windows

package main

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process trees, Windows flavour: there are no process groups to signal, so
// every child we start goes into its own job object. Processes it spawns land
// in the same job automatically, and TerminateJobObject kills the whole tree.
// Trees that must die with jazz (shell, MCP servers, hooks, scripts) get
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so even a crash of jazz takes them down
// when the OS closes our handles. Background jobs (detached) do not, like the
// setsid'd jobs on Unix.
//
// Known gap: the child runs for a moment before AssignProcessToJobObject;
// anything it spawns in that window escapes the job. os/exec offers no
// CREATE_SUSPENDED + resume, and the window is microseconds in practice.

type winTree struct {
	proc windows.Handle // SYNCHRONIZE|QUERY|TERMINATE on the leader
	job  windows.Handle // 0 when the job could not be created/assigned
}

var winTrees = struct {
	sync.Mutex
	m map[int]*winTree
}{m: map[int]*winTree{}}

// winSandboxMaxProcs caps live processes per job when the sandbox is on
// (a parallel build or npm install stays far below it).
const winSandboxMaxProcs = 512

// winDetached remembers procPrepare's choice until procAttach.
var winDetached sync.Map // *exec.Cmd -> bool

// procPrepare starts cmd in a new console process group (a Ctrl+C typed at
// jazz's console is not delivered to it, and CTRL_BREAK can target it).
// detached also gives the child no console window.
func procPrepare(cmd *exec.Cmd, detached bool) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	if detached {
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
		cmd.SysProcAttr.HideWindow = true
	}
	winDetached.Store(cmd, detached)
}

// procAttach puts a started cmd into a fresh job object.
func procAttach(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	d, _ := winDetached.LoadAndDelete(cmd)
	detached, _ := d.(bool)
	pid := cmd.Process.Pid
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.PROCESS_SET_QUOTA, false, uint32(pid))
	if err != nil {
		return
	}
	t := &winTree{proc: h}
	if job, err := windows.CreateJobObject(nil, nil); err == nil {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		if !detached {
			info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		}
		sandboxed := sbCurrent() != nil && sbCurrent().Backend == sbJobObject
		if sandboxed {
			// sandbox.go, Windows flavour: a crash dies instead of popping a
			// WER dialog, and a fork bomb hits a ceiling
			info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
			info.BasicLimitInformation.ActiveProcessLimit = winSandboxMaxProcs
		}
		if info.BasicLimitInformation.LimitFlags != 0 {
			_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		}
		if sandboxed {
			// no clipboard, desktop switching, global atoms, system settings,
			// logoff/shutdown or USER handles of processes outside the job
			ui := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: windows.JOB_OBJECT_UILIMIT_DESKTOP | windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS |
				windows.JOB_OBJECT_UILIMIT_EXITWINDOWS | windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_HANDLES |
				windows.JOB_OBJECT_UILIMIT_READCLIPBOARD | windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD | windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS}
			_, _ = windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui)))
		}
		if windows.AssignProcessToJobObject(job, h) == nil {
			t.job = job
		} else {
			windows.CloseHandle(job)
		}
	}
	winTrees.Lock()
	if old := winTrees.m[pid]; old != nil {
		old.close()
	}
	winTrees.m[pid] = t
	winTrees.Unlock()
}

func (t *winTree) close() {
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
	if t.proc != 0 {
		windows.CloseHandle(t.proc)
		t.proc = 0
	}
}

// procForget closes the handles kept for pid. For a kill-on-close job this
// also kills whatever the tree left running (the Unix code kills the group
// after the leader exits for the same reason).
func procForget(pid int) {
	winTrees.Lock()
	t := winTrees.m[pid]
	delete(winTrees.m, pid)
	winTrees.Unlock()
	if t != nil {
		t.close()
	}
}

// procKillTree terminates the job of pid (or pid alone when it has none).
func procKillTree(pid int) {
	if pid <= 0 {
		return
	}
	winTrees.Lock()
	var job, proc windows.Handle
	if t := winTrees.m[pid]; t != nil {
		job, proc = t.job, t.proc
	}
	winTrees.Unlock()
	if job != 0 && windows.TerminateJobObject(job, 1) == nil {
		return
	}
	if proc != 0 {
		_ = windows.TerminateProcess(proc, 1)
		return
	}
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}

// procTermTree is the polite stop: CTRL_BREAK to the child's console process
// group (procPrepare made it a group leader). It only reaches processes that
// share our console; callers follow up with procKillTree after a grace period.
func procTermTree(pid int) {
	if pid <= 0 { // 0 would mean "every process on this console", jazz included
		return
	}
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}

// procKillPID stops a background job. Detached jobs have no console to send
// CTRL_BREAK to, so this always terminates the whole job object (which also
// reaches what a leader that already exited left running).
func procKillPID(pid int, force bool) { procKillTree(pid) }

// procAlive reports whether pid still runs, preferring the handle we kept
// (immune to PID reuse).
func procAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	winTrees.Lock()
	var h windows.Handle
	if t := winTrees.m[pid]; t != nil {
		h = t.proc
	}
	winTrees.Unlock()
	own := false
	if h == 0 {
		var err error
		h, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return false
		}
		own = true
	}
	ev, err := windows.WaitForSingleObject(h, 0)
	if own {
		windows.CloseHandle(h)
	}
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// memTotalMB reads the physical memory size (GlobalMemoryStatusEx).
func memTotalMB() int {
	type memoryStatusEx struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys            uint64
		AvailPhys            uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	p := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if p.Find() != nil {
		return 0
	}
	if r, _, _ := p.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0
	}
	return int(m.TotalPhys >> 20)
}

// cpuModel reads the processor name from the registry.
func cpuModel() string {
	var k windows.Handle
	path, _ := windows.UTF16PtrFromString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`)
	if windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, path, 0, windows.KEY_READ, &k) != nil {
		return ""
	}
	defer windows.RegCloseKey(k)
	name, _ := windows.UTF16PtrFromString("ProcessorNameString")
	buf := make([]uint16, 256)
	n := uint32(len(buf) * 2)
	var typ uint32
	if windows.RegQueryValueEx(k, name, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n/2])
}

// osName on Windows: the product name from the registry ("Windows 11 Pro").
func osName() string {
	var k windows.Handle
	path, _ := windows.UTF16PtrFromString(`SOFTWARE\Microsoft\Windows NT\CurrentVersion`)
	if windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, path, 0, windows.KEY_READ, &k) != nil {
		return "windows"
	}
	defer windows.RegCloseKey(k)
	name, _ := windows.UTF16PtrFromString("ProductName")
	buf := make([]uint16, 256)
	n := uint32(len(buf) * 2)
	var typ uint32
	if windows.RegQueryValueEx(k, name, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return "windows"
	}
	return windows.UTF16ToString(buf[:n/2])
}
