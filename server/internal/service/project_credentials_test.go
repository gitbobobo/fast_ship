package service

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTestCredentials(svc *testServices) *ProjectCredentials {
	return newProjectCredentials(svc.projectRepo, svc.cfg, zap.NewNop())
}

func decryptTestToken(t *testing.T, svc *testServices, ciphertext []byte) string {
	t.Helper()
	plain, err := crypto.Decrypt(ciphertext, []byte(svc.cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("decrypt test token: %v", err)
	}
	return string(plain)
}

func TestRequiredGitHubToken_NotConfigured(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{ID: "p-no-github"}

	token, appErr := creds.requiredGitHubToken(project)
	if token != nil || !errors.Is(appErr, errs.ErrProjectGitHubNotConfigured) {
		t.Fatalf("expected ErrProjectGitHubNotConfigured, got token=%v err=%v", token, appErr)
	}
}

func TestRequiredGitHubToken_CorruptCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{
		ID:                   "p-bad-token",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, appErr := creds.requiredGitHubToken(project)
	if token != nil || !errors.Is(appErr, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal for corrupt ciphertext, got token=%v err=%v", token, appErr)
	}
}

func TestRequiredGitHubToken_DecryptsValidToken(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{
		ID:                   "p-ok",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: encryptTestToken(t, svc.cfg, "gh-token"),
	}

	token, appErr := creds.requiredGitHubToken(project)
	if appErr != nil || string(token) != "gh-token" {
		t.Fatalf("expected decrypted token, got token=%q err=%v", token, appErr)
	}
}

func TestOptionalGitHubToken_NotConfiguredReturnsNil(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{ID: "p-no-github"}

	token, appErr := creds.optionalGitHubToken(project)
	if token != nil || appErr != nil {
		t.Fatalf("expected (nil, nil) for unconfigured project, got token=%v err=%v", token, appErr)
	}
}

// 已配置但密文损坏必须报 50000：静默退回匿名会把凭证故障误判成「未配置」，
// 对外表现为匿名访问私有仓库/被 60/hr 限流而不是真实的内部错误。
func TestOptionalGitHubToken_CorruptCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{
		ID:                   "p-bad-token",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, appErr := creds.optionalGitHubToken(project)
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
func TestPullRequestCredential_PRTokenPreferred(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)

	withBoth := &model.Project{
		ID:                     "p-both",
		GithubOwner:            "owner",
		GithubRepo:             "repo",
		GithubTokenEncrypted:   encryptTestToken(t, svc.cfg, "gh-token"),
		GithubPRTokenEncrypted: encryptTestToken(t, svc.cfg, "pr-token"),
	}
	token, kind, appErr := creds.pullRequestCredential(withBoth)
	if appErr != nil || kind != pullRequestCredentialPR || string(token) != "pr-token" {
		t.Fatalf("expected pr token, got token=%q kind=%q err=%v", token, kind, appErr)
	}

	internalOnly := &model.Project{
		ID:                     "p-internal",
		GithubPRTokenEncrypted: encryptTestToken(t, svc.cfg, "pr-token"),
	}
	token, kind, appErr = creds.pullRequestCredential(internalOnly)
	if appErr != nil || kind != pullRequestCredentialPR || string(token) != "pr-token" {
		t.Fatalf("expected pr token on internal project, got token=%q kind=%q err=%v", token, kind, appErr)
	}
}

// 未配 PR Token 时回退现状：已配项目 Token 用项目 Token，未配置走匿名。
func TestPullRequestCredential_Fallbacks(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)

	configured := &model.Project{
		ID:                   "p-project",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: encryptTestToken(t, svc.cfg, "gh-token"),
	}
	token, kind, appErr := creds.pullRequestCredential(configured)
	if appErr != nil || kind != pullRequestCredentialProject || string(token) != "gh-token" {
		t.Fatalf("expected project token, got token=%q kind=%q err=%v", token, kind, appErr)
	}

	anonymous := &model.Project{ID: "p-anon"}
	token, kind, appErr = creds.pullRequestCredential(anonymous)
	if appErr != nil || kind != pullRequestCredentialAnonymous || token != nil {
		t.Fatalf("expected anonymous, got token=%v kind=%q err=%v", token, kind, appErr)
	}
}

