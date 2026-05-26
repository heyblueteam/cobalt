//go:build integration

package integration

import (
	"os"
	"testing"
)

type Context struct {
	T             *testing.T
	DockerHost    string
	CaddySocket   string
	CaddyExternal string
	DaemonBaseURL string
	DataDir       string
	SwarmStop     func()
	CaddyStop     func()
	DaemonStop    func()
	BinaryPath    string
}

func New(t *testing.T) *Context {
	t.Helper()
	dataDir := t.TempDir()
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	return &Context{
		T:          t,
		DockerHost: dockerHost,
		DataDir:    dataDir,
	}
}

func (c *Context) Stop() {
	if c.DaemonStop != nil {
		c.DaemonStop()
	}
	if c.CaddyStop != nil {
		c.CaddyStop()
	}
	if c.SwarmStop != nil {
		c.SwarmStop()
	}
}
