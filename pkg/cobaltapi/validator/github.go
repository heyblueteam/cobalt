package validator

import (
	"errors"
	"fmt"
)

var (
	ErrGitHubRepoRequired = errors.New("cobaltapi: GitHub repo is required")
	ErrGitHubRepoInvalid  = errors.New("cobaltapi: GitHub repo must be in owner/repo format")
	ErrCommitInvalid      = errors.New("cobaltapi: commit SHA must be 7-40 lowercase hex characters")
)

func ValidateGitHubRepo(repo string) error {
	if repo == "" {
		return ErrGitHubRepoRequired
	}
	if !GitHubRepoRX.MatchString(repo) {
		return fmt.Errorf("%w: expected owner/repo (e.g. heyblueteam/api)", ErrGitHubRepoInvalid)
	}
	return nil
}

func ValidateCommit(sha string) error {
	if sha == "" {
		return errors.New("cobaltapi: commit SHA is required")
	}
	if !CommitRX.MatchString(sha) {
		return fmt.Errorf("%w: must be 7-40 lowercase hex characters", ErrCommitInvalid)
	}
	return nil
}
