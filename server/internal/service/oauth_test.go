package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// OAuth 2.1 授权服务（INT-222）service 层测试：PKCE、授权码单次使用与过期、
// refresh 轮换栅栏与改密拒刷、scope 编码、吊销即时生效、改密联动吊销。

type oauthTestServices struct {
	db          *gorm.DB
	cfg         *config.Config
	authService *AuthService
	oauth       *OAuthService
	oauthRepo   *repository.OAuthRepository
}

func setupOAuthTestServices(t *testing.T) *oauthTestServices {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	cleanupTestDB(t, db)

	if err := db.AutoMigrate(
		&model.User{},
		&model.JWTBlacklist{},
		&model.RefreshToken{},
		&model.Project{},
		&model.OAuthClient{},
		&model.OAuthAuthorization{},
		&model.OAuthAuthorizationCode{},
		&model.OAuthAccessToken{},
		&model.OAuthRefreshToken{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:             "test-secret",
			ExpireHours:        24,
			RefreshExpireHours: 72,
		},
	}

	userRepo := repository.NewUserRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)
	oauthRepo := repository.NewOAuthRepository(db)
	authService := NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg, nil)
	oauthService := NewOAuthService(oauthRepo, userRepo, repository.NewProjectRepository(db), authService, OAuthServiceOptions{})

	return &oauthTestServices{
		db:          db,
		cfg:         cfg,
		authService: authService,
		oauth:       oauthService,
		oauthRepo:   oauthRepo,
	}
}

func (s *oauthTestServices) createClient(t *testing.T) *model.OAuthClient {
	t.Helper()
	client, err := s.oauth.RegisterClient(context.Background(), "测试客户端", []string{"https://client.example.com/cb", "http://127.0.0.1:8931/cb"}, "")
	if err != nil {
		t.Fatalf("register client: %v", err)
	}
	return client
}

// pkcePair 生成合法的 verifier/challenge 对（S256）。
func pkcePair() (verifier, challenge string) {
	verifier = strings.Repeat("k", 64)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *oauthTestServices) authorize(t *testing.T, userID string, client *model.OAuthClient, projectID string, write bool, verifier string) string {
	t.Helper()
	_, challenge := pkcePairFor(verifier)
	redirect, err := s.oauth.AuthorizeSubmit(context.Background(), userID, &OAuthAuthorizeSubmit{
		ClientID:            client.ClientID,
		RedirectURI:         "https://client.example.com/cb",
		CodeChallenge:       challenge,
		CodeChallengeMethod: OAuthChallengeMethodS256,
		State:               "st123",
		ProjectIDs:          []string{projectID},
		Write:               write,
	})
	if err != nil {
		t.Fatalf("authorize submit: %v", err)
	}
	// 从 redirect 里拆出 code。
	code := redirect
	if i := strings.Index(redirect, "code="); i >= 0 {
		code = redirect[i+len("code="):]
		if j := strings.Index(code, "&"); j >= 0 {
			code = code[:j]
		}
	}
	return code
}

