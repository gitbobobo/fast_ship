package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTestCredentials(svc *testServices) *ProjectCredentials {
	return newProjectCredentials(svc.projectRepo, svc.tokenRepo, svc.cfg, zap.NewNop())
}

// requiredGitHubToken：未关联仓库返回 40003。
func TestRequiredGitHubToken_NotConfigured(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{ID: "p-no-github"}

	token, appErr := creds.requiredGitHubToken(project)
	if token != nil || !errors.Is(appErr, errs.ErrProjectGitHubNotConfigured) {
		t.Fatalf("expected ErrProjectGitHubNotConfigured, got token=%v err=%v", token, appErr)
	}
}

// requiredGitHubToken：已关联仓库但未配公开仓库 Token 返回 40006，
// 且绝不借用代码仓库 Token 兜底——两个用途互不兜底。
func TestRequiredGitHubToken_MissingPublicToken(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-no-public")
	codeToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "code-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = codeToken.ID
	})

	token, appErr := creds.requiredGitHubToken(project)
	if token != nil || !errors.Is(appErr, errs.ErrProjectPublicGitHubTokenMissing) {
		t.Fatalf("expected ErrProjectPublicGitHubTokenMissing, got token=%v err=%v", token, appErr)
	}
	if appErr.Code != 40006 {
		t.Fatalf("expected code 40006, got %d", appErr.Code)
	}
}

// requiredGitHubToken：引用行不存在（悬空引用）报 50000，不静默当作未配置。
func TestRequiredGitHubToken_DanglingReference(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-dangling")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = "nonexistent-token-id"
		p.CodeGitHubTokenID = ""
	})

	token, appErr := creds.requiredGitHubToken(project)
	if token != nil || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 for dangling reference, got token=%v err=%v", token, appErr)
	}
}

// requiredGitHubToken：有引用则解密该行；密文损坏报 50000，不换另一把、不降级匿名。
func TestRequiredGitHubToken_CorruptCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-corrupt")
	codeToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "code-token")
	broken := createCorruptTestToken(t, svc.db, user.ID)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = broken.ID
		p.CodeGitHubTokenID = codeToken.ID
	})
	_ = codeToken

	token, appErr := creds.requiredGitHubToken(project)
	if token != nil || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 for corrupt ciphertext, got token=%v err=%v", token, appErr)
	}
	if !strings.Contains(appErr.Message, "公开仓库 Token") {
		t.Fatalf("expected purpose name in message, got %q", appErr.Message)
	}
}

func TestRequiredGitHubToken_DecryptsValidToken(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-ok")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
	})

	got, appErr := creds.requiredGitHubToken(project)
	if appErr != nil || string(got) != "gh-token" {
		t.Fatalf("expected decrypted token, got token=%q err=%v", got, appErr)
	}
}

// pullRequestCredential：已配代码仓库 Token 只用它，解密失败不回退公开/匿名；
// 未配代码仓库 Token 匿名（即使公开仓库 Token 已配，也不借来读 PR）。
func TestPullRequestCredential_Semantics(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-pr-cred")
	publicToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "public-token")
	codeToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "code-token")

	withCode := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = publicToken.ID
		p.CodeGitHubTokenID = codeToken.ID
	})
	_ = codeToken
	got, kind, appErr := creds.pullRequestCredential(withCode)
	if appErr != nil || kind != pullRequestCredentialCode || string(got) != "code-token" {
		t.Fatalf("expected code token, got token=%q kind=%q err=%v", got, kind, appErr)
	}

	anonymous := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = publicToken.ID
		p.CodeGitHubTokenID = ""
	})
	got, kind, appErr = creds.pullRequestCredential(anonymous)
	if got != nil || kind != pullRequestCredentialAnonymous || appErr != nil {
		t.Fatalf("expected anonymous even with public token, got token=%v kind=%q err=%v", got, kind, appErr)
	}
}

// 代码仓库 Token 密文损坏报 50000 且点名「代码仓库 Token」，不回退公开 Token 或匿名。
func TestPullRequestCredential_CorruptCodeTokenNoFallback(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-pr-corrupt")
	publicToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "public-token")
	broken := createCorruptTestToken(t, svc.db, user.ID)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = publicToken.ID
		p.CodeGitHubTokenID = broken.ID
	})

	got, kind, appErr := creds.pullRequestCredential(project)
	if got != nil || kind != pullRequestCredentialCode || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 with code kind, got token=%v kind=%q err=%v", got, kind, appErr)
	}
	if !strings.Contains(appErr.Message, "代码仓库 Token") {
		t.Fatalf("expected credential name in message, got %q", appErr.Message)
	}
}

