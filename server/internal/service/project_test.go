package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
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

func newTestProjectService(svc *testServices) *ProjectService {
	return NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.tokenRepo, svc.storage, svc.cfg, zap.NewNop())
}

func TestProjectServiceCreate_WithoutGitHub(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-no-github")
	projectSvc := newTestProjectService(svc)

	// 创建不带 GitHub 仓库的项目应该成功
	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:        "no-github-project",
		Description: api.Ptr("A project without GitHub"),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if project.GithubOwner != "" || project.GithubRepo != "" {
		t.Fatalf("expected empty github owner/repo, got %q/%q", project.GithubOwner, project.GithubRepo)
	}
	if project.PublicGithubTokenId != "" || project.CodeGithubTokenId != "" {
		t.Fatalf("expected empty token references, got %q/%q", project.PublicGithubTokenId, project.CodeGithubTokenId)
	}
}

// 创建时可以只填仓库不选 Token：仓库与 Token 相互独立，公开仓库 Token
// 之后在设置页或项目表单里补配。
func TestProjectServiceCreate_WithRepoURLButNoToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-no-token")
	projectSvc := newTestProjectService(svc)

	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "no-token-project",
		RepositoryUrl: api.Ptr("https://github.com/owner/repo"),
	})
	if err != nil {
		t.Fatalf("expected repo URL without token to succeed, got %v", err)
	}
	if project.GithubOwner != "owner" || project.GithubRepo != "repo" {
		t.Fatalf("expected owner/repo, got %q/%q", project.GithubOwner, project.GithubRepo)
	}
	if project.PublicGithubTokenId != "" || project.CodeGithubTokenId != "" {
		t.Fatalf("expected empty token references, got %q/%q", project.PublicGithubTokenId, project.CodeGithubTokenId)
	}
}

// 创建时引用本人 Token 库记录：两个用途可同时引用同一条记录。
func TestProjectServiceCreate_WithTokenReferences(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-create-refs")
	projectSvc := newTestProjectService(svc)
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_create")

	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:                "refs-project",
		RepositoryUrl:       api.Ptr("https://github.com/owner/repo"),
		PublicGithubTokenId: api.Ptr(token.ID),
		CodeGithubTokenId:   api.Ptr(token.ID),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if project.PublicGithubTokenId != token.ID || project.CodeGithubTokenId != token.ID {
		t.Fatalf("expected both references = %s, got %q/%q", token.ID, project.PublicGithubTokenId, project.CodeGithubTokenId)
	}
}

// 引用必须属于当前用户：不存在或他人 Token 统一 40420（跨用户不可见）。
func TestProjectServiceCreate_TokenReferenceValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-create-ref-err")
	other := createTestUser(t, svc.db, "user-create-ref-other")
	foreign := createTestToken(t, svc.db, svc.cfg.Encryption.Key, other.ID, "ghp_foreign")
	projectSvc := newTestProjectService(svc)

	for _, tc := range []struct {
		name     string
		publicID string
		codeID   string
	}{
		{"missing public token", "nonexistent-token-id", ""},
		{"foreign public token", foreign.ID, ""},
		{"missing code token", "", "nonexistent-token-id"},
		{"foreign code token", "", foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectSvc.Create(user.ID, &CreateProjectRequest{
				Name:                "ref-err-project",
				PublicGithubTokenId: api.NonEmpty(tc.publicID),
				CodeGithubTokenId:   api.NonEmpty(tc.codeID),
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || !errors.Is(appErr, errs.ErrGitHubTokenNotFound) {
				t.Fatalf("expected ErrGitHubTokenNotFound, got %v", err)
			}
		})
	}
}

func TestProjectRepoCreate_RejectsMissingToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-repo-missing-token")
	err := svc.projectRepo.Create(&model.Project{
		ID:                  uuid.NewString(),
		UserID:              user.ID,
		Name:                "dangling-ref",
		PublicGitHubTokenID: "gone-token-id",
	})
	if !errors.Is(err, repository.ErrGitHubTokenMissing) {
		t.Fatalf("expected ErrGitHubTokenMissing, got %v", err)
	}
	err = svc.projectRepo.Create(&model.Project{
		ID:     uuid.NewString(),
		UserID: user.ID,
		Name:   "no-tokens",
	})
	if err != nil {
		t.Fatalf("empty token refs must be allowed, got %v", err)
	}
}

