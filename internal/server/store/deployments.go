package store

import (
	"context"
	"errors"
	"strings"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

type Deployment struct {
	ID                 int64
	ProjectID          int64
	Number             int
	Status             cobaltapi.State
	CommitSHA          *string
	NoCache            bool
	CobaltfileOverride *string
	ResolvedCobaltfile *string
	CreatedAt          int64
	StartedAt          *int64
	FinishedAt         *int64
}

var activeKeepImageStatuses = []cobaltapi.State{
	cobaltapi.StateQueued,
	cobaltapi.StateFetching,
	cobaltapi.StateBuilding,
	cobaltapi.StateSwapping,
	cobaltapi.StateSuccess,
}

func (db *DB) ActiveDeploymentNumbers(ctx context.Context, projectID int64) ([]int, error) {
	statusStrings := make([]string, len(activeKeepImageStatuses))
	for i, s := range activeKeepImageStatuses {
		statusStrings[i] = string(s)
	}

	inClause := placeholders(len(activeKeepImageStatuses))
	q := `SELECT number FROM deployments WHERE project_id = ? AND status IN (` + inClause + `)`

	args := make([]any, 0, len(activeKeepImageStatuses)+1)
	args = append(args, projectID)
	for _, s := range statusStrings {
		args = append(args, s)
	}

	stmt, err := rqlitehttp.NewSQLStatement(q, args...)
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
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
	var out []int
	for _, row := range results[0].Values {
		out = append(out, int(toInt64(row[0])))
	}
	return out, nil
}

func (db *DB) CreateDeployment(ctx context.Context, d Deployment) (int64, error) {
	var commitSHA any = nil
	if d.CommitSHA != nil {
		commitSHA = *d.CommitSHA
	}
	resp, err := db.ExecuteSingle(ctx, `
        INSERT INTO deployments (project_id, number, status, commit_sha, no_cache, created_at)
        VALUES (?, ?, ?, ?, ?, strftime('%s', 'now'))
    `, d.ProjectID, d.Number, string(d.Status), commitSHA, boolToInt(d.NoCache))
	if err != nil {
		return 0, err
	}
	if len(resp.Results) == 0 {
		return 0, nil
	}
	return resp.Results[0].LastInsertID, nil
}

func (db *DB) NextDeploymentNumber(ctx context.Context, projectID int64) (int, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT MAX(number) FROM deployments WHERE project_id = ?
    `, projectID)
	if err != nil {
		return 0, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return 0, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return 1, nil
	}
	val := results[0].Values[0][0]
	if val == nil {
		return 1, nil
	}
	return int(toInt64(val)) + 1, nil
}

func (db *DB) SetDeploymentStatus(ctx context.Context, id int64, status cobaltapi.State) error {
	switch {
	case status == cobaltapi.StateFetching:
		_, err := db.ExecuteSingle(ctx,
			`UPDATE deployments SET status = ?, started_at = COALESCE(started_at, strftime('%s', 'now')) WHERE id = ?`,
			string(status), id,
		)
		return err
	case status.IsTerminal():
		_, err := db.ExecuteSingle(ctx,
			`UPDATE deployments SET status = ?, finished_at = strftime('%s', 'now') WHERE id = ?`,
			string(status), id,
		)
		return err
	default:
		_, err := db.ExecuteSingle(ctx,
			`UPDATE deployments SET status = ? WHERE id = ?`,
			string(status), id,
		)
		return err
	}
}

func (db *DB) GetDeployment(ctx context.Context, id int64) (*Deployment, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments WHERE id = ?
    `, id)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	return scanDeploymentRow(results[0].Values[0]), nil
}

func (db *DB) ListDeploymentsForProject(ctx context.Context, projectID int64, limit int) ([]Deployment, error) {
	if limit > 0 {
		stmt, err := rqlitehttp.NewSQLStatement(`
            SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
                   resolved_cobaltfile, created_at, started_at, finished_at
            FROM deployments
            WHERE project_id = ?
            ORDER BY number DESC
            LIMIT ?
        `, projectID, limit)
		if err != nil {
			return nil, err
		}
		resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
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
		deps, _ := scanDeployments(results[0].Values)
		return deps, nil
	}

	stmt, err := rqlitehttp.NewSQLStatement(`
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments
        WHERE project_id = ?
        ORDER BY number DESC
    `, projectID)
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
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
	deps, _ := scanDeployments(results[0].Values)
	return deps, nil
}

func (db *DB) QueuedDeployments(ctx context.Context) ([]Deployment, error) {
	stmt, err := rqlitehttp.NewSQLStatement(`
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments
        WHERE status = ?
        ORDER BY project_id, number
    `, string(cobaltapi.StateQueued))
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
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
	deps, _ := scanDeployments(results[0].Values)
	return deps, nil
}

func (db *DB) SetResolvedCobaltfile(ctx context.Context, deploymentID int64, raw string) error {
	_, err := db.ExecuteSingle(ctx,
		`UPDATE deployments SET resolved_cobaltfile = ? WHERE id = ?`,
		raw, deploymentID,
	)
	return err
}

func (db *DB) GetLastSuccessfulDeployment(ctx context.Context, projectID int64) (*Deployment, error) {
	resp, err := db.QuerySingle(ctx, `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments
        WHERE project_id = ? AND status = ?
        ORDER BY number DESC
        LIMIT 1
    `, projectID, string(cobaltapi.StateSuccess))
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	d := scanDeploymentRow(results[0].Values[0])
	return d, nil
}

func (db *DB) ActiveDeployments(ctx context.Context) ([]Deployment, error) {
	active := cobaltapi.ActiveStatesList()
	activeStrings := make([]string, len(active))
	for i, s := range active {
		activeStrings[i] = string(s)
	}

	inClause := placeholders(len(active))
	q := `
        SELECT id, project_id, number, status, commit_sha, no_cache, cobaltfile_override,
               resolved_cobaltfile, created_at, started_at, finished_at
        FROM deployments WHERE status IN (` + inClause + `)
        ORDER BY project_id, number
    `

	args := make([]any, 0, len(active))
	for _, s := range activeStrings {
		args = append(args, s)
	}

	stmt, err := rqlitehttp.NewSQLStatement(q, args...)
	if err != nil {
		return nil, err
	}
	resp, err := db.Query(ctx, rqlitehttp.SQLStatements{stmt}, nil)
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
	deps, _ := scanDeployments(results[0].Values)
	return deps, nil
}

func scanDeployments(rows [][]any) ([]Deployment, error) {
	var out []Deployment
	for _, row := range rows {
		out = append(out, *scanDeploymentRow(row))
	}
	return out, nil
}

func scanDeploymentRow(row []any) *Deployment {
	var d Deployment
	d.ID = toInt64(row[0])
	d.ProjectID = toInt64(row[1])
	d.Number = int(toInt64(row[2]))
	d.Status = cobaltapi.State(toString(row[3]))
	if row[4] != nil {
		s := toString(row[4])
		d.CommitSHA = &s
	}
	d.NoCache = toInt64(row[5]) != 0
	if row[6] != nil {
		s := toString(row[6])
		d.CobaltfileOverride = &s
	}
	if row[7] != nil {
		s := toString(row[7])
		d.ResolvedCobaltfile = &s
	}
	d.CreatedAt = toInt64(row[8])
	if row[9] != nil {
		v := toInt64(row[9])
		d.StartedAt = &v
	}
	if row[10] != nil {
		v := toInt64(row[10])
		d.FinishedAt = &v
	}
	return &d
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
