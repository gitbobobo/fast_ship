package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
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
