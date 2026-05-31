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
