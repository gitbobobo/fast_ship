package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRequiredProjectGitHubToken_NotConfigured(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{ID: "p-no-github"}

	token, appErr := requiredProjectGitHubToken(project, svc.cfg, zap.NewNop())
	if token != nil || !errors.Is(appErr, errs.ErrProjectGitHubNotConfigured) {
		t.Fatalf("expected ErrProjectGitHubNotConfigured, got token=%v err=%v", token, appErr)
	}
}

func TestRequiredProjectGitHubToken_CorruptCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{
		ID:                   "p-bad-token",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, appErr := requiredProjectGitHubToken(project, svc.cfg, zap.NewNop())
	if token != nil || !errors.Is(appErr, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal for corrupt ciphertext, got token=%v err=%v", token, appErr)
	}
}

func TestRequiredProjectGitHubToken_DecryptsValidToken(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{
		ID:                   "p-ok",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: encryptTestToken(t, svc.cfg, "gh-token"),
	}

	token, appErr := requiredProjectGitHubToken(project, svc.cfg, zap.NewNop())
	if appErr != nil || string(token) != "gh-token" {
		t.Fatalf("expected decrypted token, got token=%q err=%v", token, appErr)
	}
}

func TestOptionalProjectGitHubToken_NotConfiguredReturnsNil(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{ID: "p-no-github"}

	token, appErr := optionalProjectGitHubToken(project, svc.cfg, zap.NewNop())
	if token != nil || appErr != nil {
		t.Fatalf("expected (nil, nil) for unconfigured project, got token=%v err=%v", token, appErr)
	}
}

// 已配置但密文损坏必须报 50000：静默退回匿名会把凭证故障误判成「未配置」，
// 对外表现为匿名访问私有仓库/被 60/hr 限流而不是真实的内部错误。
func TestOptionalProjectGitHubToken_CorruptCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{
		ID:                   "p-bad-token",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, appErr := optionalProjectGitHubToken(project, svc.cfg, zap.NewNop())
	if token != nil || !errors.Is(appErr, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal for corrupt ciphertext, got token=%v err=%v", token, appErr)
	}
}

// 调用层契约：attach PR 时凭证损坏必须报错且不落记录，client factory 不得被调用——
// 证明 optional 路径的失败不会静默退回匿名客户端。
func TestAttachPullRequest_CorruptTokenDoesNotFallBackToAnonymous(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = []byte("corrupt-ciphertext")
	})
	issue := createInternalTestIssue(t, svc.db, project.ID)

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		t.Fatalf("github client must not be built when token decryption fails")
		return nil
	}

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected ErrInternal, got %v", err)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link row on decrypt failure, got %d", count)
	}
}

// 密钥不匹配（等价于密文损坏）走同一解密失败路径；日志只准带 project_id 与底层错误，
// 明文 token 与密文都不得出现在日志里。
func TestProjectGitHubToken_FailureLogContainsNoSecrets(t *testing.T) {
	svc := setupTestServices(t)
	ciphertext := encryptTestToken(t, svc.cfg, "gh-token")
	project := &model.Project{
		ID:                   "p-log-check",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: ciphertext,
	}
	wrongKeyCfg := &config.Config{Encryption: config.EncryptionConfig{Key: "abcdefghijklmnopqrstuvwxyz123456"}}

	core, observed := observer.New(zap.ErrorLevel)
	token, appErr := requiredProjectGitHubToken(project, wrongKeyCfg, zap.New(core))
	if token != nil || !errors.Is(appErr, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal for wrong key, got token=%v err=%v", token, appErr)
	}

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("expected one log entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["project_id"] != "p-log-check" || fields["error"] == nil {
		t.Fatalf("expected project_id and error fields, got %v", fields)
	}
	for _, leaked := range []string{"gh-token", string(ciphertext)} {
		if strings.Contains(entries[0].Message, leaked) {
			t.Fatalf("log message leaks secret %q: %s", leaked, entries[0].Message)
		}
		for _, v := range fields {
			if s, ok := v.(string); ok && strings.Contains(s, leaked) {
				t.Fatalf("log fields leak secret %q: %v", leaked, fields)
			}
		}
	}
}
