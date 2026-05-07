package cobaltapi

import "errors"

var (
	ErrDeploymentNotFound     = errors.New("cobaltapi: deployment not found")
	ErrDeploymentNotCancelable = errors.New("cobaltapi: deployment cannot be canceled in current state")
	ErrProjectNotFound        = errors.New("cobaltapi: project not found")
	ErrProjectNameTaken       = errors.New("cobaltapi: project name already in use")
)