func pkcePairFor(verifier string) (string, string) {
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// createOAuthTestProject 建一个不带 GitHub token 引用的项目（避免测试库建
// user_github_tokens 表）。
func createOAuthTestProject(t *testing.T, db *gorm.DB, userID string) *model.Project {
	t.Helper()
	return createTestProject(t, db, userID, func(p *model.Project) {
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = ""
	})
}

func (s *oauthTestServices) exchange(t *testing.T, clientID, code, redirectURI, verifier string) (*OAuthTokenPair, error) {
	t.Helper()
	return s.oauth.ExchangeAuthorizationCode(context.Background(), &OAuthTokenRequest{
		GrantType:    "authorization_code",
		ClientID:     clientID,
		Code:         code,
		RedirectURI:  redirectURI,
		CodeVerifier: verifier,
	})
}

func TestOAuthScopeEncoding(t *testing.T) {
	// write 只落 write 条目（write 隐含 read）；排序保证幂等。
	got := BuildOAuthScopes([]string{"b-id", "a-id", "a-id", ""}, true)
	if got != "project:a-id:write project:b-id:write" {
		t.Fatalf("unexpected write scopes: %q", got)
	}
	readOnly := BuildOAuthScopes([]string{"a-id"}, false)
	if readOnly != "project:a-id:read" {
		t.Fatalf("unexpected read scopes: %q", readOnly)
	}
	// write 隐含 read；read 不含 write。
	if !ScopeHasRead("project:a-id:write", "a-id") || ScopeHasWrite("project:a-id:read", "a-id") {
		t.Fatalf("write implies read broken")
	}
	if ScopeHasRead("project:b-id:read", "a-id") {
		t.Fatalf("scope 越权：其他项目不应命中")
	}
	if !ScopeSubset("p1:read p2:read", "p1:read") || ScopeSubset("p1:read", "p1:read p2:read") {
		t.Fatalf("scope subset 判断错误")
	}
}

func TestOAuthValidateRedirectURI(t *testing.T) {
	valid := []string{
		"https://client.example.com/cb",
		"https://client.example.com:8443/cb",
		"http://127.0.0.1:8080/callback",
		"http://localhost:3000/cb",
		"http://[::1]:9000/cb",
	}
	for _, uri := range valid {
		if !ValidateRedirectURI(uri) {
			t.Fatalf("应放行 %s", uri)
		}
	}
	invalid := []string{
		"http://client.example.com/cb",       // 非回环 http
		"https://client.example.com/cb#frag", // fragment
		"ftp://client.example.com",
		"https:///no-host",
		"not a url",
	}
	for _, uri := range invalid {
		if ValidateRedirectURI(uri) {
			t.Fatalf("应拒绝 %s", uri)
		}
	}
}

func TestOAuthPKCEVerify(t *testing.T) {
	verifier, challenge := pkcePair()
	if !VerifyPKCE(verifier, challenge) {
		t.Fatalf("正确 verifier 应通过")
	}
	if VerifyPKCE(strings.Repeat("x", 64), challenge) {
		t.Fatalf("错误 verifier 应拒绝")
	}
	if ValidateCodeChallenge("too-short") || ValidateCodeChallenge(strings.Repeat("*", 64)) {
		t.Fatalf("非法 base64url / 过短 challenge 应拒绝")
	}
	if !ValidateCodeChallenge(challenge) {
		t.Fatalf("合法 challenge 应通过")
	}
}

// 完整授权码流程：PKCE 正确 verifier 通过；错误 verifier 拒绝。
func TestOAuthAuthorizationCodePKCE(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)

	verifier, challenge := pkcePair()
	redirect, err := s.oauth.AuthorizeSubmit(context.Background(), user.ID, &OAuthAuthorizeSubmit{
		ClientID:            client.ClientID,
		RedirectURI:         "https://client.example.com/cb",
		CodeChallenge:       challenge,
		CodeChallengeMethod: OAuthChallengeMethodS256,
		ProjectIDs:          []string{project.ID},
		Write:               true,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !strings.Contains(redirect, "code=") {
		t.Fatalf("redirect 应携带 code: %s", redirect)
	}
	code := redirect[strings.Index(redirect, "code=")+len("code="):]

	// 错误 verifier：拒绝且码不被消费（修好后仍可用）。
	if _, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", strings.Repeat("x", 64)); err == nil {
		t.Fatalf("错误 verifier 应拒绝")
	}
	pair, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", verifier)
	if err != nil {
		t.Fatalf("正确 verifier 应通过: %v", err)
	}
	if !strings.HasPrefix(pair.AccessToken, "fsm_") || !strings.HasPrefix(pair.RefreshToken, "fsn_") {
		t.Fatalf("令牌前缀错误: %s", pair.AccessToken[:4])
	}
	if pair.ExpiresIn != 3600 || pair.TokenType != "Bearer" || pair.Scope != "project:"+project.ID+":write" {
		t.Fatalf("token 响应形状错误: %+v", pair)
	}

	info, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken)
	if err != nil {
		t.Fatalf("validate access: %v", err)
	}
	if info.UserID != user.ID || info.ClientID != client.ClientID || !ScopeHasWrite(info.Scopes, project.ID) {
		t.Fatalf("access info 错误: %+v", info)
	}
}

