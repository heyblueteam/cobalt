package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/store"
)

func TestValidate_ProjectMustExist(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	err := Validate(context.Background(), db, EnqueueRequest{ProjectID: 99})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestValidate_ProjectIDRequired(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	err := Validate(context.Background(), db, EnqueueRequest{ProjectID: 0})
	if err == nil {
		t.Error("want error for missing project_id")
	}
}

func TestValidate_AcceptsValidCobaltfileOverride(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid := newProject(t, db, "api")

	err := Validate(context.Background(), db, EnqueueRequest{
		ProjectID:          pid,
		CobaltfileOverride: `{"version":"1.0","services":{"web":{}}}`,
	})
	if err != nil {
		t.Errorf("valid cobaltfile rejected: %v", err)
	}
}

func TestValidate_RejectsBrokenCobaltfileOverride(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid := newProject(t, db, "api")

	err := Validate(context.Background(), db, EnqueueRequest{
		ProjectID:          pid,
		CobaltfileOverride: `{"version":"2.0"}`, // unsupported version
	})
	if err == nil {
		t.Error("invalid cobaltfile accepted")
	}
}

func TestValidate_NoCobaltfileOverrideOK(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid := newProject(t, db, "api")

	err := Validate(context.Background(), db, EnqueueRequest{ProjectID: pid})
	if err != nil {
		t.Errorf("validate without override: %v", err)
	}
}
