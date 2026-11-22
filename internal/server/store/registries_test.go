package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

func TestRegistryCredentials_EncryptedRoundTripAndReplace(t *testing.T) {
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))
	ctx := context.Background()

	if err := db.SetRegistryCredential(ctx, "ghcr.io", "bot", "token-1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRegistryCredential(ctx, "ghcr.io", "bot2", "token-2"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRegistryCredential(ctx, "ghcr.io")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "bot2" || got.Password != "token-2" {
		t.Errorf("got %+v", got)
	}

	resp, err := db.QuerySingle(ctx, `SELECT password FROM registry_credentials WHERE registry = ?`, "ghcr.io")
	if err != nil {
		t.Fatal(err)
	}
	stored := blobToString(resp.GetQueryResults()[0].Values[0][0])
	if !encryption.IsCiphertext(stored) || strings.Contains(stored, "token-2") {
		t.Errorf("password not encrypted at rest: %q", stored)
	}

	list, err := db.ListRegistryCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Registry != "ghcr.io" || list[0].Password != "" {
		t.Errorf("list must omit passwords: %+v", list)
	}

	if err := db.DeleteRegistryCredential(ctx, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetRegistryCredential(ctx, "ghcr.io"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if err := db.DeleteRegistryCredential(ctx, "ghcr.io"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := db.SetRegistryCredential(ctx, "https://ghcr.io", "u", "p"); err == nil {
		t.Error("expected invalid registry host to be refused")
	}
}

func TestDeployment_ImageRefsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	pid, err := db.CreateProject(ctx, Project{Name: "server", GithubRepo: "h/blue", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{"default": "ghcr.io/h/server@sha256:" + strings.Repeat("c", 64)}
	id, err := db.CreateDeployment(ctx, Deployment{ProjectID: pid, Number: 1, Status: cobaltapi.StateQueued, ImageRefs: refs})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetDeployment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageRefs["default"] != refs["default"] {
		t.Errorf("image refs: %+v", got.ImageRefs)
	}
	plain, err := db.CreateDeployment(ctx, Deployment{ProjectID: pid, Number: 2, Status: cobaltapi.StateQueued})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetDeployment(ctx, plain)
	if got.ImageRefs != nil {
		t.Errorf("build deployment has refs: %+v", got.ImageRefs)
	}
}

func TestProject_ImageSourceDefaultsToBuildAndUpdates(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id, err := db.CreateProject(ctx, Project{Name: "server", GithubRepo: "h/blue", Branch: "main", Path: "server"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := db.GetProjectByID(ctx, id)
	if p.ImageSource != validator.ImageSourceBuild {
		t.Fatalf("default image source: %q", p.ImageSource)
	}
	if err := db.UpdateProjectSource(ctx, id, "h/blue", "main", "server", "", validator.ImageSourceRegistry); err != nil {
		t.Fatal(err)
	}
	p, _ = db.GetProjectByName(ctx, "server")
	if p.ImageSource != validator.ImageSourceRegistry {
		t.Fatalf("after update: %q", p.ImageSource)
	}
	found, err := db.FindProjectsForRepoBranch(ctx, "h/blue", "main")
	if err != nil || len(found) != 1 || found[0].ImageSource != validator.ImageSourceRegistry {
		t.Fatalf("webhook lookup must carry image source: %+v %v", found, err)
	}
	if err := db.UpdateProjectSource(ctx, id, "h/blue", "main", "server", "", "ci"); err == nil {
		t.Error("expected invalid image source to be refused")
	}
}