// 授权码单次使用与过期。
func TestOAuthCodeSingleUseAndExpiry(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)
	verifier, challenge := pkcePair()

	code := s.authorizeWith(t, user.ID, client, "https://client.example.com/cb", challenge, []string{project.ID}, false)

	// 第一次兑换成功。
	if _, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", verifier); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	// 重放：invalid_grant。
	_, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", verifier)
	if err == nil {
		t.Fatalf("重放应拒绝")
	}
	if grant, ok := AsOAuthGrant(err); !ok || grant.Code != "invalid_grant" {
		t.Fatalf("重放应 invalid_grant，得到 %v", err)
	}

	// 过期：直接造一条已过期码。
	rawExpired := strings.Repeat("e", 43)
	expired := &model.OAuthAuthorizationCode{
		ID:              uuid.NewString(),
		CodeHash:        HashApiKey(rawExpired),
		UserID:          user.ID,
		ClientID:        client.ClientID,
		AuthorizationID: "auth-x",
		RedirectURI:     "https://client.example.com/cb",
		Scope:           "project:proj-1:read",
		CodeChallenge:   challenge,
		ExpiresAt:       time.Now().Add(-time.Minute),
		CreatedAt:       time.Now(),
	}
	if err := s.db.Create(expired).Error; err != nil {
		t.Fatalf("seed expired code: %v", err)
	}
	if _, err := s.exchange(t, client.ClientID, rawExpired, "https://client.example.com/cb", verifier); err == nil {
		t.Fatalf("过期码应拒绝")
	}
}

func (s *oauthTestServices) authorizeWith(t *testing.T, userID string, client *model.OAuthClient, redirectURI, challenge string, projects []string, write bool) string {
	t.Helper()
	redirect, err := s.oauth.AuthorizeSubmit(context.Background(), userID, &OAuthAuthorizeSubmit{
		ClientID:            client.ClientID,
		RedirectURI:         redirectURI,
		CodeChallenge:       challenge,
		CodeChallengeMethod: OAuthChallengeMethodS256,
		ProjectIDs:          projects,
		Write:               write,
	})
	if err != nil {
		t.Fatalf("authorize submit: %v", err)
	}
	code := redirect[strings.Index(redirect, "code=")+len("code="):]
	if i := strings.Index(code, "&"); i >= 0 {
		code = code[:i]
	}
	return code
}

// refresh 轮换：旧 token 一次性；再刷被并发轮换抢先后拒绝（栅栏）。
func TestOAuthRefreshRotationFence(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)
	verifier, _ := pkcePair()
	code := s.authorize(t, user.ID, client, project.ID, false, verifier)
	pair, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	refresh := func() (*OAuthTokenPair, error) {
		return s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
			GrantType:    "refresh_token",
			ClientID:     client.ClientID,
			RefreshToken: pair.RefreshToken,
		})
	}

	next, err := refresh()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !strings.HasPrefix(next.AccessToken, "fsm_") || !strings.HasPrefix(next.RefreshToken, "fsn_") {
		t.Fatalf("轮换令牌前缀错误")
	}

	// 旧 refresh 已被轮换吊销：重放拒绝。
	if _, err := refresh(); err == nil {
		t.Fatalf("旧 refresh 重放应拒绝")
	}

	// 错误 client_id 拒绝。
	if _, err := s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
		GrantType: "refresh_token", ClientID: "wrong-client", RefreshToken: next.RefreshToken,
	}); err == nil {
		t.Fatalf("client 不匹配应拒绝")
	}
}

