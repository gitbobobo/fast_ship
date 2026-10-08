package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
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
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())

	// 创建不带 GitHub 仓库的项目应该成功
	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:        "no-github-project",
		Description: api.Ptr("A project without GitHub"),
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
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())

	// 创建带 GitHub 仓库的项目，提供 token 应该成功
	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "github-project",
		Description:   api.Ptr("A project with GitHub"),
		RepositoryUrl: api.Ptr("https://github.com/owner/repo"),
		GithubToken:   api.Ptr("ghp_test123"),
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
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())

	// 提供了仓库地址但没有 token 应该失败
	_, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "no-token-project",
		RepositoryUrl: api.Ptr("https://github.com/owner/repo"),
	})
	if err == nil {
		t.Fatal("expected error when repo URL provided without token, got nil")
	}
}

func TestProjectServiceGetBranches_NotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-branches")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
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

func decryptProjectPRToken(t *testing.T, svc *testServices, projectID string) string {
	t.Helper()
	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", projectID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if len(stored.GithubPRTokenEncrypted) == 0 {
		return ""
	}
	plain, err := crypto.Decrypt(stored.GithubPRTokenEncrypted, []byte(svc.cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("decrypt stored pr token: %v", err)
	}
	return string(plain)
}

// 创建时可不带反馈仓库直接配 PR Token；响应只暴露 has_github_pr_token，
// 明文与密文都不出现在响应 JSON 里。
func TestProjectServiceCreate_WithPRToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-token")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())

	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "pr-token-project",
		GithubPrToken: api.Ptr("pr-token-v1"),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !project.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token=true")
	}
	if got := decryptProjectPRToken(t, svc, project.Id); got != "pr-token-v1" {
		t.Fatalf("expected stored pr-token-v1, got %q", got)
	}

	body, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(body), "pr-token-v1") || strings.Contains(string(body), `"github_pr_token"`) {
		t.Fatalf("response leaks pr token material: %s", body)
	}
}

// pr_token_source_project_id 复制源项目的 PR Token 密文，解密后与源 Token 相同；
// 与 github_pr_token 同传时 source 优先（与 source_project_id 语义一致）。
func TestProjectServiceCreate_PRTokenFromSource(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-source")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	source := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-shared")
	})

	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:                   "pr-source-project",
		PrTokenSourceProjectId: api.Ptr(source.ID),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !project.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token=true")
	}
	if got := decryptProjectPRToken(t, svc, project.Id); got != "pr-token-shared" {
		t.Fatalf("expected copied pr-token-shared, got %q", got)
	}

	project2, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:                   "pr-source-wins",
		GithubPrToken:          api.Ptr("pr-token-other"),
		PrTokenSourceProjectId: api.Ptr(source.ID),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got := decryptProjectPRToken(t, svc, project2.Id); got != "pr-token-shared" {
		t.Fatalf("expected source to win with pr-token-shared, got %q", got)
	}
}

// source 项目不存在返回 40401；存在但未配 PR Token 返回 40001。
func TestProjectServiceCreate_PRTokenSourceErrors(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-source-err")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	noPRToken := createTestProject(t, svc.db, user.ID)

	for _, tc := range []struct {
		name     string
		sourceID string
		wantCode int
	}{
		{"source missing", "nonexistent-project-id", errs.ErrProjectNotFound.Code},
		{"source has no pr token", noPRToken.ID, errs.ErrInvalidParams.Code},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectSvc.Create(user.ID, &CreateProjectRequest{
				Name:                   "pr-source-err-" + tc.name,
				PrTokenSourceProjectId: api.Ptr(tc.sourceID),
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || appErr.Code != tc.wantCode {
				t.Fatalf("expected code %d, got %v", tc.wantCode, err)
			}
		})
	}
}

// 未传任何 PR Token 字段时更新保留现值。
func TestProjectServiceUpdate_PreservesPRToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-preserve")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("renamed"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !resp.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token preserved")
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-v1" {
		t.Fatalf("expected pr-token-v1 preserved, got %q", got)
	}
}

// 传非空 github_pr_token 替换现值；显式空串视为不修改。
func TestProjectServiceUpdate_ReplacesPRToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-replace")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		GithubPrToken: api.Ptr("pr-token-v2"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !resp.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token after replace")
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-v2" {
		t.Fatalf("expected pr-token-v2, got %q", got)
	}

	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		GithubPrToken: api.Ptr(""),
	}); err != nil {
		t.Fatalf("update with empty token: %v", err)
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-v2" {
		t.Fatalf("expected empty string to keep pr-token-v2, got %q", got)
	}
}

