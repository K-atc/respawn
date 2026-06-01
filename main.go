package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const defaultMaxQuickExits = 1

type arrayFlags []string

func (a *arrayFlags) String() string { return strings.Join(*a, ",") }
func (a *arrayFlags) Set(v string) error {
	*a = append(*a, v)
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  respawn [options] [-- command args...]

Default command is: codex remote-control

Options:
  --restart-on REGEX   restart when stdout/stderr line matches REGEX
                       can be specified multiple times
  --delay DURATION     wait before restarting (default: 2s)
  --max-restarts N     stop after N restarts; 0 means unlimited (default: 0)
  --quick-exit-window DURATION
                       duration under which non-zero exit counts as quick failure (default: 5s)
  --max-quick-exits N  stop after N consecutive quick non-zero exits; 0 means unlimited (default: 1)
  --lock-file PATH     prevent concurrent respawn instances with this lock file
                       default for codex remote-control: /tmp/respawn-codex-remote-control.lock

Examples:
  respawn
  respawn --delay 5s -- codex remote-control
  respawn --restart-on 'stdin is closed' -- your-command
`)
}

func main() {
	var patterns arrayFlags
	var delay time.Duration
	var maxRestarts int
	var maxQuickExits int
	var quickExitWindow time.Duration
	var lockFile string

	flag.Var(&patterns, "restart-on", "regex that triggers restart when seen in output")
	flag.DurationVar(&delay, "delay", 2*time.Second, "delay before restart")
	flag.IntVar(&maxRestarts, "max-restarts", 0, "maximum restart count; 0 means unlimited")
	flag.DurationVar(&quickExitWindow, "quick-exit-window", 5*time.Second, "duration under which a non-zero child exit counts as a quick failure")
	flag.IntVar(&maxQuickExits, "max-quick-exits", defaultMaxQuickExits, "stop after this many consecutive quick non-zero exits; 0 means unlimited")
	flag.StringVar(&lockFile, "lock-file", "", "lock file that prevents concurrent respawn instances")
	flag.Usage = usage
	flag.Parse()

	cmdArgs := flag.Args()
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"codex", "remote-control"}
	}
	if len(patterns) == 0 {
		patterns = defaultRestartPatterns()
	}
	if isCodexRemoteControlCommand(cmdArgs) {
		running, err := codexRemoteControlAlreadyRunning(os.Getpid())
		if err == nil && running {
			fmt.Fprintln(os.Stderr, "respawn: codex remote-control already appears to be running; not starting another instance")
			os.Exit(1)
		}
	}
	if lockFile == "" {
		lockFile = defaultLockFile(cmdArgs)
	}
	if lockFile != "" {
		lock, err := acquireLock(lockFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "respawn: another instance appears to be running (lock %s): %v\n", lockFile, err)
			os.Exit(1)
		}
		defer lock.Close()
	}

	reList := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "respawn: invalid --restart-on regex %q: %v\n", p, err)
			os.Exit(2)
		}
		reList = append(reList, re)
	}

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	var stopping atomic.Bool

	restarts := 0
	quickExits := 0
	for {
		if stopping.Load() {
			return
		}
		if restarts > 0 {
			fmt.Fprintf(os.Stderr, "respawn: restarting in %s (restart #%d): %s\n", delay, restarts, shellJoin(cmdArgs))
			select {
			case <-time.After(delay):
			case sig := <-sigCh:
				fmt.Fprintf(os.Stderr, "respawn: received %s; exiting\n", sig)
				return
			}
		} else {
			fmt.Fprintf(os.Stderr, "respawn: starting: %s\n", shellJoin(cmdArgs))
		}

		startedAt := time.Now()
		restart, code, triggered := runOnce(cmdArgs, reList, sigCh, &stopping)
		if stopping.Load() {
			os.Exit(code)
		}
		if !restart {
			os.Exit(code)
		}
		var stopQuickExitLoop bool
		quickExits, stopQuickExitLoop = updateQuickExitState(triggered, code, time.Since(startedAt), quickExitWindow, quickExits, maxQuickExits)
		if stopQuickExitLoop {
			fmt.Fprintf(os.Stderr, "respawn: child exited too quickly %d times; not restarting (exit code %d)\n", quickExits, code)
			os.Exit(code)
		}
		restarts++
		if maxRestarts > 0 && restarts > maxRestarts {
			fmt.Fprintf(os.Stderr, "respawn: max restarts reached (%d); exiting\n", maxRestarts)
			os.Exit(1)
		}
	}
}

func updateQuickExitState(triggered bool, exitCode int, runtime, window time.Duration, current, max int) (next int, stop bool) {
	if triggered || exitCode == 0 || runtime >= window {
		return 0, false
	}
	next = current + 1
	return next, max > 0 && next >= max
}

func defaultRestartPatterns() []string {
	return []string{
		`write_stdin failed: stdin is closed for this session`,
		`ERROR`,
	}
}

func defaultLockFile(args []string) string {
	if isCodexRemoteControlCommand(args) {
		return "/tmp/respawn-codex-remote-control.lock"
	}
	return ""
}

func isCodexRemoteControlCommand(args []string) bool {
	return len(args) == 2 && filepath.Base(args[0]) == "codex" && args[1] == "remote-control"
}

func isCodexRemoteControlProcess(args []string) bool {
	return len(args) >= 2 && filepath.Base(args[len(args)-2]) == "codex" && args[len(args)-1] == "remote-control"
}

func codexRemoteControlAlreadyRunning(selfPID int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == selfPID {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
		if isCodexRemoteControlProcess(args) {
			return true, nil
		}
	}
	return false, nil
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func runOnce(args []string, patterns []*regexp.Regexp, sigCh <-chan os.Signal, stopping *atomic.Bool) (restart bool, exitCode int, triggered bool) {
	if shouldUsePTY(args) {
		return runOncePTY(args, patterns, sigCh, stopping)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn: stdout pipe: %v\n", err)
		return true, 1, false
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn: stderr pipe: %v\n", err)
		return true, 1, false
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "respawn: start failed: %v\n", err)
		return true, 127, false
	}

	restartCh := make(chan string, 1)
	doneCopy := make(chan struct{}, 2)
	go copyAndWatch(os.Stdout, stdout, patterns, restartCh, doneCopy)
	go copyAndWatch(os.Stderr, stderr, patterns, restartCh, doneCopy)

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	select {
	case reason := <-restartCh:
		triggered = true
		fmt.Fprintf(os.Stderr, "respawn: restart trigger matched: %s\n", strings.TrimSpace(reason))
		terminateProcessTree(cmd.Process.Pid)
		select {
		case waitErr = <-waitCh:
		case <-time.After(10 * time.Second):
			killProcessTree(cmd.Process.Pid)
			waitErr = <-waitCh
		}
		restart = true
	case sig := <-sigCh:
		stopping.Store(true)
		fmt.Fprintf(os.Stderr, "respawn: forwarding %s to child\n", sig)
		forwardSignal(cmd.Process.Pid, sig)
		select {
		case waitErr = <-waitCh:
		case <-time.After(10 * time.Second):
			killProcessTree(cmd.Process.Pid)
			waitErr = <-waitCh
		}
		restart = false
	case waitErr = <-waitCh:
		fmt.Fprintf(os.Stderr, "respawn: child exited; restarting\n")
		restart = true
	}

	<-doneCopy
	<-doneCopy
	return restart, exitCodeFromError(waitErr), triggered
}

func runOncePTY(args []string, patterns []*regexp.Regexp, sigCh <-chan os.Signal, stopping *atomic.Bool) (restart bool, exitCode int, triggered bool) {
	master, slave, err := openPTY()
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn: pty open failed: %v\n", err)
		return true, 1, false
	}
	defer master.Close()
	defer slave.Close()
	copyWindowSize(os.Stdout, slave)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "respawn: start failed: %v\n", err)
		return true, 127, false
	}
	_ = slave.Close()

	restartCh := make(chan string, 1)
	doneCopy := make(chan struct{}, 1)
	go copyAndWatch(os.Stdout, master, patterns, restartCh, doneCopy)
	go func() { _, _ = io.Copy(master, os.Stdin) }()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	select {
	case reason := <-restartCh:
		triggered = true
		fmt.Fprintf(os.Stderr, "respawn: restart trigger matched: %s\n", strings.TrimSpace(reason))
		terminateProcessTree(cmd.Process.Pid)
		select {
		case waitErr = <-waitCh:
		case <-time.After(10 * time.Second):
			killProcessTree(cmd.Process.Pid)
			waitErr = <-waitCh
		}
		restart = true
	case sig := <-sigCh:
		stopping.Store(true)
		fmt.Fprintf(os.Stderr, "respawn: forwarding %s to child\n", sig)
		forwardSignal(cmd.Process.Pid, sig)
		select {
		case waitErr = <-waitCh:
		case <-time.After(10 * time.Second):
			killProcessTree(cmd.Process.Pid)
			waitErr = <-waitCh
		}
		restart = false
	case waitErr = <-waitCh:
		fmt.Fprintf(os.Stderr, "respawn: child exited; restarting\n")
		restart = true
	}

	_ = master.Close()
	<-doneCopy
	return restart, exitCodeFromError(waitErr), triggered
}

func shouldUsePTY(args []string) bool {
	return isCodexRemoteControlCommand(args)
}

func copyAndWatch(dst *os.File, src io.Reader, patterns []*regexp.Regexp, restartCh chan<- string, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	r := bufio.NewReaderSize(src, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			_, _ = io.WriteString(dst, line)
			for _, re := range patterns {
				if re.MatchString(line) {
					select {
					case restartCh <- line:
					default:
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func openPTY() (master *os.File, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = master.Close()
		return nil, nil, errno
	}
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		_ = master.Close()
		return nil, nil, errno
	}
	slave, err = os.OpenFile(filepath.Join("/dev/pts", strconv.FormatUint(uint64(n), 10)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func copyWindowSize(from, to *os.File) {
	var ws struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, from.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return
	}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, to.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func terminateProcessTree(pid int) { signalProcessTree(pid, syscall.SIGTERM) }
func killProcessTree(pid int)      { signalProcessTree(pid, syscall.SIGKILL) }

func signalProcessTree(pid int, sig syscall.Signal) {
	// Snapshot descendants before signaling the root. Some commands spawn helper
	// processes that detach into another process group; if the root exits first,
	// those helpers may be reparented and become impossible to discover from the
	// original PID.
	descendants := descendantPIDs(pid)
	for _, childPID := range descendants {
		_ = syscall.Kill(childPID, sig)
	}
	_ = syscall.Kill(-pid, sig)
	_ = syscall.Kill(pid, sig)
}

func forwardSignal(pid int, sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		signalProcessTree(pid, s)
		return
	}
	terminateProcessTree(pid)
}

func descendantPIDs(rootPID int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	childrenByParent := make(map[int][]int)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == rootPID {
			continue
		}
		ppid, ok := procPPID(filepath.Join("/proc", entry.Name(), "stat"))
		if !ok {
			continue
		}
		childrenByParent[ppid] = append(childrenByParent[ppid], pid)
	}

	var descendants []int
	queue := append([]int(nil), childrenByParent[rootPID]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		descendants = append(descendants, pid)
		queue = append(queue, childrenByParent[pid]...)
	}
	return descendants
}

func procPPID(statPath string) (int, bool) {
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, false
	}
	text := string(data)
	endComm := strings.LastIndex(text, ") ")
	if endComm < 0 || endComm+2 >= len(text) {
		return 0, false
	}
	fields := strings.Fields(text[endComm+2:])
	// After the comm field, /proc/[pid]/stat starts with: state ppid pgrp ...
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`!*?[]{}();&|<>") {
			quoted[i] = strconv.Quote(a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