// attach PR 时代码仓库 Token 解密失败必须报错且不落记录，client factory 不得被调用。
func TestAttachPullRequest_CorruptTokenDoesNotFallBackToAnonymous(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-attach-corrupt")
	broken := createCorruptTestToken(t, svc.db, user.ID)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.CodeGitHubTokenID = broken.ID
	})
	issue := createInternalTestIssue(t, svc.db, project.ID)

	svc.issueService.newPRClient = func(string) githubapi.GitHubClient {
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

// 解密失败的日志只准带 project_id/token_id 与底层错误，明文与密文不外泄。
func TestCredentials_FailureLogContainsNoSecrets(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-log-check")
	ciphertext := encryptTestToken(t, svc.cfg, "secret-token-plain")
	token := &model.UserGitHubToken{
		ID:               "token-log-check",
		UserID:           user.ID,
		Title:            "log check",
		TokenEncrypted:   ciphertext,
		TokenFingerprint: "fp-log-check",
		TokenPrefix:      "secret-t",
		DetectionStatus:  model.TokenDetectionUnchecked,
		DetectionType:    model.TokenTypeUnknown,
		DetectionScopes:  "[]",
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := svc.db.Create(token).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
	})
	// 换一把密钥让解密失败（等价于密文损坏）。
	wrongKeyCfg := &config.Config{Encryption: config.EncryptionConfig{Key: "abcdefghijklmnopqrstuvwxyz123456"}}

	core, observed := observer.New(zap.ErrorLevel)
	creds := newProjectCredentials(svc.projectRepo, svc.tokenRepo, wrongKeyCfg, zap.New(core))
	got, appErr := creds.requiredGitHubToken(project)
	if got != nil || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 for wrong key, got token=%v err=%v", got, appErr)
	}

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("expected one log entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["project_id"] != project.ID || fields["token_id"] != token.ID || fields["error"] == nil {
		t.Fatalf("expected project_id/token_id/error fields, got %v", fields)
	}
	for _, leaked := range []string{"secret-token-plain", string(ciphertext)} {
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

// CheckTokenReference：空引用合法；本人 Token 通过；他人 Token 与不存在的 id
// 都返回 40420（跨用户不可见）。
func TestCheckTokenReference(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-ref")
	other := createTestUser(t, svc.db, "user-ref-other")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "mine")
	foreign := createTestToken(t, svc.db, svc.cfg.Encryption.Key, other.ID, "theirs")

	if appErr := creds.CheckTokenReference(user.ID, ""); appErr != nil {
		t.Fatalf("empty reference must be allowed, got %v", appErr)
	}
	if appErr := creds.CheckTokenReference(user.ID, token.ID); appErr != nil {
		t.Fatalf("own token must pass, got %v", appErr)
	}
	for name, tokenID := range map[string]string{
		"missing":  "nonexistent-token-id",
		"foreign ": foreign.ID,
	} {
		appErr := creds.CheckTokenReference(user.ID, tokenID)
		if appErr == nil || !errors.Is(appErr, errs.ErrGitHubTokenNotFound) {
			t.Fatalf("%s: expected ErrGitHubTokenNotFound, got %v", name, appErr)
		}
		if appErr.Code != 40420 {
			t.Fatalf("expected code 40420, got %d", appErr.Code)
		}
	}
}

// 贯通断言：同一 Token 记录可同时被公开仓库与代码仓库两个用途引用，
// 两条读路径解出同一明文。
func TestProjectCredentials_SameTokenBothPurposes(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-both")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "shared-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	required, appErr := creds.requiredGitHubToken(project)
	if appErr != nil || string(required) != "shared-token" {
		t.Fatalf("required path: got token=%q err=%v", required, appErr)
	}
	pr, kind, appErr := creds.pullRequestCredential(project)
	if appErr != nil || kind != pullRequestCredentialCode || string(pr) != "shared-token" {
		t.Fatalf("pr path: got token=%q kind=%q err=%v", pr, kind, appErr)
	}
}
