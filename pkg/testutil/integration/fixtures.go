//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
)

func FixturePath(name string) string {
	return filepath.Join("testdata", "fixtures", name)
}

func CloneFixture(fixtureName string) (string, error) {
	src := FixturePath(fixtureName)
	cmd := exec.Command("cp", "-r", src, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func InitGitRepo(dir string) error {
	cmds := []*exec.Cmd{
		exec.Command("git", "init"),
		exec.Command("git", "add", "."),
		exec.Command("git", "commit", "-m", "initial"),
	}
	for _, cmd := range cmds {
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}