func appErrCode(t *testing.T, err error) int {
	t.Helper()
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	t.Fatalf("非 AppError：%v", err)
	return 0
}

// 改密后：access 校验失败（token_version 快照比对），refresh 直接失败。
func TestOAuthPasswordChangeInvalidatesTokens(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)
	verifier, _ := pkcePair()
	code := s.authorize(t, user.ID, client, project.ID, false, verifier)
	pair, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	if _, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken); err != nil {
		t.Fatalf("改密前 access 应有效: %v", err)
	}

	// 预置真实 bcrypt 哈希，让改密走通（含 OAuth 联动吊销）。
	hashed, err := bcrypt.GenerateFromPassword([]byte("old-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := s.db.Model(&model.User{}).Where("id = ?", user.ID).Update("password_hash", string(hashed)).Error; err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	if _, err := s.authService.UpdatePassword(context.Background(), user.ID, &UpdatePasswordRequest{OldPassword: "old-pass", NewPassword: "new-pass"}); err != nil {
		t.Fatalf("update password: %v", err)
	}

	if _, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken); err == nil {
		t.Fatalf("改密后 access 应立即失效")
	}
	if _, err := s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
		GrantType: "refresh_token", ClientID: client.ClientID, RefreshToken: pair.RefreshToken,
	}); err == nil {
		t.Fatalf("改密后 refresh 应失败")
	}
}

// 授权吊销即时生效：吊销授权后 access 校验失败、refresh 失败、码不可兑换。
func TestOAuthRevokeAuthorizationImmediate(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)
	verifier, _ := pkcePair()
	code := s.authorize(t, user.ID, client, project.ID, false, verifier)

	authRows, err := s.oauth.ListAuthorizations(user.ID)
	if err != nil || len(authRows) != 1 {
		t.Fatalf("list authorizations: %v %v", authRows, err)
	}

	pair, err := s.exchange(t, client.ClientID, code, "https://client.example.com/cb", verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	// 未吊销：校验通过。
	if _, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken); err != nil {
		t.Fatalf("吊销前 access 应有效: %v", err)
	}

	// 非本人 id：40437。
	if err := s.oauth.RevokeAuthorization(context.Background(), authRows[0].ID, "someone-else"); err == nil || appErrCode(t, err) != errs.ErrOAuthAuthorizationNotFound.Code {
		t.Fatalf("非本人吊销应 40437，得到 %v", err)
	}

	if err := s.oauth.RevokeAuthorization(context.Background(), authRows[0].ID, user.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken); err == nil {
		t.Fatalf("吊销后 access 应立即失效")
	}
	if _, err := s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
		GrantType: "refresh_token", ClientID: client.ClientID, RefreshToken: pair.RefreshToken,
	}); err == nil {
		t.Fatalf("吊销后 refresh 应失败")
	}
	if views, _ := s.oauth.ListAuthorizations(user.ID); len(views) != 0 {
		t.Fatalf("吊销后清单应为空")
	}
}

