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
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

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
	var lockFile string

	flag.Var(&patterns, "restart-on", "regex that triggers restart when seen in output")
	flag.DurationVar(&delay, "delay", 2*time.Second, "delay before restart")
	flag.IntVar(&maxRestarts, "max-restarts", 0, "maximum restart count; 0 means unlimited")
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

		restart, code := runOnce(cmdArgs, reList, sigCh, &stopping)
		if stopping.Load() {
			os.Exit(code)
		}
		if !restart {
			os.Exit(code)
		}
		restarts++
		if maxRestarts > 0 && restarts > maxRestarts {
			fmt.Fprintf(os.Stderr, "respawn: max restarts reached (%d); exiting\n", maxRestarts)
			os.Exit(1)
		}
	}
}

func defaultRestartPatterns() []string {
	return []string{
		`write_stdin failed: stdin is closed for this session`,
		`ERROR`,
	}
}

func defaultLockFile(args []string) string {
	if len(args) == 2 && args[0] == "codex" && args[1] == "remote-control" {
		return "/tmp/respawn-codex-remote-control.lock"
	}
	return ""
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

func runOnce(args []string, patterns []*regexp.Regexp, sigCh <-chan os.Signal, stopping *atomic.Bool) (restart bool, exitCode int) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn: stdout pipe: %v\n", err)
		return true, 1
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn: stderr pipe: %v\n", err)
		return true, 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "respawn: start failed: %v\n", err)
		return true, 127
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
		fmt.Fprintf(os.Stderr, "respawn: restart trigger matched: %s\n", strings.TrimSpace(reason))
		terminateProcessGroup(cmd.Process.Pid)
		select {
		case waitErr = <-waitCh:
		case <-time.After(10 * time.Second):
			killProcessGroup(cmd.Process.Pid)
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
			killProcessGroup(cmd.Process.Pid)
			waitErr = <-waitCh
		}
		restart = false
	case waitErr = <-waitCh:
		fmt.Fprintf(os.Stderr, "respawn: child exited; restarting\n")
		restart = true
	}

	<-doneCopy
	<-doneCopy
	return restart, exitCodeFromError(waitErr)
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

func terminateProcessGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }
func killProcessGroup(pid int)      { _ = syscall.Kill(-pid, syscall.SIGKILL) }

func forwardSignal(pid int, sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(-pid, s)
		return
	}
	terminateProcessGroup(pid)
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