// PR Token 密文损坏必须报 50000 且不回退：项目 Token 有效也不能顶上来，
// 否则用户以为 PR Token 在用、实际走的却是另一个凭证。错误文案须点名
// 「PR 访问 Token」——否则用户不知道该去检查哪个凭证。
func TestPullRequestCredential_CorruptPRTokenNoFallback(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{
		ID:                     "p-bad-pr",
		GithubOwner:            "owner",
		GithubRepo:             "repo",
		GithubTokenEncrypted:   encryptTestToken(t, svc.cfg, "gh-token"),
		GithubPRTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, kind, appErr := creds.pullRequestCredential(project)
	if token != nil || kind != pullRequestCredentialPR || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 with pr kind, got token=%v kind=%q err=%v", token, kind, appErr)
	}
	if !strings.Contains(appErr.Message, "PR 访问 Token") {
		t.Fatalf("expected credential name in message, got %q", appErr.Message)
	}
}

// 项目 Token 密文损坏同样点名「项目 Token」。
func TestPullRequestCredential_CorruptProjectTokenNamed(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	project := &model.Project{
		ID:                   "p-bad-proj",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("corrupt-ciphertext"),
	}

	token, kind, appErr := creds.pullRequestCredential(project)
	if token != nil || kind != pullRequestCredentialProject || appErr == nil || appErr.Code != errs.ErrInternal.Code {
		t.Fatalf("expected 50000 with project kind, got token=%v kind=%q err=%v", token, kind, appErr)
	}
	if !strings.Contains(appErr.Message, "项目 Token") {
		t.Fatalf("expected credential name in message, got %q", appErr.Message)
	}
}

// PR Token 解密失败的日志同样只准带 project_id 与底层错误，明文与密文不外泄。
func TestPullRequestCredential_FailureLogContainsNoSecrets(t *testing.T) {
	svc := setupTestServices(t)
	ciphertext := encryptTestToken(t, svc.cfg, "pr-token")
	project := &model.Project{
		ID:                     "p-pr-log",
		GithubPRTokenEncrypted: ciphertext,
	}
	wrongKeyCfg := &config.Config{Encryption: config.EncryptionConfig{Key: "abcdefghijklmnopqrstuvwxyz123456"}}

	core, observed := observer.New(zap.ErrorLevel)
	creds := newProjectCredentials(svc.projectRepo, wrongKeyCfg, zap.New(core))
	token, _, appErr := creds.pullRequestCredential(project)
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
func TestGitHubToken_FailureLogContainsNoSecrets(t *testing.T) {
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
	creds := newProjectCredentials(svc.projectRepo, wrongKeyCfg, zap.New(core))
	token, appErr := creds.requiredGitHubToken(project)
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

// 写侧：source 复制直接复用源项目密文 blob——AES-GCM 每次加密产生随机 nonce，
// 返回字节与源列相等才能证明没有解密重加密；同传明文时 source 优先。
func TestResolveGitHubToken_SourceCopiesCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-gh-src")
	source := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "access-token-src")
	})

	got, err := creds.resolveGitHubToken(user.ID, "plaintext-ignored", source.ID)
	if err != nil {
		t.Fatalf("resolve from source: %v", err)
	}
	if !bytes.Equal(got, source.GithubTokenEncrypted) {
		t.Fatal("expected source ciphertext reused verbatim, got re-encrypted bytes")
	}
	if plain := decryptTestToken(t, svc, got); plain != "access-token-src" {
		t.Fatalf("expected copied token to decrypt to access-token-src, got %q", plain)
	}
}

func TestResolveGitHubToken_PlaintextEncrypts(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)

	got, err := creds.resolveGitHubToken("user-1", "gh-token", "")
	if err != nil {
		t.Fatalf("resolve plaintext: %v", err)
	}
	if bytes.Equal(got, []byte("gh-token")) {
		t.Fatal("expected ciphertext, got plaintext bytes")
	}
	if plain := decryptTestToken(t, svc, got); plain != "gh-token" {
		t.Fatalf("expected gh-token, got %q", plain)
	}
}

func TestResolveGitHubToken_Errors(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-gh-err")
	otherUser := createTestUser(t, svc.db, "user-gh-other")
	noToken := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = nil
	})
	source := createTestProject(t, svc.db, otherUser.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "access-token-src")
	})

	for _, tc := range []struct {
		name      string
		userID    string
		plaintext string
		sourceID  string
		wantCode  int
	}{
		{"neither provided", user.ID, "", "", errs.ErrInvalidParams.Code},
		{"source missing", user.ID, "", "nonexistent-project-id", errs.ErrProjectNotFound.Code},
		{"source has no access token", user.ID, "", noToken.ID, errs.ErrInvalidParams.Code},
		{"source owned by another user", user.ID, "", source.ID, errs.ErrProjectNotFound.Code},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := creds.resolveGitHubToken(tc.userID, tc.plaintext, tc.sourceID)
			appErr, ok := err.(*errs.AppError)
			if !ok || appErr.Code != tc.wantCode {
				t.Fatalf("expected code %d, got %v", tc.wantCode, err)
			}
		})
	}
}