// 授权提交：project_ids 必须属于该用户；重授权覆盖 scope（read → write）。
func TestOAuthAuthorizeProjectOwnershipAndRescope(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	client := s.createClient(t)
	_, challenge := pkcePair()

	// 未选项目：拒绝。
	if _, err := s.oauth.AuthorizeSubmit(context.Background(), user.ID, &OAuthAuthorizeSubmit{
		ClientID: client.ClientID, RedirectURI: "https://client.example.com/cb",
		CodeChallenge: challenge, CodeChallengeMethod: OAuthChallengeMethodS256,
	}); err == nil {
		t.Fatalf("空 project_ids 应拒绝")
	}

	// 不属于该用户的项目：拒绝。
	if _, err := s.oauth.AuthorizeSubmit(context.Background(), user.ID, &OAuthAuthorizeSubmit{
		ClientID: client.ClientID, RedirectURI: "https://client.example.com/cb",
		CodeChallenge: challenge, CodeChallengeMethod: OAuthChallengeMethodS256,
		ProjectIDs: []string{"not-mine"},
	}); err == nil {
		t.Fatalf("非本人项目应拒绝")
	}

	project := createOAuthTestProject(t, s.db, user.ID)

	// 先 read 后 write：同一授权行覆盖 scope。
	for i, write := range []bool{false, true} {
		want := "project:" + project.ID + ":read"
		if write {
			want = "project:" + project.ID + ":write"
		}
		redirect, err := s.oauth.AuthorizeSubmit(context.Background(), user.ID, &OAuthAuthorizeSubmit{
			ClientID: client.ClientID, RedirectURI: "https://client.example.com/cb",
			CodeChallenge: challenge, CodeChallengeMethod: OAuthChallengeMethodS256,
			ProjectIDs: []string{project.ID}, Write: write, State: "xyz",
		})
		if err != nil {
			t.Fatalf("authorize %d: %v", i, err)
		}
		if !strings.Contains(redirect, "state=xyz") {
			t.Fatalf("state 应原样带回: %s", redirect)
		}
		rows, err := s.oauth.ListAuthorizations(user.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("重授权应仍是一行: %v %v", rows, err)
		}
		if rows[0].Scopes != want {
			t.Fatalf("scope 应覆盖为 %s，得到 %s", want, rows[0].Scopes)
		}
	}
}

// GET 授权预取：redirect_uri 精确匹配与 PKCE 方法校验。
func TestOAuthAuthorizePreviewValidation(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	client := s.createClient(t)
	_, challenge := pkcePair()

	cases := []struct {
		name       string
		redirect   string
		method     string
		challenge  string
		wantErrOne int // 期望的错误码之一取首位，0 = 成功
	}{
		{"ok", "https://client.example.com/cb", "S256", challenge, 0},
		{"redirect 不匹配", "https://evil.example.com/cb", "S256", challenge, errs.ErrOAuthRedirectURIMismatch.Code},
		{"method 非 S256", "https://client.example.com/cb", "plain", challenge, errs.ErrOAuthChallengeInvalid.Code},
		{"challenge 非法", "https://client.example.com/cb", "S256", "short", errs.ErrOAuthChallengeInvalid.Code},
	}
	for _, tc := range cases {
		_, err := s.oauth.AuthorizePreview(context.Background(), user.ID, client.ClientID, tc.redirect, tc.challenge, tc.method)
		if tc.wantErrOne == 0 {
			if err != nil {
				t.Fatalf("%s: 应成功，得到 %v", tc.name, err)
			}
			continue
		}
		if err == nil || appErrCode(t, err) != tc.wantErrOne {
			t.Fatalf("%s: 期望错误码 %d，得到 %v", tc.name, tc.wantErrOne, err)
		}
	}

	// 未知客户端 id。
	if _, err := s.oauth.AuthorizePreview(context.Background(), user.ID, "nope", "https://client.example.com/cb", challenge, "S256"); err == nil {
		t.Fatalf("未知客户端应拒绝")
	}
}

