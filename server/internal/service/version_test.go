package service

import (
	"context"
	"errors"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
)

func TestVersionServiceUpdate_PartialUpdatePreservesFields(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	version := createTestVersion(t, svc.db, project.ID)

	newNotes := "updated notes"
	updated, err := svc.versionService.Update(context.Background(), version.ID, project.UserID, true, &UpdateVersionRequest{
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
	_, err := svc.versionService.Update(context.Background(), version.ID, project.UserID, false, &UpdateVersionRequest{
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
	_, err := svc.versionService.Update(context.Background(), version.ID, project.UserID, true, &UpdateVersionRequest{
		VersionNumber: &newVersionNumber,
	})
	if !errors.Is(err, errs.ErrVersionNumberExists) {
		t.Fatalf("expected ErrVersionNumberExists, got %v", err)
	}
}

func TestVersionServiceCreate_ValidatesTargetBranch(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	svc.versionService.newBranchClient = func(token, owner, repo string) gitHubBranchClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return &fakeBranchClient{
			branches: []*ghclient.Branch{
				{Name: "main", SHA: "abc123", Default: true},
				{Name: "release/1.0", SHA: "def456"},
			},
			defaultBranch: "main",
		}
	}

	version, err := svc.versionService.Create(context.Background(), project.ID, project.UserID, &CreateVersionRequest{
		VersionNumber:   "v1.0.0",
		TargetCommitish: "release/1.0",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if version.TargetCommitish != "release/1.0" {
		t.Fatalf("expected target branch to be stored, got %q", version.TargetCommitish)
	}
}

func TestVersionServiceUpdate_RejectsUnknownTargetBranch(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})
	version := createTestVersion(t, svc.db, project.ID)
	svc.versionService.newBranchClient = func(token, owner, repo string) gitHubBranchClient {
		return &fakeBranchClient{
			branches:      []*ghclient.Branch{{Name: "main", SHA: "abc123", Default: true}},
			defaultBranch: "main",
		}
	}

	branch := "missing"
	_, err := svc.versionService.Update(context.Background(), version.ID, project.UserID, true, &UpdateVersionRequest{
		TargetCommitish: &branch,
	})
	if !errors.Is(err, errs.ErrTargetBranchNotFound) {
		t.Fatalf("expected ErrTargetBranchNotFound, got %v", err)
	}
}

type fakeBranchClient struct {
	branches      []*ghclient.Branch
	defaultBranch string
	err           error
}

func (f *fakeBranchClient) ListBranches(context.Context) ([]*ghclient.Branch, string, error) {
	return f.branches, f.defaultBranch, f.err
}