// clear 与 plaintext/source 同时显式提供（指针非 nil，不论取值——false/空串
// 也算提供）返回 40001；kind 单独提供或取值非法同样 40001。
func TestResolvePRToken_ConflictAndKindValidation(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-pr-conflict")

	for _, tc := range []struct {
		name  string
		input projectPRTokenInput
	}{
		{"clear true + plaintext", projectPRTokenInput{plaintext: api.Ptr("pr-token-v2"), clear: api.Ptr(true)}},
		{"clear false + plaintext", projectPRTokenInput{plaintext: api.Ptr("pr-token-v2"), clear: api.Ptr(false)}},
		{"clear true + empty plaintext", projectPRTokenInput{plaintext: api.Ptr(""), clear: api.Ptr(true)}},
		{"clear true + source", projectPRTokenInput{sourceProjectID: api.Ptr("src"), clear: api.Ptr(true)}},
		{"clear true + empty source", projectPRTokenInput{sourceProjectID: api.Ptr(""), clear: api.Ptr(true)}},
		{"kind alone access", projectPRTokenInput{sourceKind: api.Ptr(api.Access)}},
		{"kind alone pr", projectPRTokenInput{sourceKind: api.Ptr(api.Pr)}},
		{"kind invalid", projectPRTokenInput{sourceKind: api.Ptr(api.PrTokenSourceKind("bogus")), sourceProjectID: api.Ptr("src")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := creds.resolvePRToken(user.ID, nil, tc.input)
			appErr, ok := err.(*errs.AppError)
			if !ok || appErr.Code != errs.ErrInvalidParams.Code {
				t.Fatalf("expected 40001, got %v", err)
			}
		})
	}
}

// clear=true 丢弃现值返回 nil；clear=false 单独提供按未提供处理，返回现值。
func TestResolvePRToken_ClearAndPreserve(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	current := encryptTestToken(t, svc.cfg, "pr-token-v1")

	got, err := creds.resolvePRToken("user-1", current, projectPRTokenInput{clear: api.Ptr(true)})
	if err != nil || got != nil {
		t.Fatalf("expected nil after clear, got %v err=%v", got, err)
	}

	got, err = creds.resolvePRToken("user-1", current, projectPRTokenInput{clear: api.Ptr(false)})
	if err != nil || !bytes.Equal(got, current) {
		t.Fatalf("expected current preserved on clear=false, got %v err=%v", got, err)
	}

	got, err = creds.resolvePRToken("user-1", current, projectPRTokenInput{})
	if err != nil || !bytes.Equal(got, current) {
		t.Fatalf("expected current preserved on empty input, got %v err=%v", got, err)
	}
}

// source 复制按 kind 选列：access 复制 GithubTokenEncrypted、pr（缺省与显式等价）
// 复制 GithubPRTokenEncrypted；密文 blob 原样复用；同传明文时 source 优先。
func TestResolvePRToken_SourceCopiesCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-pr-src")
	source := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "access-token-src")
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-src")
	})

	got, err := creds.resolvePRToken(user.ID, nil, projectPRTokenInput{
		sourceProjectID: api.Ptr(source.ID),
		sourceKind:      api.Ptr(api.Access),
	})
	if err != nil {
		t.Fatalf("resolve access kind: %v", err)
	}
	if !bytes.Equal(got, source.GithubTokenEncrypted) {
		t.Fatal("expected access ciphertext reused verbatim, got re-encrypted bytes")
	}
	if plain := decryptTestToken(t, svc, got); plain != "access-token-src" {
		t.Fatalf("expected access-token-src, got %q", plain)
	}

	got, err = creds.resolvePRToken(user.ID, nil, projectPRTokenInput{
		plaintext:       api.Ptr("pr-token-ignored"),
		sourceProjectID: api.Ptr(source.ID),
	})
	if err != nil {
		t.Fatalf("resolve pr kind: %v", err)
	}
	if !bytes.Equal(got, source.GithubPRTokenEncrypted) {
		t.Fatal("expected pr ciphertext reused verbatim, got re-encrypted bytes")
	}
	if plain := decryptTestToken(t, svc, got); plain != "pr-token-src" {
		t.Fatalf("expected pr-token-src, got %q", plain)
	}
}