// 缩权重授权：旧 access/refresh 即时吊销、未消费旧码作废、created_at 刷新为
// 最近一次授权时间（审查 P2：权限不得超出本次授权档）。
func TestOAuthReauthorizeNarrowingRevokesTokens(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)

	// 第一次：write 授权并换出令牌。
	verifier, _ := pkcePair()
	code := s.authorize(t, user.ID, client, project.ID, true, verifier)
	pair, err := s.oauth.ExchangeAuthorizationCode(context.Background(), &OAuthTokenRequest{
		ClientID: client.ClientID, Code: code,
		RedirectURI: "https://client.example.com/cb", CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if _, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken); err != nil {
		t.Fatalf("旧 access 应有效: %v", err)
	}

	// 第二次：缩权为只读重授权（先睡 2ms 保证 created_at 可观测地前进）。
	time.Sleep(2 * time.Millisecond)
	verifier2, _ := pkcePair()
	s.authorize(t, user.ID, client, project.ID, false, verifier2)

	if _, err := s.oauth.ValidateAccessToken(context.Background(), pair.AccessToken); err == nil {
		t.Fatalf("缩权重授权后旧 access 应失效")
	}
	if _, err := s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
		ClientID: client.ClientID, RefreshToken: pair.RefreshToken,
	}); err == nil {
		t.Fatalf("缩权重授权后旧 refresh 应失效")
	}

	// 未消费旧码：第一次授权流程里没有多余旧码，再造一条旧码验证作废。
	verifier3, _ := pkcePair()
	code3 := s.authorize(t, user.ID, client, project.ID, false, verifier3)
	verifier4, _ := pkcePair()
	s.authorize(t, user.ID, client, project.ID, false, verifier4) // scope 未变不吊销，再缩一次制造变化
	verifier5, _ := pkcePair()
	s.authorize(t, user.ID, client, project.ID, true, verifier5)
	_, err = s.oauth.ExchangeAuthorizationCode(context.Background(), &OAuthTokenRequest{
		ClientID: client.ClientID, Code: code3,
		RedirectURI: "https://client.example.com/cb", CodeVerifier: verifier3,
	})
	if err == nil {
		t.Fatalf("scope 变化后未消费旧码应作废")
	}

	rows, err := s.oauth.ListAuthorizations(user.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("重授权应仍是一行: %v %v", rows, err)
	}
	if rows[0].Scopes != "project:"+project.ID+":write" {
		t.Fatalf("scope 应为最新授权档: %s", rows[0].Scopes)
	}
}

// refresh 按请求收窄（RFC 6749 §6）：请求只读 scope 成功、新令牌持窄 scope；
// write 隐含 read 使 write 客户端请求只读不算越界。
func TestOAuthRefreshScopeNarrowing(t *testing.T) {
	s := setupOAuthTestServices(t)
	user := createTestUser(t, s.db, uuid.NewString())
	project := createOAuthTestProject(t, s.db, user.ID)
	client := s.createClient(t)

	verifier, _ := pkcePair()
	code := s.authorize(t, user.ID, client, project.ID, true, verifier)
	pair, err := s.oauth.ExchangeAuthorizationCode(context.Background(), &OAuthTokenRequest{
		ClientID: client.ClientID, Code: code,
		RedirectURI: "https://client.example.com/cb", CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	// write 客户端请求只读：子集判定应放行（write 隐含 read）。
	narrow, err := s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
		ClientID: client.ClientID, RefreshToken: pair.RefreshToken,
		Scope: "project:" + project.ID + ":read",
	})
	if err != nil {
		t.Fatalf("降权刷新应成功: %v", err)
	}
	if narrow.Scope != "project:"+project.ID+":read" {
		t.Fatalf("新令牌应持窄 scope，得到 %s", narrow.Scope)
	}
	info, err := s.oauth.ValidateAccessToken(context.Background(), narrow.AccessToken)
	if err != nil {
		t.Fatalf("窄 access 应有效: %v", err)
	}
	if ScopeHasWrite(info.Scopes, project.ID) {
		t.Fatalf("窄 access 不应再有写权限")
	}
	if !ScopeHasRead(info.Scopes, project.ID) {
		t.Fatalf("窄 access 应保留读权限")
	}

	// 请求超出授权的 scope：拒绝。
	if _, err := s.oauth.RefreshToken(context.Background(), &OAuthTokenRequest{
		ClientID: client.ClientID, RefreshToken: narrow.RefreshToken,
		Scope: "project:" + project.ID + ":write",
	}); err == nil {
		t.Fatalf("请求写权限应被拒绝")
	}
}
