package store

import (
	"context"
	"errors"
)

// CommandRun is a row from the command_runs audit table — one row per
// `cobalt run` invocation. Lets operators answer "who ran what command,
// when, and how did it end" against any project.
type CommandRun struct {
	ID         int64
	ProjectID  int64
	APIKeyID   int64 // 0 when the row was inserted before bearer auth landed
	Service    string
	Command    string
	Status     string // "running" or "finished"
	ExitCode   int64  // valid only when Status == "finished"; -1 for non-ExitError failures
	TTY        bool
	CreatedAt  int64
	FinishedAt int64 // 0 while running
}

// Status values for command_runs.status.
const (
	CommandRunStatusRunning  = "running"
	CommandRunStatusFinished = "finished"
)

// CreateCommandRun inserts a new command_runs row in "running" state.
// Called at handler entry, before the docker container actually starts —
// so even sessions that fail before producing output leave a trace.
func (db *DB) CreateCommandRun(ctx context.Context, projectID, apikeyID int64, service, command string, tty bool) (int64, error) {
	tt := int64(0)
	if tty {
		tt = 1
	}
	resp, err := db.ExecuteSingle(
		ctx,
		`INSERT INTO command_runs (project_id, apikey_id, service, command, status, tty, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, strftime('%s', 'now'))`,
		projectID, apikeyID, service, command, CommandRunStatusRunning, tt,
	)
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].LastInsertID, nil
}

// FinishCommandRun records the run's exit code and flips status to
// "finished". Idempotent: a second call just overwrites the columns.
func (db *DB) FinishCommandRun(ctx context.Context, id int64, exitCode int) error {
	_, err := db.ExecuteSingle(
		ctx,
		`UPDATE command_runs SET status = ?, exit_code = ?, finished_at = strftime('%s', 'now') WHERE id = ?`,
		CommandRunStatusFinished, exitCode, id,
	)
	return err
}

// ListCommandRunsForProject returns the project's runs newest-first.
// limit caps the result count; pass 0 for an internal default of 50.
func (db *DB) ListCommandRunsForProject(ctx context.Context, projectID int64, limit int) ([]CommandRun, error) {
	if limit <= 0 {
		limit = 50
	}
	resp, err := db.QuerySingle(
		ctx,
		`SELECT id, project_id, COALESCE(apikey_id, 0), COALESCE(service, ''), command,
		        status, COALESCE(exit_code, 0), tty, created_at, COALESCE(finished_at, 0)
		 FROM command_runs WHERE project_id = ?
		 ORDER BY created_at DESC LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return nil, nil
	}
	out := make([]CommandRun, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		out = append(out, CommandRun{
			ID:         toInt64(row[0]),
			ProjectID:  toInt64(row[1]),
			APIKeyID:   toInt64(row[2]),
			Service:    toString(row[3]),
			Command:    toString(row[4]),
			Status:     toString(row[5]),
			ExitCode:   toInt64(row[6]),
			TTY:        toInt64(row[7]) != 0,
			CreatedAt:  toInt64(row[8]),
			FinishedAt: toInt64(row[9]),
		})
	}
	return out, nil
}