func TestResolvePRToken_SourceErrors(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-pr-src-err")
	otherUser := createTestUser(t, svc.db, "user-pr-src-other")
	accessOnly := createTestProject(t, svc.db, user.ID)
	noAccess := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = nil
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-x")
	})
	foreign := createTestProject(t, svc.db, otherUser.ID, func(p *model.Project) {
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-src")
	})

	for _, tc := range []struct {
		name     string
		userID   string
		sourceID string
		kind     *api.PrTokenSourceKind
		wantCode int
	}{
		{"source missing", user.ID, "nonexistent-project-id", nil, errs.ErrProjectNotFound.Code},
		{"pr kind but source has no pr token", user.ID, accessOnly.ID, api.Ptr(api.Pr), errs.ErrInvalidParams.Code},
		{"access kind but source has no access token", user.ID, noAccess.ID, api.Ptr(api.Access), errs.ErrInvalidParams.Code},
		{"source owned by another user", user.ID, foreign.ID, nil, errs.ErrProjectNotFound.Code},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := creds.resolvePRToken(tc.userID, nil, projectPRTokenInput{
				sourceProjectID: api.Ptr(tc.sourceID),
				sourceKind:      tc.kind,
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || appErr.Code != tc.wantCode {
				t.Fatalf("expected code %d, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestResolvePRToken_PlaintextEncrypts(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)

	got, err := creds.resolvePRToken("user-1", nil, projectPRTokenInput{plaintext: api.Ptr("pr-token")})
	if err != nil {
		t.Fatalf("resolve plaintext: %v", err)
	}
	if plain := decryptTestToken(t, svc, got); plain != "pr-token" {
		t.Fatalf("expected pr-token, got %q", plain)
	}
}

// 贯通断言：写侧复制出的密文赋给目标项目后，读侧解密同处一个模块——
// 复制的 PR Token 密文与源列字节相等且 pullRequestCredential 解出源明文（kind=pr）；
// access kind 复制到 PR 字段后读取来源同样记为 pr；Access Token 复制走 required 路径。
func TestProjectCredentials_CopyThenDecrypt(t *testing.T) {
	svc := setupTestServices(t)
	creds := newTestCredentials(svc)
	user := createTestUser(t, svc.db, "user-roundtrip")
	source := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "access-token-src")
		p.GithubPRTokenEncrypted = encryptTestToken(t, svc.cfg, "pr-token-src")
	})

	// PR Token 复制 → 读取
	target := &model.Project{ID: "p-target"}
	copied, err := creds.resolvePRToken(user.ID, nil, projectPRTokenInput{
		sourceProjectID: api.Ptr(source.ID),
	})
	if err != nil {
		t.Fatalf("copy pr token: %v", err)
	}
	target.GithubPRTokenEncrypted = copied
	token, kind, appErr := creds.pullRequestCredential(target)
	if appErr != nil || kind != pullRequestCredentialPR || string(token) != "pr-token-src" {
		t.Fatalf("expected copied pr token readable as pr credential, got token=%q kind=%q err=%v", token, kind, appErr)
	}

	// Access Token 复制进 PR 字段 → 读取来源记为 pr
	accessCopied, err := creds.resolvePRToken(user.ID, nil, projectPRTokenInput{
		sourceProjectID: api.Ptr(source.ID),
		sourceKind:      api.Ptr(api.Access),
	})
	if err != nil {
		t.Fatalf("copy access token as pr: %v", err)
	}
	target.GithubPRTokenEncrypted = accessCopied
	token, kind, appErr = creds.pullRequestCredential(target)
	if appErr != nil || kind != pullRequestCredentialPR || string(token) != "access-token-src" {
		t.Fatalf("expected access-kind copy readable as pr credential, got token=%q kind=%q err=%v", token, kind, appErr)
	}

	// Access Token 复制 → required 路径解密
	accessToken, err := creds.resolveGitHubToken(user.ID, "", source.ID)
	if err != nil {
		t.Fatalf("copy access token: %v", err)
	}
	target.GithubOwner = "owner"
	target.GithubRepo = "repo"
	target.GithubTokenEncrypted = accessToken
	token, appErr2 := creds.requiredGitHubToken(target)
	if appErr2 != nil || string(token) != "access-token-src" {
		t.Fatalf("expected copied access token readable via required, got token=%q err=%v", token, appErr2)
	}
}