func TestProjectRepoUpdate_RejectsDeletedToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-repo-update-gone")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_live")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = ""
	})
	if err := svc.db.Where("id = ?", token.ID).Delete(&model.UserGitHubToken{}).Error; err != nil {
		t.Fatalf("delete token: %v", err)
	}
	project.Name = "renamed"
	if err := svc.projectRepo.Update(project); !errors.Is(err, repository.ErrGitHubTokenMissing) {
		t.Fatalf("expected ErrGitHubTokenMissing, got %v", err)
	}
}

// 项目仓库地址变更时，审查设置必须停用并刷新缓存的 repo_full_name：
// 否则旧仓库占着唯一索引，而新仓库可能与另一个已启用项目重复。
func TestProjectRepoUpdate_RepoSyncsReviewSettings(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-repo-review-sync")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "oldowner"
		p.GithubRepo = "oldrepo"
	})
	now := time.Now().UTC()
	row := &model.ProjectReviewSettings{
		ProjectID:              project.ID,
		UserID:                 user.ID,
		RepoFullName:           "oldowner/oldrepo",
		Enabled:                true,
		CodeCombinationsJSON:   "[]",
		VisualCombinationsJSON: "[]",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := svc.db.Create(row).Error; err != nil {
		t.Fatalf("create review settings: %v", err)
	}
	project.GithubOwner = "NewOwner"
	project.GithubRepo = "NewRepo"
	if err := svc.projectRepo.Update(project); err != nil {
		t.Fatalf("update project: %v", err)
	}
	var got model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", project.ID).First(&got).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if got.Enabled {
		t.Fatal("repo 变更后审查设置应停用")
	}
	if got.RepoFullName != "newowner/newrepo" {
		t.Fatalf("repo_full_name = %q, want newowner/newrepo", got.RepoFullName)
	}
	// 换仓库要推进 epoch 并清 pending：在途同步不能再向旧仓库发布。
	var ledger model.ReviewPullRequest
	if err := svc.db.Create(&model.ReviewPullRequest{
		ProjectID: project.ID, RepoFullName: "oldowner/oldrepo", PRNumber: 1,
		UserID: user.ID, HeadSHA: "sha", SyncPending: true, SyncEpoch: 4, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	project.GithubOwner, project.GithubRepo = "Another", "Repo"
	if err := svc.projectRepo.Update(project); err != nil {
		t.Fatalf("update project 2: %v", err)
	}
	svc.db.First(&ledger, "project_id = ? AND pr_number = 1", project.ID)
	if ledger.SyncPending || ledger.SyncEpoch != 5 {
		t.Fatalf("ledger pending/epoch = %v/%d, want false/5", ledger.SyncPending, ledger.SyncEpoch)
	}
}

func TestProjectRepo_NullTokenRefsScanAsEmpty(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-null-token-scan")
	project := createTestProject(t, svc.db, user.ID)
	if err := svc.db.Exec("UPDATE projects SET public_github_token_id = NULL, code_github_token_id = NULL WHERE id = ?", project.ID).Error; err != nil {
		t.Fatalf("null refs: %v", err)
	}
	var got model.Project
	if err := svc.db.First(&got, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got.PublicGitHubTokenID != "" || got.CodeGitHubTokenID != "" {
		t.Fatalf("NULL refs must scan as empty strings, got public=%q code=%q", got.PublicGitHubTokenID, got.CodeGitHubTokenID)
	}
}

func TestProjectServiceGetBranches_NotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-branches")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
	})

	_, _, err := projectSvc.GetBranches(t.Context(), project.ID, user.ID)
	if err == nil {
		t.Fatal("expected error for project without GitHub config, got nil")
	}
}

