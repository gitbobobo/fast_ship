package service

import (
	"bytes"
	"testing"
)

func TestArtifactServiceUpload_ReplacesExistingArtifact(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	version := createTestVersion(t, svc.db, project.ID)

	first, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("first"),
	)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}

	second, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		20,
		"android-prod",
		"API Key: CI-Android",
		bytes.NewBufferString("second"),
	)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}

	artifacts, err := svc.artifactRepo.ListByVersionID(version.ID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}

	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact after replacement, got %d", len(artifacts))
	}
	if first.ID != second.ID {
		t.Fatalf("expected artifact replacement to reuse same record, got %q and %q", first.ID, second.ID)
	}
	if second.FileSize != 20 {
		t.Fatalf("expected updated file size 20, got %d", second.FileSize)
	}
	if second.Platform != "android-prod" {
		t.Fatalf("expected updated platform, got %q", second.Platform)
	}
	if second.UploadedBy != "API Key: CI-Android" {
		t.Fatalf("expected updated uploader, got %q", second.UploadedBy)
	}
}
