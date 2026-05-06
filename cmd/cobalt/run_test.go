package main

import (
	"context"
	"testing"
)

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
