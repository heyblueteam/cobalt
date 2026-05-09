package cobaltapi

// ServerUpgradeRequest is the body of POST /api/server/upgrade.
//
// Image (required) is the full image reference the daemon should swap
// to. The daemon doesn't try to validate the registry/tag — it spawns
// the helper which will fail-fast if the pull doesn't succeed.
//
// Pull defaults to true. Set to false when the image is already
// available locally (rarely useful outside dev loops).
type ServerUpgradeRequest struct {
	Image string `json:"image"`
	Pull  bool   `json:"pull,omitempty"`
}

// ServerUpgradeStatus is the JSON shape returned by the upgrade
// trigger and status endpoints. Match constants in the store package.
const (
	UpgradeStatusRunning    = "running"
	UpgradeStatusSucceeded  = "succeeded"
	UpgradeStatusFailed     = "failed"
	UpgradeStatusRolledBack = "rolled-back"
)

// ServerUpgrade is the public shape of one upgrade attempt — what the
// CLI sees from POST /api/server/upgrade and GET /api/server/upgrade/{id}.
type ServerUpgrade struct {
	ID            string `json:"id"`
	TargetImage   string `json:"targetImage"`
	TargetVersion string `json:"targetVersion,omitempty"`
	FromVersion   string `json:"fromVersion,omitempty"`
	Status        string `json:"status"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	StartedAt     int64  `json:"startedAt"`
	EndedAt       int64  `json:"endedAt,omitempty"`
}

// IsTerminal reports whether the upgrade is in a final state. CLI
// followers stop streaming the log once this is true.
func (u ServerUpgrade) IsTerminal() bool {
	switch u.Status {
	case UpgradeStatusSucceeded, UpgradeStatusFailed, UpgradeStatusRolledBack:
		return true
	}
	return false
}