// 更新三态：key 缺省保留现值、Set+空值清空、Set+非空替换（须属于当前用户）。
func TestProjectServiceUpdate_TokenReferenceThreeStates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-ref-three")
	projectSvc := newTestProjectService(svc)
	tokenA := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_token_a")
	tokenB := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_token_b")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = tokenA.ID
		p.CodeGitHubTokenID = tokenA.ID
	})

	storedRefs := func() (string, string) {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.PublicGitHubTokenID, stored.CodeGitHubTokenID
	}

	// key 缺省：保留现值
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("untouched refs"),
	}); err != nil {
		t.Fatalf("update without refs: %v", err)
	}
	publicRef, codeRef := storedRefs()
	if publicRef != tokenA.ID || codeRef != tokenA.ID {
		t.Fatalf("expected refs preserved, got %q/%q", publicRef, codeRef)
	}

	// 非空替换：公开仓库换到 tokenB，代码仓库保持
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		PublicGithubTokenId: TokenRefUpdate{Set: true, Value: tokenB.ID},
	}); err != nil {
		t.Fatalf("update replace public ref: %v", err)
	}
	publicRef, codeRef = storedRefs()
	if publicRef != tokenB.ID || codeRef != tokenA.ID {
		t.Fatalf("expected public=%s code=%s, got %q/%q", tokenB.ID, tokenA.ID, publicRef, codeRef)
	}

	// 显式清空：Set+空值清空公开仓库引用
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		PublicGithubTokenId: TokenRefUpdate{Set: true},
	}); err != nil {
		t.Fatalf("update clear public ref: %v", err)
	}
	publicRef, codeRef = storedRefs()
	if publicRef != "" || codeRef != tokenA.ID {
		t.Fatalf("expected public cleared, got %q/%q", publicRef, codeRef)
	}
}

// 更新时引用不属于当前用户返回 40420，且失败请求不改动现值。
func TestProjectServiceUpdate_TokenReferenceValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-upd-ref-err")
	other := createTestUser(t, svc.db, "user-upd-ref-other")
	foreign := createTestToken(t, svc.db, svc.cfg.Encryption.Key, other.ID, "ghp_foreign_upd")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_keep")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})
	projectSvc := newTestProjectService(svc)

	for _, tc := range []struct {
		name     string
		publicID string
	}{
		{"missing", "nonexistent-token-id"},
		{"foreign", foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
				PublicGithubTokenId: TokenRefUpdate{Set: true, Value: tc.publicID},
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || !errors.Is(appErr, errs.ErrGitHubTokenNotFound) {
				t.Fatalf("expected ErrGitHubTokenNotFound, got %v", err)
			}
		})
	}

	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.PublicGitHubTokenID != token.ID {
		t.Fatalf("expected failed updates to leave reference unchanged, got %q", stored.PublicGitHubTokenID)
	}
}

// 响应投影：两个引用字段原样返回（未配置为空串），不再有 has_github_* 布尔标记。
func TestProjectServiceResponse_TokenReferences(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-resp-refs")
	projectSvc := newTestProjectService(svc)

	bare := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = ""
	})
	resp, err := projectSvc.Get(bare.ID, user.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.PublicGithubTokenId != "" || resp.CodeGithubTokenId != "" {
		t.Fatalf("expected empty references, got %q/%q", resp.PublicGithubTokenId, resp.CodeGithubTokenId)
	}

	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_linked")
	linked := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})
	resp, err = projectSvc.Get(linked.ID, user.ID)
	if err != nil {
		t.Fatalf("get linked: %v", err)
	}
	if resp.PublicGithubTokenId != token.ID || resp.CodeGithubTokenId != token.ID {
		t.Fatalf("expected references=%s, got %q/%q", token.ID, resp.PublicGithubTokenId, resp.CodeGithubTokenId)
	}
}

