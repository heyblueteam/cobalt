package deploy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// DeployLogPath is the canonical on-disk location for a deployment's
// captured stdout/stderr. The path includes the project's mutable
// display name; renaming a project doesn't relocate old logs (acceptable
// — the log archive is for forensics on a specific deploy).
//
//	{dataDir}/logs/deployments/{projectName}/{deploymentNumber}.log
func DeployLogPath(dataDir, projectName string, deploymentNumber int) string {
	return filepath.Join(dataDir, "logs", "deployments", projectName,
		strconv.Itoa(deploymentNumber)+".log")
}

// OpenDeployLog opens (or creates) the deploy log file for append. The
// returned WriteCloser is what the orchestrator hands to build, hook, and
// generator subprocesses for their stdout/stderr.
//
// Parent directories are created if missing. Ownership of the file is
// the daemon's.
func OpenDeployLog(dataDir, projectName string, deploymentNumber int) (io.WriteCloser, error) {
	path := DeployLogPath(dataDir, projectName, deploymentNumber)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("deploy: mkdir log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("deploy: open log %s: %w", path, err)
	}
	return f, nil
}

// nopCloser wraps an io.Writer and provides a no-op Close. Used by the
// orchestrator when callers (tests) supply their own writer.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
