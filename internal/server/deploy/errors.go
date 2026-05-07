package deploy

import "errors"

var (
	ErrProjectIDRequired     = errors.New("deploy: project_id required")
	ErrCobaltfileInvalid     = errors.New("deploy: invalid cobaltfile override")
	ErrNotInFlight           = errors.New("deploy: not in-flight")
	ErrNotTracked            = errors.New("deploy: not tracked in dispatcher")
	ErrDeploymentNotCancelable = errors.New("deploy: deployment cannot be canceled in current state")
)