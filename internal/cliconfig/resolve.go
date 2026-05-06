package cliconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResolveProject determines the active project name for a CLI command.
//
// Resolution order (per design/issue cli#117 track 2):
//
//  1. explicit --project flag value
//  2. COBALT_PROJECT environment variable
//  3. cobalt.json in cwd or any ancestor (its "name" field)
//  4. CurrentProject for the active server in cliconfig
//
// startDir is where the cobalt.json walk begins (typically cwd). If empty,
// the walk is skipped.
func ResolveProject(flag, env, startDir string, server Server) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env != "" {
		return env, nil
	}
	if startDir != "" {
		if name, ok := readCobaltJSONName(startDir); ok {
			return name, nil
		}
	}
	if server.CurrentProject != "" {
		return server.CurrentProject, nil
	}
	return "", errors.New(
		"no project resolved: pass --project, set COBALT_PROJECT, " +
			"add a name to cobalt.json in this directory, or run 'cobalt use <project>'",
	)
}

// readCobaltJSONName walks up from dir looking for a cobalt.json file with a
// non-empty "name" field. Returns name and true on success; returns "" and
// false if no usable cobalt.json was found.
func readCobaltJSONName(dir string) (string, bool) {
	cur := dir
	for {
		path := filepath.Join(cur, "cobalt.json")
		if name, ok := tryReadName(path); ok {
			return name, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

func tryReadName(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", false
	}
	if doc.Name == "" {
		return "", false
	}
	return doc.Name, true
}

// ProjectError formats a project-resolution failure with the same wording as
// ResolveProject. Useful when callers want to surface the same hint without
// re-running the resolver.
func ProjectError() error {
	return fmt.Errorf(
		"no project resolved: pass --project, set COBALT_PROJECT, " +
			"add a name to cobalt.json in this directory, or run 'cobalt use <project>'",
	)
}
