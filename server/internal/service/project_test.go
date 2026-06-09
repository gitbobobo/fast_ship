package service

import (
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

func TestParseRepositoryURL(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantOwner   string
		wantRepo    string
		wantErr     bool
		errContains string
	}{
		{
			name:      "owner/repo format",
			input:     "godbobo/fast_ship",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "https full url",
			input:     "https://github.com/godbobo/fast_ship",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "https with .git suffix",
			input:     "https://github.com/godbobo/fast_ship.git",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "http url",
			input:     "http://github.com/godbobo/fast_ship",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "github.com prefix without protocol",
			input:     "github.com/godbobo/fast_ship",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "with trailing slash",
			input:     "https://github.com/godbobo/fast_ship/",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "with leading/trailing spaces",
			input:     "  godbobo/fast_ship  ",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "with extra path segments",
			input:     "https://github.com/godbobo/fast_ship/issues",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:        "empty string",
			input:       "",
			wantErr:     true,
			errContains: "不能为空",
		},
		{
			name:        "only owner",
			input:       "godbobo",
			wantErr:     true,
			errContains: "格式无效",
		},
		{
			name:        "missing owner",
			input:       "/fast_ship",
			wantErr:     true,
			errContains: "不能为空",
		},
		{
			name:        "missing repo",
			input:       "godbobo/",
			wantErr:     true,
			errContains: "格式无效",
		},
		{
			name:        "empty repo segment",
			input:       "godbobo//",
			wantErr:     true,
			errContains: "不能为空",
		},
		{
			name:        "invalid characters in owner",
			input:       "god bobo/fast_ship",
			wantErr:     true,
			errContains: "非法字符",
		},
		{
			name:        "invalid characters in repo",
			input:       "godbobo/fast ship",
			wantErr:     true,
			errContains: "非法字符",
		},
		{
			name:      "repo with dot and hyphen",
			input:     "godbobo/fast-ship.v2",
			wantOwner: "godbobo",
			wantRepo:  "fast-ship.v2",
			wantErr:   false,
		},
		{
			name:      "owner with underscore",
			input:     "god_bobo/fast-ship",
			wantOwner: "god_bobo",
			wantRepo:  "fast-ship",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOwner, gotRepo, err := parseRepositoryURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotOwner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", gotOwner, tt.wantOwner)
			}
			if gotRepo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", gotRepo, tt.wantRepo)
			}
		})
	}
}

func TestProjectServiceCreate_WithoutGitHub(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-no-github")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg)

	// 创建不带 GitHub 仓库的项目应该成功
	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:        "no-github-project",
		Description: "A project without GitHub",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if project.GithubOwner != "" {
		t.Errorf("expected empty GithubOwner, got %q", project.GithubOwner)
	}
	if project.GithubRepo != "" {
		t.Errorf("expected empty GithubRepo, got %q", project.GithubRepo)
	}
}

func TestProjectServiceCreate_WithGitHub(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-with-github")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg)

	// 创建带 GitHub 仓库的项目，提供 token 应该成功
	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "github-project",
		Description:   "A project with GitHub",
		RepositoryURL: "https://github.com/owner/repo",
		GithubToken:   "ghp_test123",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if project.GithubOwner != "owner" {
		t.Errorf("expected GithubOwner=owner, got %q", project.GithubOwner)
	}
	if project.GithubRepo != "repo" {
		t.Errorf("expected GithubRepo=repo, got %q", project.GithubRepo)
	}
}

func TestProjectServiceCreate_WithRepoURLButNoToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-no-token")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg)

	// 提供了仓库地址但没有 token 应该失败
	_, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "no-token-project",
		RepositoryURL: "https://github.com/owner/repo",
	})
	if err == nil {
		t.Fatal("expected error when repo URL provided without token, got nil")
	}
}

func TestProjectServiceGetBranches_NotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-branches")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = nil
	})

	_, _, err := projectSvc.GetBranches(t.Context(), project.ID, user.ID)
	if err == nil {
		t.Fatal("expected error for project without GitHub config, got nil")
	}
}
