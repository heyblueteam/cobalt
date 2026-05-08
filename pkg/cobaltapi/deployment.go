package cobaltapi

// State is a deployment's lifecycle status. The set is closed; all values
// are listed below as constants.
type State string

// All deployment states.
const (
	// StateQueued — accepted by the API, not yet picked up by the dispatcher.
	StateQueued State = "queued"
	// StateFetching — repo clone / fetch in progress.
	StateFetching State = "fetching"
	// StateBuilding — image builds in progress.
	StateBuilding State = "building"
	// StateSwapping — services starting, healthcheck waiting, Caddy cutover.
	StateSwapping State = "swapping"

	// StateSuccess — terminal: live and serving.
	StateSuccess State = "success"
	// StateFailed — terminal: deploy errored at some stage. Rollback was
	// attempted; old deployment may still be live.
	StateFailed State = "failed"
	// StateCanceled — terminal: explicitly canceled mid-flight.
	StateCanceled State = "canceled"
	// StateSkipped — terminal: superseded by a newer queued deploy for the
	// same project before it ran.
	StateSkipped State = "skipped"
)

// activeStates are the non-terminal in-flight states. Used for "is a
// deploy currently happening?" checks.
var activeStates = map[State]struct{}{
	StateFetching: {},
	StateBuilding: {},
	StateSwapping: {},
}

// terminalStates are the four "done forever" states.
var terminalStates = map[State]struct{}{
	StateSuccess:  {},
	StateFailed:   {},
	StateCanceled: {},
	StateSkipped:  {},
}

// IsActive reports whether the deploy is currently in flight.
func (s State) IsActive() bool { _, ok := activeStates[s]; return ok }

// IsTerminal reports whether the deploy has reached a final state.
func (s State) IsTerminal() bool { _, ok := terminalStates[s]; return ok }

// IsValid reports whether s is one of the known states.
func (s State) IsValid() bool {
	if s == StateQueued {
		return true
	}
	return s.IsActive() || s.IsTerminal()
}

// AllStates lists every state, useful for tests and `IN (...)` SQL clauses.
func AllStates() []State {
	return []State{
		StateQueued, StateFetching, StateBuilding, StateSwapping,
		StateSuccess, StateFailed, StateCanceled, StateSkipped,
	}
}

// ActiveStatesList returns the active states as a slice — handy for SQL
// `IN (...)` arguments where map iteration order would be undesirable.
func ActiveStatesList() []State {
	return []State{StateFetching, StateBuilding, StateSwapping}
}

// Deployment is the public shape of a deployment row.
type Deployment struct {
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"projectId"`
	Number     int    `json:"number"`
	Status     State  `json:"status"`
	CommitSHA  string `json:"commitSha,omitempty"`
	NoCache    bool   `json:"noCache,omitempty"`
	RollbackOf int64  `json:"rollbackOf,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	StartedAt  int64  `json:"startedAt,omitempty"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

// DeploymentCreateRequest is the body of POST
// /api/projects/{name}/deployments.
type DeploymentCreateRequest struct {
	Commit             string `json:"commit,omitempty"`
	NoCache            bool   `json:"noCache,omitempty"`
	CobaltfileOverride string `json:"cobaltfileOverride,omitempty"`
}

// ProjectCron is the wire shape of one project-cron entry returned
// by GET /api/projects/{name}/crons.
type ProjectCron struct {
	Service          string `json:"service"`
	Schedule         string `json:"schedule"`
	Command          string `json:"command"`
	DeploymentNumber int    `json:"deploymentNumber"`
	// NextFireAt is the unix-second timestamp of the next scheduled
	// run, or zero if the scheduler hasn't yet computed one (e.g.
	// the daemon just restarted and reconcile hasn't completed).
	NextFireAt int64 `json:"nextFireAt,omitempty"`
}

// RollbackRequest is the body of POST /api/projects/{name}/rollback.
// To omitted/zero means "the most recent successful deployment that
// isn't the current live one."
type RollbackRequest struct {
	To int `json:"to,omitempty"`
}