// List 的 issue_count 统计项目 Issue 总数（open、closed 都计入），
// issue_workflow_counts 只计 state=open 的 Issue 按内部状态分组（无 internal
// meta 计入 unset，open+done 仍算入 done，closed 不进任何分段）；无 Issue 的
// 项目两字段都缺省（nil）；Get 等详情响应不携带这些字段。
func TestProjectServiceList_IssueCount(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-issue-count")
	projectSvc := newTestProjectService(svc)

	busy := createTestProject(t, svc.db, user.ID)
	quiet := createTestProject(t, svc.db, user.ID)
	empty := createTestProject(t, svc.db, user.ID)

	// 后续 Issue 共用项目时避开 (project_id, github_issue_id) 唯一索引；
	// 只改刚写入的 1001 行，已 bump 过的旧行保持原值
	bumpGitHubIssueID := func(projectID string, id int64) {
		if err := svc.db.Model(&model.IssueGitHubMeta{}).
			Where("project_id = ? AND github_issue_id = 1001", projectID).
			Update("github_issue_id", id).Error; err != nil {
			t.Fatalf("bump github_issue_id: %v", err)
		}
	}

	createTestIssue(t, svc.db, busy.ID, func(i *model.Issue) { i.SequenceNumber = 1 })
	bumpGitHubIssueID(busy.ID, 1002)
	second := createTestIssue(t, svc.db, busy.ID, func(i *model.Issue) {
		i.SequenceNumber = 2
		i.State = model.IssueStateClosed
	})
	bumpGitHubIssueID(busy.ID, 1003)
	third := createTestIssue(t, svc.db, busy.ID, func(i *model.Issue) { i.SequenceNumber = 3 })
	fourth := createTestIssue(t, svc.db, quiet.ID, func(i *model.Issue) { i.SequenceNumber = 1 })
	bumpGitHubIssueID(quiet.ID, 1002)
	fifth := createTestIssue(t, svc.db, quiet.ID, func(i *model.Issue) {
		i.SequenceNumber = 2
		i.State = model.IssueStateClosed
	})

	// busy：open 无 meta（unset）+ closed done + open done；
	// quiet：open in_progress + closed todo
	for issueID, status := range map[string]model.IssueWorkflowStatus{
		second.ID: model.IssueWorkflowStatusDone,
		third.ID:  model.IssueWorkflowStatusDone,
		fourth.ID: model.IssueWorkflowStatusInProgress,
		fifth.ID:  model.IssueWorkflowStatusTodo,
	} {
		if err := svc.db.Create(&model.IssueInternalMeta{
			IssueID:        issueID,
			WorkflowStatus: status,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}).Error; err != nil {
			t.Fatalf("create internal meta: %v", err)
		}
	}

	resp, total, err := projectSvc.List(user.ID, 1, 10)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if total != 3 || len(resp) != 3 {
		t.Fatalf("expected 3 projects, got total=%d len=%d", total, len(resp))
	}

	counts := make(map[string]*int, len(resp))
	workflowCounts := make(map[string]*api.IssueWorkflowCounts, len(resp))
	for _, p := range resp {
		counts[p.Id] = p.IssueCount
		workflowCounts[p.Id] = p.IssueWorkflowCounts
	}
	if got := counts[busy.ID]; got == nil || *got != 3 {
		t.Fatalf("busy issue_count = %v, want 3（closed 也计入）", got)
	}
	if got := counts[quiet.ID]; got == nil || *got != 2 {
		t.Fatalf("quiet issue_count = %v, want 2（closed 也计入）", got)
	}
	if got := counts[empty.ID]; got != nil {
		t.Fatalf("empty issue_count = %v, want nil", *got)
	}

	if got := workflowCounts[busy.ID]; got == nil ||
		got.Unset != 1 || got.Done != 1 || got.Todo != 0 || got.InProgress != 0 {
		t.Fatalf("busy issue_workflow_counts = %+v, want {unset:1 done:1}（closed done 不计）", got)
	}
	if got := workflowCounts[quiet.ID]; got == nil ||
		got.InProgress != 1 || got.Unset != 0 || got.Todo != 0 || got.Done != 0 {
		t.Fatalf("quiet issue_workflow_counts = %+v, want {in_progress:1}（closed todo 不计）", got)
	}
	if got := workflowCounts[empty.ID]; got != nil {
		t.Fatalf("empty issue_workflow_counts = %+v, want nil", *got)
	}

	detail, err := projectSvc.Get(busy.ID, user.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if detail.IssueCount != nil || detail.IssueWorkflowCounts != nil {
		t.Fatalf("get 详情不应携带计数: issue_count=%v issue_workflow_counts=%v",
			detail.IssueCount, detail.IssueWorkflowCounts)
	}
}

// description 按指针三态更新：nil 保留现值、指向空串清空、指向非空替换
// （INT-67 回归：Update 曾从未给 project.Description 赋值，任何取值都静默无效）。
func TestProjectServiceUpdate_Description(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-desc")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.Description = "old desc"
	})

	storedDesc := func() string {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.Description
	}

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{})
	if err != nil {
		t.Fatalf("update nil description: %v", err)
	}
	if resp.Description != "old desc" || storedDesc() != "old desc" {
		t.Fatalf("expected old desc preserved, got resp=%q stored=%q", resp.Description, storedDesc())
	}

	resp, err = projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("new desc"),
	})
	if err != nil {
		t.Fatalf("update new desc: %v", err)
	}
	if resp.Description != "new desc" || storedDesc() != "new desc" {
		t.Fatalf("expected new desc, got resp=%q stored=%q", resp.Description, storedDesc())
	}

	resp, err = projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr(""),
	})
	if err != nil {
		t.Fatalf("update empty desc: %v", err)
	}
	if resp.Description != "" || storedDesc() != "" {
		t.Fatalf("expected cleared description, got resp=%q stored=%q", resp.Description, storedDesc())
	}
}

