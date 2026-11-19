package service

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
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

// INT-241 回归：Artifact Upload/Delete 归属校验先于 pending 判断，非所有者
// 无论目标 pending 还是已发货都得到归属类错误（40401），不会先看到 40904。
func TestArtifactServiceUpload_NonOwnerGetsOwnershipErrorNotPending(t *testing.T) {
	for _, status := range []model.VersionStatus{model.VersionStatusPending, model.VersionStatusShipped} {
		svc := setupTestServices(t)
		createTestUser(t, svc.db, "user-1")
		createTestUser(t, svc.db, "user-2")
		project := createTestProject(t, svc.db, "user-1")
		version := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
			v.Status = status
		})

		_, err := svc.artifactService.Upload(version.ID, "user-2", "app.apk", 10, "android", "入侵者", bytes.NewBufferString("data"))
		if !errors.Is(err, errs.ErrProjectNotFound) {
			t.Fatalf("status %q: expected ErrProjectNotFound for non-owner, got %v", status, err)
		}
	}
}

func TestArtifactServiceDelete_NonOwnerGetsOwnershipErrorNotPending(t *testing.T) {
	for _, status := range []model.VersionStatus{model.VersionStatusPending, model.VersionStatusShipped} {
		svc := setupTestServices(t)
		createTestUser(t, svc.db, "user-1")
		createTestUser(t, svc.db, "user-2")
		project := createTestProject(t, svc.db, "user-1")
		version := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
			v.Status = status
		})
		artifact := &model.Artifact{
			ID:         "artifact-" + version.ID + "-" + string(status),
			VersionID:  version.ID,
			FileName:   "app.apk",
			FileSize:   10,
			FilePath:   project.ID + "/" + version.ID + "/app.apk",
			Platform:   "android",
			UploadedBy: "Web 用户",
			UploadedAt: time.Now(),
		}
		if err := svc.db.Create(artifact).Error; err != nil {
			t.Fatalf("create artifact: %v", err)
		}

		if err := svc.artifactService.Delete(artifact.ID, "user-2"); !errors.Is(err, errs.ErrProjectNotFound) {
			t.Fatalf("status %q: expected ErrProjectNotFound for non-owner, got %v", status, err)
		}
	}
}

func TestArtifactServiceDelete_OwnerNonPendingReturnsNotPending(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	version := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.Status = model.VersionStatusShipped
	})
	artifact := &model.Artifact{
		ID:         "artifact-owner-shipped",
		VersionID:  version.ID,
		FileName:   "app.apk",
		FileSize:   10,
		FilePath:   project.ID + "/" + version.ID + "/app.apk",
		Platform:   "android",
		UploadedBy: "Web 用户",
		UploadedAt: time.Now(),
	}
	if err := svc.db.Create(artifact).Error; err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	if err := svc.artifactService.Delete(artifact.ID, project.UserID); !errors.Is(err, errs.ErrVersionNotPending) {
		t.Fatalf("expected ErrVersionNotPending, got %v", err)
	}
}
