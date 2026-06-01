package main

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestDefaultRestartPatternsIncludeErrorTrigger(t *testing.T) {
	patterns := defaultRestartPatterns()
	for _, pattern := range patterns {
		if pattern == `ERROR` {
			return
		}
	}
	t.Fatalf("default restart patterns = %q, want ERROR trigger", patterns)
}

func TestOpenPTYProvidesTerminalStdout(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("openPTY: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	cmd := exec.Command("sh", "-c", "test -t 1")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Run(); err != nil {
		t.Fatalf("child stdout was not a terminal: %v", err)
	}
}

func TestShouldUsePTY(t *testing.T) {
	if !shouldUsePTY([]string{"codex", "remote-control"}) {
		t.Fatal("codex remote-control should use a PTY")
	}
	if shouldUsePTY([]string{"codex", "remote-remote-control"}) {
		t.Fatal("unknown codex subcommands should keep pipe mode so quick failures remain visible")
	}
	if shouldUsePTY([]string{"sh", "-c", "echo hi"}) {
		t.Fatal("non-codex commands should keep pipe mode")
	}
}

func TestUpdateQuickExitState(t *testing.T) {
	tests := []struct {
		name      string
		triggered bool
		exitCode  int
		runtime   time.Duration
		current   int
		max       int
		wantNext  int
		wantStop  bool
	}{
		{name: "quick non zero increments", exitCode: 1, runtime: time.Second, current: 1, max: 5, wantNext: 2},
		{name: "stops at max", exitCode: 1, runtime: time.Second, current: 4, max: 5, wantNext: 5, wantStop: true},
		{name: "unlimited max", exitCode: 1, runtime: time.Second, current: 99, max: 0, wantNext: 100},
		{name: "triggered restart resets", triggered: true, exitCode: 1, runtime: time.Second, current: 2, max: 5, wantNext: 0},
		{name: "zero exit resets", exitCode: 0, runtime: time.Second, current: 2, max: 5, wantNext: 0},
		{name: "slow exit resets", exitCode: 1, runtime: 5 * time.Second, current: 2, max: 5, wantNext: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNext, gotStop := updateQuickExitState(tt.triggered, tt.exitCode, tt.runtime, 5*time.Second, tt.current, tt.max)
			if gotNext != tt.wantNext || gotStop != tt.wantStop {
				t.Fatalf("updateQuickExitState() = %d, %v; want %d, %v", gotNext, gotStop, tt.wantNext, tt.wantStop)
			}
		})
	}
}

func TestDefaultLockFileForCodexRemoteControl(t *testing.T) {
	got := defaultLockFile([]string{"codex", "remote-control"})
	want := "/tmp/respawn-codex-remote-control.lock"
	if got != want {
		t.Fatalf("defaultLockFile() = %q, want %q", got, want)
	}
}

func TestDefaultLockFileEmptyForOtherCommands(t *testing.T) {
	if got := defaultLockFile([]string{"echo", "hello"}); got != "" {
		t.Fatalf("defaultLockFile() = %q, want empty", got)
	}
}

func TestProcPPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, []byte("123 (cmd with spaces) S 45 123 123 0 -1 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := procPPID(path)
	if !ok || got != 45 {
		t.Fatalf("procPPID() = %d, %v; want 45, true", got, ok)
	}
}

func TestSignalProcessTreeSignalsDetachedDescendant(t *testing.T) {
	tmp := t.TempDir()
	pidFile := filepath.Join(tmp, "child.pid")
	markerFile := filepath.Join(tmp, "child.signaled")

	cmd := exec.Command(os.Args[0], "-test.run=TestSignalHelper")
	cmd.Env = append(os.Environ(),
		"RESPAWN_SIGNAL_HELPER=parent",
		"RESPAWN_SIGNAL_PIDFILE="+pidFile,
		"RESPAWN_SIGNAL_MARKER="+markerFile,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	waitForFile(t, pidFile)
	childPIDData, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(string(childPIDData))
	if err != nil {
		t.Fatal(err)
	}
	if !containsPID(descendantPIDs(cmd.Process.Pid), childPID) {
		t.Fatalf("detached child PID %d was not discovered as descendant of %d", childPID, cmd.Process.Pid)
	}
	signalProcessTree(cmd.Process.Pid, syscall.SIGTERM)
	waitForFile(t, markerFile)
}

func TestSignalHelper(t *testing.T) {
	switch os.Getenv("RESPAWN_SIGNAL_HELPER") {
	case "parent":
		cmd := exec.Command(os.Args[0], "-test.run=TestSignalHelper")
		cmd.Env = append(os.Environ(), "RESPAWN_SIGNAL_HELPER=child")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("RESPAWN_SIGNAL_PIDFILE"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		<-ch
		_ = os.WriteFile(os.Getenv("RESPAWN_SIGNAL_MARKER"), []byte("signaled\n"), 0o600)
		os.Exit(0)
	}
}

func containsPID(pids []int, want int) bool {
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestIsCodexRemoteControlProcess(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "direct", args: []string{"codex", "remote-control"}, want: true},
		{name: "node wrapper", args: []string{"node", "/path/to/codex", "remote-control"}, want: true},
		{name: "other codex command", args: []string{"codex", "exec"}, want: false},
		{name: "too short", args: []string{"codex"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCodexRemoteControlProcess(tt.args); got != tt.want {
				t.Fatalf("isCodexRemoteControlProcess(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
