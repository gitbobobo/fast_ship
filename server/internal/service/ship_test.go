package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	gh "github.com/google/go-github/v62/github"
)

func TestShipServiceCheck_ReturnsDetailedMissingItems(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = []byte{}
	})
	version := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.ReleaseNotes = ""
		v.TargetCommitish = ""
	})

	check, err := svc.shipService.Check(version.ID, project.UserID)
	if err != nil {
		t.Fatalf("ship check: %v", err)
	}

	if check.CanShip {
		t.Fatalf("expected ship check to fail")
	}
	if len(check.Items) != 4 {
		t.Fatalf("expected 4 check items, got %d", len(check.Items))
	}

	assertCheckItem := func(key string, ok bool) {
		t.Helper()
		for _, item := range check.Items {
			if item.Key == key {
				if item.OK != ok {
					t.Fatalf("expected %s ok=%v, got %v", key, ok, item.OK)
				}
				return
			}
		}
		t.Fatalf("missing check item %s", key)
	}

	assertCheckItem("release_notes", false)
	assertCheckItem("artifacts", false)
	assertCheckItem("target_commitish", false)
	assertCheckItem("github_config", false)
}

func TestShipServiceShip_SuccessUpdatesVersionAndUploadsAssets(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		release: &gh.RepositoryRelease{
			ID:      int64Ptr(42),
			HTMLURL: stringPtr("https://github.com/owner/repo/releases/tag/v1.0.0"),
		},
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		if owner != project.GithubOwner || repo != project.GithubRepo {
			t.Fatalf("unexpected repo: %s/%s", owner, repo)
		}
		return fake
	}

	if err := svc.shipService.Ship(version.ID, project.UserID); err != nil {
		t.Fatalf("ship version: %v", err)
	}

	updated, err := svc.versionRepo.FindByID(version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}

	if updated.Status != model.VersionStatusShipped {
		t.Fatalf("expected shipped status, got %q", updated.Status)
	}
	if updated.ShipStatus != model.ShipStatusCompleted {
		t.Fatalf("expected completed ship status, got %q", updated.ShipStatus)
	}
	if updated.ShipStage != model.ShipStageFinalize {
		t.Fatalf("expected finalize ship stage, got %q", updated.ShipStage)
	}
	if updated.ShippedAt == nil {
		t.Fatalf("expected shipped_at to be set")
	}
	if updated.GithubReleaseURL != "https://github.com/owner/repo/releases/tag/v1.0.0" {
		t.Fatalf("unexpected release url: %q", updated.GithubReleaseURL)
	}
	if updated.ErrorLog != "" {
		t.Fatalf("expected empty error log, got %q", updated.ErrorLog)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createTagCalls != 1 {
		t.Fatalf("expected create tag once, got %d", fake.createTagCalls)
	}
	if fake.createReleaseCalls != 1 {
		t.Fatalf("expected create release once, got %d", fake.createReleaseCalls)
	}
	if len(fake.uploadedAssets) != 1 || fake.uploadedAssets[0] != "app.apk" {
		t.Fatalf("unexpected uploaded assets: %#v", fake.uploadedAssets)
	}
}

func TestShipServiceShip_CreateReleaseFailureRecordsFailureState(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		createReleaseErr: errors.New("release api down"),
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return fake
	}

	err := svc.shipService.Ship(version.ID, project.UserID)
	if err == nil {
		t.Fatalf("expected ship to fail")
	}
	if !strings.Contains(err.Error(), "创建 Release 失败") {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, reloadErr := svc.versionRepo.FindByID(version.ID)
	if reloadErr != nil {
		t.Fatalf("reload version: %v", reloadErr)
	}

	if updated.Status != model.VersionStatusPending {
		t.Fatalf("expected pending status after failure, got %q", updated.Status)
	}
	if updated.ShipStatus != model.ShipStatusFailed {
		t.Fatalf("expected failed ship status, got %q", updated.ShipStatus)
	}
	if updated.ShipStage != model.ShipStageCreateRelease {
		t.Fatalf("expected create release stage, got %q", updated.ShipStage)
	}
	if !strings.Contains(updated.ErrorLog, "release api down") {
		t.Fatalf("expected error log to contain release failure, got %q", updated.ErrorLog)
	}
	if updated.GithubReleaseURL != "" {
		t.Fatalf("expected empty release url after failure, got %q", updated.GithubReleaseURL)
	}
}

func TestShipServiceShip_UploadFailureRecordsFailureState(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)

	if _, err := svc.artifactService.Upload(
		version.ID,
		project.UserID,
		"app.apk",
		10,
		"android",
		"Web 用户",
		bytes.NewBufferString("apk-data"),
	); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}

	fake := &fakeGitHubClient{
		release:        &gh.RepositoryRelease{ID: int64Ptr(42)},
		uploadAssetErr: errors.New("asset upload failed"),
	}
	svc.shipService.newClient = func(token, owner, repo string) gitHubClient {
		return fake
	}

	err := svc.shipService.Ship(version.ID, project.UserID)
	if err == nil {
		t.Fatalf("expected ship to fail")
	}
	if !strings.Contains(err.Error(), "上传安装包失败") {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, reloadErr := svc.versionRepo.FindByID(version.ID)
	if reloadErr != nil {
		t.Fatalf("reload version: %v", reloadErr)
	}

	if updated.Status != model.VersionStatusPending {
		t.Fatalf("expected pending status after failure, got %q", updated.Status)
	}
	if updated.ShipStatus != model.ShipStatusFailed {
		t.Fatalf("expected failed ship status, got %q", updated.ShipStatus)
	}
	if updated.ShipStage != model.ShipStageUploadAssets {
		t.Fatalf("expected upload assets stage, got %q", updated.ShipStage)
	}
	if !strings.Contains(updated.ErrorLog, "asset upload failed") {
		t.Fatalf("expected error log to contain upload failure, got %q", updated.ErrorLog)
	}
}

type fakeGitHubClient struct {
	mu sync.Mutex

	validateErr      error
	createTagErr     error
	createReleaseErr error
	uploadAssetErr   error
	release          *gh.RepositoryRelease

	createTagCalls     int
	createReleaseCalls int
	uploadedAssets     []string
}

func (f *fakeGitHubClient) ValidateRepository(context.Context) error {
	return f.validateErr
}

func (f *fakeGitHubClient) CreateTag(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createTagCalls++
	return f.createTagErr
}

func (f *fakeGitHubClient) CreateRelease(context.Context, string, string, string) (*gh.RepositoryRelease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createReleaseCalls++
	if f.createReleaseErr != nil {
		return nil, f.createReleaseErr
	}
	return f.release, nil
}

func (f *fakeGitHubClient) UploadAsset(_ context.Context, _ int64, filename string, _ *os.File) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadedAssets = append(f.uploadedAssets, filename)
	return f.uploadAssetErr
}

func int64Ptr(v int64) *int64 {
	return &v
}

func stringPtr(v string) *string {
	return &v
}