// 创建带 code_repository_url：单独配置的代码仓库原样落库并回显；
// 与公开仓库大小写不敏感相同 → 归一为空串（空 = 同公开仓库）。
func TestProjectServiceCreate_CodeRepoURL(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-code-repo")
	projectSvc := newTestProjectService(svc)

	created, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "code-repo-project",
		RepositoryUrl:     api.Ptr("https://github.com/liuyincs/musiver"),
		CodeRepositoryUrl: api.Ptr("gitbobobo/musiver"),
	})
	if err != nil {
		t.Fatalf("create with code repo: %v", err)
	}
	if created.CodeGithubOwner != "gitbobobo" || created.CodeGithubRepo != "musiver" {
		t.Fatalf("code repo = %q/%q, want gitbobobo/musiver", created.CodeGithubOwner, created.CodeGithubRepo)
	}
	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", created.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.CodeGithubOwner != "gitbobobo" || stored.CodeGithubRepo != "musiver" {
		t.Fatalf("stored code repo = %q/%q", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}

	// code 仓库与公开仓库相同（大小写不同）→ 归一为空串。
	same, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "code-repo-same",
		RepositoryUrl:     api.Ptr("liuyincs/Musiver"),
		CodeRepositoryUrl: api.Ptr("https://github.com/LIUYINCS/musiver.git"),
	})
	if err != nil {
		t.Fatalf("create same repo: %v", err)
	}
	if same.CodeGithubOwner != "" || same.CodeGithubRepo != "" {
		t.Fatalf("same repo should normalize to empty, got %q/%q", same.CodeGithubOwner, same.CodeGithubRepo)
	}

	// 非法格式 → 40001。
	if _, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "code-repo-bad",
		CodeRepositoryUrl: api.Ptr("git bobo/musiver"),
	}); appErrorCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("invalid code repo code = %d, want %d", appErrorCode(t, err), errs.ErrInvalidParams.Code)
	}
}

