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

// 已配 PR 访问 Token 时优先且只使用它——即便项目 Token 同样有效也选 PR Token；
// internal 项目（未配反馈仓库）只配 PR Token 也要生效。
func TestResolvePullRequestCredential_PRTokenPreferred(t *testing.T) {
	svc := setupTestServices(t)

	withBoth := &model.Project{
		ID:                     "p-both",
		GithubOwner:            "owner",
		GithubRepo:             "repo",
		GithubTokenEncrypted:   encryptTestToken(t, svc.cfg, "gh-token"),
		GithubPRTokenEncrypted: encryptTestToken(t, svc.cfg, "pr-token"),
	}
	token, kind, appErr := resolvePullRequestCredential(withBoth, svc.cfg, zap.NewNop())
	if appErr != nil || kind != pullRequestCredentialPR || string(token) != "pr-token" {
		t.Fatalf("expected pr token, got token=%q kind=%q err=%v", token, kind, appErr)
	}

	internalOnly := &model.Project{
		ID:                     "p-internal",
		GithubPRTokenEncrypted: encryptTestToken(t, svc.cfg, "pr-token"),
	}
	token, kind, appErr = resolvePullRequestCredential(internalOnly, svc.cfg, zap.NewNop())
	if appErr != nil || kind != pullRequestCredentialPR || string(token) != "pr-token" {
		t.Fatalf("expected pr token on internal project, got token=%q kind=%q err=%v", token, kind, appErr)
	}
}

// 未配 PR Token 时回退现状：已配项目 Token 用项目 Token，未配置走匿名。
func TestResolvePullRequestCredential_Fallbacks(t *testing.T) {
	svc := setupTestServices(t)

	configured := &model.Project{
		ID:                   "p-project",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: encryptTestToken(t, svc.cfg, "gh-token"),
	}
	token, kind, appErr := resolvePullRequestCredential(configured, svc.cfg, zap.NewNop())
	if appErr != nil || kind != pullRequestCredentialProject || string(token) != "gh-token" {
		t.Fatalf("expected project token, got token=%q kind=%q err=%v", token, kind, appErr)
	}

	anonymous := &model.Project{ID: "p-anon"}
	token, kind, appErr = resolvePullRequestCredential(anonymous, svc.cfg, zap.NewNop())
	if appErr != nil || kind != pullRequestCredentialAnonymous || token != nil {
		t.Fatalf("expected anonymous, got token=%v kind=%q err=%v", token, kind, appErr)
	}
}

// PR Token 密文损坏必须报 50000 且不回退：项目 Token 有效也不能顶上来，
// 否则用户以为 PR Token 在用、实际走的却是另一个凭证。错误文案须点名
// 「PR 访问 Token」——否则用户不知道该去检查哪个凭证。
func TestResolvePullRequestCredential_CorruptPRTokenNoFallback(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{
		ID:                     "p-bad-pr",
		GithubOwner:            "owner",
		GithubRepo:             "repo",
		GithubTokenEncrypted:   encryptTestToken(t, svc.cfg, "gh-token"),
		GithubPRTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, kind, appErr := resolvePullRequestCredential(project, svc.cfg, zap.NewNop())
	if token != nil || kind != pullRequestCredentialPR || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 with pr kind, got token=%v kind=%q err=%v", token, kind, appErr)
	}
	if !strings.Contains(appErr.Message, "PR 访问 Token") {
		t.Fatalf("expected credential name in message, got %q", appErr.Message)
	}
}

// 项目 Token 密文损坏同样点名「项目 Token」。
func TestResolvePullRequestCredential_CorruptProjectTokenNamed(t *testing.T) {
	svc := setupTestServices(t)
	project := &model.Project{
		ID:                   "p-bad-proj",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, kind, appErr := resolvePullRequestCredential(project, svc.cfg, zap.NewNop())
	if token != nil || kind != pullRequestCredentialProject || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 with project kind, got token=%v kind=%q err=%v", token, kind, appErr)
	}
	if !strings.Contains(appErr.Message, "项目 Token") {
		t.Fatalf("expected credential name in message, got %q", appErr.Message)
	}
}

// PR Token 解密失败的日志同样只准带 project_id 与底层错误，明文与密文不外泄。
func TestResolvePullRequestCredential_FailureLogContainsNoSecrets(t *testing.T) {
	svc := setupTestServices(t)
	ciphertext := encryptTestToken(t, svc.cfg, "pr-token")
	project := &model.Project{
		ID:                     "p-pr-log",
		GithubPRTokenEncrypted: ciphertext,
	}
	wrongKeyCfg := &config.Config{Encryption: config.EncryptionConfig{Key: "abcdefghijklmnopqrstuvwxyz123456"}}

	core, observed := observer.New(zap.ErrorLevel)
	token, _, appErr := resolvePullRequestCredential(project, wrongKeyCfg, zap.New(core))
	if token != nil || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 for wrong key, got token=%v err=%v", token, appErr)
	}

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("expected one log entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["project_id"] != "p-pr-log" || fields["error"] == nil {
		t.Fatalf("expected project_id and error fields, got %v", fields)
	}
	for _, leaked := range []string{"pr-token", string(ciphertext)} {
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
