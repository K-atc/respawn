package main

import "testing"

func TestDefaultRestartPatternsIncludeErrorTrigger(t *testing.T) {
	patterns := defaultRestartPatterns()
	for _, pattern := range patterns {
		if pattern == `ERROR` {
			return
		}
	}
	t.Fatalf("default restart patterns = %q, want ERROR trigger", patterns)
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