// 更新 code_repository_url 三态：key 缺省保留、显式空清空（回落公开仓库）、
// 非空替换；替换成与公开仓库相同的值归一为空串；失败请求不留半改动。
func TestProjectServiceUpdate_CodeRepoURLThreeStates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-code-repo-upd")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "liuyincs"
		p.GithubRepo = "musiver"
		p.CodeGithubOwner = "gitbobobo"
		p.CodeGithubRepo = "musiver"
	})

	storedCode := func() (string, string) {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.CodeGithubOwner, stored.CodeGithubRepo
	}

	// key 缺省：保留现值。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("code repo untouched"),
	}); err != nil {
		t.Fatalf("update without code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "gitbobobo" || repo != "musiver" {
		t.Fatalf("expected code repo preserved, got %q/%q", owner, repo)
	}

	// 非空替换。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "https://github.com/other/code.git"},
	}); err != nil {
		t.Fatalf("update replace code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "other" || repo != "code" {
		t.Fatalf("expected other/code, got %q/%q", owner, repo)
	}

	// 替换成与公开仓库相同的值（大小写不同）→ 归一为空串。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "LIUYINCS/MUSIVER"},
	}); err != nil {
		t.Fatalf("update same-as-public code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("same-as-public should normalize to empty, got %q/%q", owner, repo)
	}

	// 显式空串清空（代码仓库本就为空时仍是合法三态操作）。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "again/repo"},
	}); err != nil {
		t.Fatalf("set again: %v", err)
	}
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true},
	}); err != nil {
		t.Fatalf("update clear code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("expected cleared code repo, got %q/%q", owner, repo)
	}

	// 非法格式 → 40001，且不改动现值。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "bad repo"},
	}); appErrorCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("invalid code repo code = %d, want %d", appErrorCode(t, err), errs.ErrInvalidParams.Code)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("failed update must leave value unchanged, got %q/%q", owner, repo)
	}

	// —— 归一收敛为「应用完仓库字段后的最后一步」的回归 ——

	seedEnabled := func(repo string) {
		t.Helper()
		now := time.Now().UTC()
		if err := svc.db.Where("project_id = ?", project.ID).
			Delete(&model.ProjectReviewSettings{}).Error; err != nil {
			t.Fatalf("clear settings: %v", err)
		}
		if err := svc.db.Create(&model.ProjectReviewSettings{
			ProjectID: project.ID, UserID: user.ID, RepoFullName: repo, Enabled: true,
			CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}
	loadSettings := func() model.ProjectReviewSettings {
		t.Helper()
		var row model.ProjectReviewSettings
		if err := svc.db.Where("project_id = ?", project.ID).First(&row).Error; err != nil {
			t.Fatalf("load settings: %v", err)
		}
		return row
	}

	// 同一请求里公开与代码仓库指向同一仓库（大小写不同）→ code 归一为空、
	// 审查停用且快照刷新为新公开仓库。
	seedEnabled("liuyincs/musiver") // 此时代码仓库为空，审查仓库回落公开仓库
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		RepositoryUrl:     api.Ptr("new/pub"),
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "NEW/Pub"},
	}); err != nil {
		t.Fatalf("update both repos: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("同一请求 code=公开（大小写不同）应归一为空, got %q/%q", owner, repo)
	}
	if row := loadSettings(); row.Enabled || row.RepoFullName != "new/pub" {
		t.Fatalf("审查应停用且快照刷新为 new/pub: enabled=%v repo=%q", row.Enabled, row.RepoFullName)
	}

	// code 仓库等于旧公开仓库、同时公开仓库改走：code 按请求显式落库，
	// 不被误清空（归一只清「改完之后两者相同」的情形）。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		RepositoryUrl:     api.Ptr("another/pub2"),
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "liuyincs/musiver"},
	}); err != nil {
		t.Fatalf("update code to old public: %v", err)
	}
	if owner, repo := storedCode(); owner != "liuyincs" || repo != "musiver" {
		t.Fatalf("code 应按请求显式落库, got %q/%q", owner, repo)
	}
	if row := loadSettings(); row.RepoFullName != "liuyincs/musiver" {
		t.Fatalf("审查仓库已变为 liuyincs/musiver, snapshot = %q", row.RepoFullName)
	}

	// 只改公开仓库使其等于存量显式代码仓库：归一成空；审查仓库没变
	// （liuyincs/musiver → liuyincs/musiver），不停用、快照保持正确。
	seedEnabled("liuyincs/musiver")
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		RepositoryUrl: api.Ptr("LIUYINCS/Musiver"),
	}); err != nil {
		t.Fatalf("update public onto code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("公开仓库撞存量 code 仓库应归一为空, got %q/%q", owner, repo)
	}
	if row := loadSettings(); !row.Enabled || row.RepoFullName != "liuyincs/musiver" {
		t.Fatalf("审查仓库未变不应停用: enabled=%v repo=%q", row.Enabled, row.RepoFullName)
	}
	var storedPublic model.Project
	if err := svc.db.First(&storedPublic, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if storedPublic.GithubOwner != "LIUYINCS" || storedPublic.GithubRepo != "Musiver" {
		t.Fatalf("公开仓库应原样落库, got %q/%q", storedPublic.GithubOwner, storedPublic.GithubRepo)
	}
}

// 改代码仓库会停用审查并刷新 repo_full_name 快照（新的 CodeRepo 小写）；
// 代码仓库已显式配置时，只改公开仓库不停用审查——审查仓库未变。
func TestProjectRepoUpdate_CodeRepoSyncsReviewSettings(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-code-repo-sync")

	seedEnabled := func(projectID string, repo string) {
		t.Helper()
		now := time.Now().UTC()
		if err := svc.db.Create(&model.ProjectReviewSettings{
			ProjectID: projectID, UserID: user.ID, RepoFullName: repo, Enabled: true,
			CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}

	// 只改代码仓库：停用 + 快照刷新为新代码仓库。
	withCode := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "proj"
	})
	seedEnabled(withCode.ID, "pub/proj")
	withCode.CodeGithubOwner = "gitbobobo"
	withCode.CodeGithubRepo = "musiver"
	if err := svc.projectRepo.Update(withCode); err != nil {
		t.Fatalf("update code repo: %v", err)
	}
	var gotCode model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", withCode.ID).First(&gotCode).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if gotCode.Enabled {
		t.Fatal("改代码仓库后审查设置应停用")
	}
	if gotCode.RepoFullName != "gitbobobo/musiver" {
		t.Fatalf("repo_full_name = %q, want gitbobobo/musiver", gotCode.RepoFullName)
	}

	// 只改公开仓库（代码仓库显式配置）：审查不停用、快照不变。
	codeOnly := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "proj"
		p.CodeGithubOwner = "gitbobobo"
		p.CodeGithubRepo = "musiver"
	})
	seedEnabled(codeOnly.ID, "gitbobobo/musiver")
	codeOnly.GithubOwner = "AnotherPub"
	codeOnly.GithubRepo = "AnotherProj"
	if err := svc.projectRepo.Update(codeOnly); err != nil {
		t.Fatalf("update public repo only: %v", err)
	}
	var gotCodeOnly model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", codeOnly.ID).First(&gotCodeOnly).Error; err != nil {
		t.Fatalf("load settings 2: %v", err)
	}
	if !gotCodeOnly.Enabled || gotCodeOnly.RepoFullName != "gitbobobo/musiver" {
		t.Fatalf("只改公开仓库不应影响审查: enabled=%v repo=%q", gotCodeOnly.Enabled, gotCodeOnly.RepoFullName)
	}

	// 未配置代码仓库时改公开仓库：行为与现状一致（停用 + 刷新）。
	fallback := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "old"
		p.GithubRepo = "repo"
	})
	seedEnabled(fallback.ID, "old/repo")
	fallback.GithubOwner = "new"
	fallback.GithubRepo = "Repo"
	if err := svc.projectRepo.Update(fallback); err != nil {
		t.Fatalf("update public repo fallback: %v", err)
	}
	var gotFallback model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", fallback.ID).First(&gotFallback).Error; err != nil {
		t.Fatalf("load settings 3: %v", err)
	}
	if gotFallback.Enabled || gotFallback.RepoFullName != "new/repo" {
		t.Fatalf("fallback 项目改公开仓库应停用并刷新: enabled=%v repo=%q", gotFallback.Enabled, gotFallback.RepoFullName)
	}
}
