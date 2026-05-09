package store

import (
	"context"
	"errors"
	"time"
)

// Upgrade is one row in the upgrades table — a single attempt to swap
// the daemon to a new image. Status transitions are:
//
//	running → succeeded
//	running → failed       (helper exited with an error)
//	running → rolled-back  (probe timed out, helper restored prior image)
//
// The helper container writes structured progress to LogPath; the
// CLI streams that file via SSE.
type Upgrade struct {
	ID            string
	TargetImage   string
	TargetVersion string
	FromVersion   string
	Status        string
	ErrorMessage  string
	LogPath       string
	StartedAt     int64
	EndedAt       *int64
}

const (
	UpgradeStatusRunning    = "running"
	UpgradeStatusSucceeded  = "succeeded"
	UpgradeStatusFailed     = "failed"
	UpgradeStatusRolledBack = "rolled-back"
)

// IsTerminal reports whether the upgrade has reached a final state.
// CLI streamers stop following once this is true.
func (u Upgrade) IsTerminal() bool {
	switch u.Status {
	case UpgradeStatusSucceeded, UpgradeStatusFailed, UpgradeStatusRolledBack:
		return true
	}
	return false
}

// CreateUpgrade records the start of an upgrade attempt. The helper
// container is responsible for moving status off "running" via
// SetUpgradeStatus.
func (db *DB) CreateUpgrade(ctx context.Context, u Upgrade) error {
	if u.ID == "" || u.TargetImage == "" || u.LogPath == "" {
		return errors.New("store.CreateUpgrade: id, targetImage, and logPath are required")
	}
	if u.Status == "" {
		u.Status = UpgradeStatusRunning
	}
	if u.StartedAt == 0 {
		u.StartedAt = time.Now().Unix()
	}
	resp, err := db.ExecuteSingle(ctx,
		`INSERT INTO upgrades (id, target_image, target_version, from_version, status, log_path, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.TargetImage, u.TargetVersion, u.FromVersion, u.Status, u.LogPath, u.StartedAt,
	)
	if err != nil {
		return err
	}
	if len(resp.Results) > 0 && resp.Results[0].Error != "" {
		return errors.New(resp.Results[0].Error)
	}
	return nil
}

// GetUpgrade returns one row by id. Used by the status + output
// endpoints.
func (db *DB) GetUpgrade(ctx context.Context, id string) (*Upgrade, error) {
	resp, err := db.QuerySingle(ctx,
		`SELECT id, target_image, target_version, from_version, status,
		        error_message, log_path, started_at, ended_at
		 FROM upgrades WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	row := results[0].Values[0]
	u := &Upgrade{
		ID:            toString(row[0]),
		TargetImage:   toString(row[1]),
		TargetVersion: toString(row[2]),
		FromVersion:   toString(row[3]),
		Status:        toString(row[4]),
		ErrorMessage:  toString(row[5]),
		LogPath:       toString(row[6]),
		StartedAt:     toInt64(row[7]),
	}
	if row[8] != nil {
		ended := toInt64(row[8])
		u.EndedAt = &ended
	}
	return u, nil
}

// LatestRunningUpgrade returns the most recently started upgrade in
// status=running, if any. Used to short-circuit /api/server/upgrade
// when one is already in flight.
func (db *DB) LatestRunningUpgrade(ctx context.Context) (*Upgrade, error) {
	resp, err := db.QuerySingle(ctx,
		`SELECT id FROM upgrades WHERE status = ? ORDER BY started_at DESC LIMIT 1`,
		UpgradeStatusRunning)
	if err != nil {
		return nil, err
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	id := toString(results[0].Values[0][0])
	return db.GetUpgrade(ctx, id)
}

// SetUpgradeStatus moves an upgrade to a terminal state. errMsg is
// stored only when status indicates failure (failed / rolled-back).
func (db *DB) SetUpgradeStatus(ctx context.Context, id, status, errMsg string) error {
	now := time.Now().Unix()
	resp, err := db.ExecuteSingle(ctx,
		`UPDATE upgrades SET status = ?, error_message = ?, ended_at = ? WHERE id = ?`,
		status, errMsg, now, id)
	if err != nil {
		return err
	}
	if len(resp.Results) > 0 && resp.Results[0].Error != "" {
		return errors.New(resp.Results[0].Error)
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SweepStaleUpgrades marks any upgrade still in status=running for
// longer than maxAge as failed. Run on daemon boot to clean up after
// a crash that left an upgrade row dangling.
func (db *DB) SweepStaleUpgrades(ctx context.Context, maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	resp, err := db.ExecuteSingle(ctx,
		`UPDATE upgrades SET status = ?, error_message = ?, ended_at = ?
		 WHERE status = ? AND started_at < ?`,
		UpgradeStatusFailed,
		"upgrade row left in running state past max-age window — likely a daemon crash mid-upgrade",
		time.Now().Unix(),
		UpgradeStatusRunning, cutoff)
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return int(resp.Results[0].RowsAffected), nil
}
