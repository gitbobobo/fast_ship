package service

import (
	"errors"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

func TestVersionServiceUpdate_PartialUpdatePreservesFields(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	version := createTestVersion(t, svc.db, project.ID)

	newNotes := "updated notes"
	updated, err := svc.versionService.Update(version.ID, project.UserID, true, &UpdateVersionRequest{
		ReleaseNotes: &newNotes,
	})
	if err != nil {
		t.Fatalf("update version: %v", err)
	}

	if updated.ReleaseNotes != newNotes {
		t.Fatalf("expected release notes %q, got %q", newNotes, updated.ReleaseNotes)
	}
	if updated.TargetCommitish != "main" {
		t.Fatalf("expected target commitish to be preserved, got %q", updated.TargetCommitish)
	}
	if updated.VersionNumber != "v1.0.0" {
		t.Fatalf("expected version number to be preserved, got %q", updated.VersionNumber)
	}
}

func TestVersionServiceUpdate_APIKeyCannotChangeVersionNumber(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	version := createTestVersion(t, svc.db, project.ID)

	newVersionNumber := "v1.0.1"
	_, err := svc.versionService.Update(version.ID, project.UserID, false, &UpdateVersionRequest{
		VersionNumber: &newVersionNumber,
	})
	if !errors.Is(err, errs.ErrApiKeyForbidden) {
		t.Fatalf("expected ErrApiKeyForbidden, got %v", err)
	}
}

func TestVersionServiceUpdate_RejectsDuplicateVersionNumber(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	version := createTestVersion(t, svc.db, project.ID)
	otherVersion := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.VersionNumber = "v1.0.1"
	})

	_, _ = version, otherVersion

	newVersionNumber := "v1.0.1"
	_, err := svc.versionService.Update(version.ID, project.UserID, true, &UpdateVersionRequest{
		VersionNumber: &newVersionNumber,
	})
	if !errors.Is(err, errs.ErrVersionNumberExists) {
		t.Fatalf("expected ErrVersionNumberExists, got %v", err)
	}
}
