package main

import (
	"context"
	"testing"
)

func TestJoinRunArgs(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"echo", "hello"}, "echo hello"},
		{[]string{"echo", "hello", "world"}, "echo hello world"},
		{[]string{"ls -la"}, "ls -la"}, // single quoted arg preserved
		{[]string{"sh", "-c", "while true; do sleep 1; done"}, "sh -c while true; do sleep 1; done"},
	}
	for _, tt := range tests {
		if got := joinRunArgs(tt.args); got != tt.want {
			t.Errorf("joinRunArgs(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestRunMissingCommand(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"run", "--project", "api"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !contains(err.Error(), "required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunParseServiceFlag(t *testing.T) {
	api := newMockAPI()
	defer api.close()

	if err := api.configPath(t); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	cmd, _, _ := root.Find([]string{"run"})
	if cmd == nil {
		t.Fatal("run command not found")
	}
	if f := cmd.Flags().Lookup("service"); f == nil {
		t.Error("run command missing --service flag")
	}
	if f := cmd.Flags().Lookup("project"); f == nil {
		t.Error("run command missing --project flag")
	}
}