// clear_github_pr_token=true 显式清除：字段置空、has_github_pr_token=false、
// 后续 PR 读取恢复沿用项目 Token；清除不删除既有的 PR 关联行。
func TestProjectServiceUpdate_ClearsPRToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-clear")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		ClearGithubPrToken: api.Ptr(true),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resp.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token=false after clear")
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "" {
		t.Fatalf("expected stored pr token cleared, got %q", got)
	}
}

// 更新时经 pr_token_source_project_id 复制源项目密文；与 github_pr_token
// 同传时 source 优先。
func TestProjectServiceUpdate_PRTokenFromSource(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-upd-source")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	source := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-shared")
	})
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		PrTokenSourceProjectId: api.Ptr(source.ID),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !resp.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token after copy")
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-shared" {
		t.Fatalf("expected copied pr-token-shared, got %q", got)
	}

	resp, err = projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		GithubPrToken:          api.Ptr("pr-token-v2"),
		PrTokenSourceProjectId: api.Ptr(source.ID),
	})
	if err != nil {
		t.Fatalf("update with token+source: %v", err)
	}
	if !resp.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token after token+source")
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-shared" {
		t.Fatalf("expected source to win with pr-token-shared, got %q", got)
	}
}

// 更新时 source 项目不存在返回 40401；未配 PR Token 返回 40001。
func TestProjectServiceUpdate_PRTokenSourceErrors(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-upd-source-err")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	noPRToken := createTestProject(t, svc.db, user.ID)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	for _, tc := range []struct {
		name     string
		sourceID string
		wantCode int
	}{
		{"source missing", "nonexistent-project-id", errs.ErrProjectNotFound.Code},
		{"source has no pr token", noPRToken.ID, errs.ErrInvalidParams.Code},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
				PrTokenSourceProjectId: api.Ptr(tc.sourceID),
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || appErr.Code != tc.wantCode {
				t.Fatalf("expected code %d, got %v", tc.wantCode, err)
			}
		})
	}
	// 失败请求不得改动现值
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-v1" {
		t.Fatalf("expected pr-token-v1 unchanged, got %q", got)
	}
}

// clear 标志与 github_pr_token 同时显式提供即冲突（不论取值——false/空串
// 也算提供），返回 40001；这是按指针非 nil 判定，不是按解引用后的值。
func TestProjectServiceUpdate_ClearAndReplaceConflict(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-conflict")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	for _, req := range []*UpdateProjectRequest{
		{GithubPrToken: api.Ptr("pr-token-v2"), ClearGithubPrToken: api.Ptr(true)},
		{GithubPrToken: api.Ptr("pr-token-v2"), ClearGithubPrToken: api.Ptr(false)},
		{GithubPrToken: api.Ptr(""), ClearGithubPrToken: api.Ptr(true)},
		{PrTokenSourceProjectId: api.Ptr("src"), ClearGithubPrToken: api.Ptr(true)},
		{PrTokenSourceProjectId: api.Ptr(""), ClearGithubPrToken: api.Ptr(true)},
	} {
		_, err := projectSvc.Update(project.ID, user.ID, req)
		appErr, ok := err.(*errs.AppError)
		if !ok || appErr.Code != errs.ErrInvalidParams.Code {
			t.Fatalf("expected 40001 for %+v, got %v", req, err)
		}
	}
	// 冲突请求不得改动现值
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-v1" {
		t.Fatalf("expected pr-token-v1 unchanged after conflict, got %q", got)
	}
}

// clear_github_pr_token=false 单独传按未提供处理：不报错也不改动现值。
func TestProjectServiceUpdate_ClearFalseIsNoop(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-pr-clear-false")
	projectSvc := NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.storage, svc.cfg, zap.NewNop())
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-v1")
	})

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		ClearGithubPrToken: api.Ptr(false),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !resp.HasGithubPrToken {
		t.Fatal("expected has_github_pr_token unchanged")
	}
	if got := decryptProjectPRToken(t, svc, project.ID); got != "pr-token-v1" {
		t.Fatalf("expected pr-token-v1 unchanged, got %q", got)
	}
}
