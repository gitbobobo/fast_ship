package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OAuth 2.1 授权服务（INT-222）：Fast Ship 作为授权服务器 + 资源服务器元数据，
// 面向 MCP 资源 {PublicURL}/mcp。协议细节与令牌族见 docs/internal/oauth-mcp.md。

const (
	// OAuthAccessTokenPrefix MCP access token 前缀（fsm_），有效期 1 小时。
	OAuthAccessTokenPrefix = "fsm_"
	// OAuthRefreshTokenPrefix OAuth refresh token 前缀（fsn_），有效期 30 天。
	OAuthRefreshTokenPrefix = "fsn_"

	oauthAccessTokenTTL  = time.Hour
	oauthRefreshTokenTTL = 30 * 24 * time.Hour
	oauthCodeTTL         = 10 * time.Minute

	// OAuthResourcePath MCP 资源路径；资源标识 = PublicURL + 此路径。
	OAuthResourcePath = "/mcp"

	// OAuthChallengeMethodS256 唯一接受的 PKCE 方法（RFC 7636 / OAuth 2.1）。
	OAuthChallengeMethodS256 = "S256"
)

// OAuth scope 编码：project:<uuid>:read 与 project:<uuid>:write（write 隐含
// read）。授权页提交选中的项目集合 + 全局读写档，服务端据此算出 scope 串。
func BuildOAuthScopes(projectIDs []string, write bool) string {
	seen := make(map[string]bool, len(projectIDs))
	scopes := make([]string, 0, len(projectIDs))
	for _, id := range projectIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if write {
			scopes = append(scopes, "project:"+id+":write")
		} else {
			scopes = append(scopes, "project:"+id+":read")
		}
	}
	sort.Strings(scopes)
	return strings.Join(scopes, " ")
}

// scopeSet 把 scope 串拆成集合。
func scopeSet(scopes string) map[string]bool {
	set := make(map[string]bool)
	for _, s := range strings.Fields(scopes) {
		set[s] = true
	}
	return set
}

// ScopeHasRead 校验 scope 集合是否含项目读权限（write 隐含 read）。
func ScopeHasRead(scopes, projectID string) bool {
	set := scopeSet(scopes)
	return set["project:"+projectID+":read"] || set["project:"+projectID+":write"]
}

// ScopeHasWrite 校验 scope 集合是否含项目写权限。
func ScopeHasWrite(scopes, projectID string) bool {
	return scopeSet(scopes)["project:"+projectID+":write"]
}

// ScopeSubset 校验 requested 是否不超出 granted。write 隐含 read：持有
// :write 的客户端请求只读 scope（RFC 6749 §6 降权）不算越界。
func ScopeSubset(granted, requested string) bool {
	grantedSet := scopeSet(granted)
	for s := range scopeSet(requested) {
		if grantedSet[s] {
			continue
		}
		// :read 未直接授予时，看同项目 :write 是否在授权内。
		if strings.HasSuffix(s, ":read") && grantedSet[strings.TrimSuffix(s, ":read")+":write"] {
			continue
		}
		return false
	}
	return true
}

// OAuthScopesSupported 发现文档里声明的 scope 形态（编码模式提示；具体取值
// 依赖用户项目集合，无法枚举）。
var OAuthScopesSupported = []string{"project:<uuid>:read", "project:<uuid>:write"}

// OAuthServiceOptions OAuthService 的可选依赖。
type OAuthServiceOptions struct {
	// PublicURL 部署公开基址来源（INT-129）。为空时发现端点返回空 base。
	PublicURL PublicURLSource
	Logger    *slog.Logger
}

type OAuthService struct {
	repo        *repository.OAuthRepository
	userRepo    *repository.UserRepository
	projectRepo *repository.ProjectRepository
	authService *AuthService
	publicURL   PublicURLSource
	logger      *slog.Logger
}

func NewOAuthService(repo *repository.OAuthRepository, userRepo *repository.UserRepository, projectRepo *repository.ProjectRepository, authService *AuthService, opts OAuthServiceOptions) *OAuthService {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &OAuthService{
		repo:        repo,
		userRepo:    userRepo,
		projectRepo: projectRepo,
		authService: authService,
		publicURL:   opts.PublicURL,
		logger:      logger,
	}
}

func (s *OAuthService) log(ctx context.Context) *slog.Logger {
	l := selflog.LoggerOr(ctx, s.logger)
	if selflog.LoggerFromContext(ctx) != nil {
		return l.With(slog.String("module", "oauth"))
	}
	return l
}

// BaseURL 部署公开基址（去尾斜杠）。为空表示尚未配置 public_url。
func (s *OAuthService) BaseURL(ctx context.Context) string {
	if s.publicURL == nil {
		return ""
	}
	return strings.TrimRight(s.publicURL.PublicURL(ctx), "/")
}

// RevokeAllForUserTx 改密联动：在改密事务里吊销该用户全部 OAuth 授权与
// access/refresh 令牌。authService.UpdatePassword 经钩子调用它。
func (s *OAuthService) RevokeAllForUserTx(tx *gorm.DB, userID string) error {
	return s.repo.RevokeAllForUserTx(tx, userID)
}

// ---- 动态客户端注册（RFC 7591） ----

// ValidateRedirectURI 校验注册用的 redirect_uri：必须是 https，或 http 的
// 回环地址（127.0.0.1/localhost/[::1]，任意端口，RFC 8252）；不得带 fragment。
func ValidateRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	default:
		return false
	}
}

// RegisterClient 动态注册一个公开客户端（无认证）：校验 redirect_uris 与
// token_endpoint_auth_method=none，生成随机 client_id。未知字段忽略。
func (s *OAuthService) RegisterClient(ctx context.Context, name string, redirectURIs []string, authMethod string) (*model.OAuthClient, error) {
	if len(redirectURIs) == 0 {
		return nil, errs.New(errs.ErrInvalidParams.Code, "redirect_uris 不能为空")
	}
	for _, uri := range redirectURIs {
		if !ValidateRedirectURI(uri) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "redirect_uri 不合法：仅接受 https 或回环地址 http（RFC 8252）")
		}
	}
	if authMethod != "" && authMethod != "none" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "token_endpoint_auth_method 仅接受 none（公开客户端）")
	}

	raw, err := GenerateApiKeyRaw()
	if err != nil {
		return nil, errs.ErrInternal
	}
	client := &model.OAuthClient{
		ClientID:     raw,
		ClientName:   strings.TrimSpace(name),
		RedirectURIs: marshalURIs(redirectURIs),
		CreatedAt:    time.Now(),
	}
	if err := s.repo.CreateClient(client); err != nil {
		return nil, errs.ErrInternal
	}

	// 只记元信息：client_id 是授权流程的公开标识可以记；redirect_uri 等不落日志。
	s.log(ctx).Info("OAuth 客户端注册",
		slog.String("client_id", client.ClientID),
		slog.String("client_name", client.ClientName),
	)
	return client, nil
}

// RedirectURIsOf 读出客户端注册的 redirect_uri 列表。
func RedirectURIsOf(client *model.OAuthClient) []string {
	var uris []string
	if err := json.Unmarshal([]byte(client.RedirectURIs), &uris); err != nil {
		return nil
	}
	return uris
}

func marshalURIs(uris []string) string {
	b, err := json.Marshal(uris)
	if err != nil {
		// 字符串列表序列化不会失败；兜底保证合法 JSON。
		return "[]"
	}
	return string(b)
}

// ---- PKCE ----

// ValidateCodeChallenge 校验 S256 challenge：合法 base64url（无填充，43-128 字符）。
func ValidateCodeChallenge(challenge string) bool {
	if len(challenge) < 43 || len(challenge) > 128 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil
}

// VerifyPKCE 常数时间比较 BASE64URL(SHA256(verifier)) 与 challenge。
func VerifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// ---- 授权端点 ----

// OAuthAuthorizePreview GET /api/oauth/authorize 的投影：当前账号（授权页
// 需展示身份，防共享电脑误授权）、客户端名 + 该用户项目清单（供授权页勾选）。
type OAuthAuthorizePreview struct {
	Username   string               `json:"username"`
	Email      string               `json:"email"`
	ClientName string               `json:"client_name"`
	Projects   []OAuthProjectOption `json:"projects"`
}

// OAuthProjectOption 授权页的项目候选项。
type OAuthProjectOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// checkAuthorizeParams 授权端点共用的入参校验：客户端存在、PKCE S256、
// redirect_uri 精确匹配。校验通过返回客户端。
func (s *OAuthService) checkAuthorizeParams(clientID, redirectURI, challenge, method string) (*model.OAuthClient, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(redirectURI) == "" || strings.TrimSpace(challenge) == "" {
		return nil, errs.ErrInvalidParams
	}
	client, err := s.repo.FindClient(clientID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.ErrOAuthClientNotFound
		}
		return nil, errs.ErrInternal
	}
	if method != OAuthChallengeMethodS256 {
		return nil, errs.ErrOAuthChallengeInvalid
	}
	if !ValidateCodeChallenge(challenge) {
		return nil, errs.ErrOAuthChallengeInvalid
	}
	found := false
	for _, uri := range RedirectURIsOf(client) {
		if uri == redirectURI {
			found = true
			break
		}
	}
	if !found {
		return nil, errs.ErrOAuthRedirectURIMismatch
	}
	return client, nil
}

// AuthorizePreview GET /api/oauth/authorize：校验参数后返回当前账号、客户端名
// 与该用户项目清单。state 由调用方透传，服务端不校验。
func (s *OAuthService) AuthorizePreview(ctx context.Context, userID, clientID, redirectURI, challenge, method string) (*OAuthAuthorizePreview, error) {
	client, err := s.checkAuthorizeParams(clientID, redirectURI, challenge, method)
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	projects, err := s.projectRepo.ListByUser(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	options := make([]OAuthProjectOption, 0, len(projects))
	for _, p := range projects {
		options = append(options, OAuthProjectOption{ID: p.ID, Name: p.Name})
	}
	return &OAuthAuthorizePreview{
		Username:   user.Username,
		Email:      user.Email,
		ClientName: client.ClientName,
		Projects:   options,
	}, nil
}

// OAuthAuthorizeSubmit POST /api/oauth/authorize 的入参。
type OAuthAuthorizeSubmit struct {
	ClientID            string   `json:"client_id"`
	RedirectURI         string   `json:"redirect_uri"`
	CodeChallenge       string   `json:"code_challenge"`
	CodeChallengeMethod string   `json:"code_challenge_method"`
	State               string   `json:"state"`
	ProjectIDs          []string `json:"project_ids"`
	Write               bool     `json:"write"`
}

// AuthorizeSubmit 授权提交：同一事务里 upsert 该 user+client 的授权（重授权
// 覆盖 scope）并写入授权码（10 分钟、单次使用），返回带回 code/state 的
// redirect 地址。事务闭包内只用 tx 句柄（单连接池规则，见 docs/internal/auth.md）。
func (s *OAuthService) AuthorizeSubmit(ctx context.Context, userID string, req *OAuthAuthorizeSubmit) (string, error) {
	client, err := s.checkAuthorizeParams(req.ClientID, req.RedirectURI, req.CodeChallenge, req.CodeChallengeMethod)
	if err != nil {
		return "", err
	}
	if len(req.ProjectIDs) == 0 {
		return "", errs.ErrOAuthProjectsEmpty
	}

	// project_ids 必须都属于该用户：权限不得超出授权集合。
	owned := make(map[string]bool, len(req.ProjectIDs))
	projects, err := s.projectRepo.ListByUser(userID)
	if err != nil {
		return "", errs.ErrInternal
	}
	for _, p := range projects {
		owned[p.ID] = true
	}
	for _, id := range req.ProjectIDs {
		if !owned[strings.TrimSpace(id)] {
			return "", errs.ErrOAuthProjectsInvalid
		}
	}

	scopes := BuildOAuthScopes(req.ProjectIDs, req.Write)
	if scopes == "" {
		return "", errs.ErrOAuthProjectsEmpty
	}

	now := time.Now()
	rawCode, err := randomToken()
	if err != nil {
		return "", errs.ErrInternal
	}

	err = s.repo.Transaction(func(tx *gorm.DB) error {
		authID, err := s.repo.UpsertAuthorizationTx(tx, &model.OAuthAuthorization{
			ID:        uuid.NewString(),
			UserID:    userID,
			ClientID:  client.ClientID,
			Scopes:    scopes,
			CreatedAt: now,
		})
		if err != nil {
			return err
		}
		return s.repo.CreateAuthorizationCodeTx(tx, &model.OAuthAuthorizationCode{
			ID:              uuid.NewString(),
			CodeHash:        HashApiKey(rawCode),
			UserID:          userID,
			ClientID:        client.ClientID,
			AuthorizationID: authID,
			RedirectURI:     req.RedirectURI,
			Scope:           scopes,
			CodeChallenge:   req.CodeChallenge,
			ExpiresAt:       now.Add(oauthCodeTTL),
			CreatedAt:       now,
		})
	})
	if err != nil {
		return "", errs.ErrInternal
	}

	// 只记元信息：授权码明文不落日志。
	s.log(ctx).Info("OAuth 授权码签发",
		slog.String("user_id", userID),
		slog.String("client_id", client.ClientID),
	)

	return buildRedirectWithCode(req.RedirectURI, rawCode, req.State), nil
}

// buildRedirectWithCode 把 code/state 拼到 redirect_uri（保留已有 query）。
func buildRedirectWithCode(redirectURI, code, state string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		// checkAuthorizeParams 已保证是注册过的合法地址；兜底不拼参数。
		return redirectURI
	}
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ---- Token 端点 ----

// ErrOAuthGrant RFC 协议错误：token/register 端点直接以 {error,
// error_description} 返回，不套 Fast Ship envelope。
type ErrOAuthGrant struct {
	Code        string // RFC 6749 §5.2 标准 error 码
	Description string
}

func (e *ErrOAuthGrant) Error() string { return e.Code + ": " + e.Description }

func oauthGrantError(code, description string) *ErrOAuthGrant {
	return &ErrOAuthGrant{Code: code, Description: description}
}

// AsOAuthGrant 判断错误是否为 OAuth 协议错误。
func AsOAuthGrant(err error) (*ErrOAuthGrant, bool) {
	if err == nil {
		return nil, false
	}
	g, ok := err.(*ErrOAuthGrant)
	return g, ok
}

// OAuthTokenRequest token 端点入参（handler 从 form / Basic 头解析后传入）。
type OAuthTokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Scope        string
}

// randomToken 32 字节 CSPRNG → base64url（无填充，43 字符）。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashOAuthToken 令牌哈希：与 API Key 同法 SHA-256（哈希的是去掉前缀的原文）。
func HashOAuthToken(raw string) string {
	return HashApiKey(raw)
}

// OAuthTokenPair token 端点响应（RFC 6749 §5.1）。
type OAuthTokenPair struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// issueTokenPairTx 事务内签发 access（fsm_，1 小时）+ refresh（fsn_，30 天），
// token_version 记签发时用户当前版本快照。闭包内只用 tx。
func issueTokenPairTx(tx *gorm.DB, repo *repository.OAuthRepository, userID, clientID, authID, scope string, tokenVersion int) (*OAuthTokenPair, error) {
	now := time.Now()

	rawAccess, err := randomToken()
	if err != nil {
		return nil, err
	}
	rawRefresh, err := randomToken()
	if err != nil {
		return nil, err
	}

	access := &model.OAuthAccessToken{
		ID:              uuid.NewString(),
		TokenHash:       HashOAuthToken(rawAccess),
		UserID:          userID,
		ClientID:        clientID,
		AuthorizationID: authID,
		Scope:           scope,
		TokenVersion:    tokenVersion,
		ExpiresAt:       now.Add(oauthAccessTokenTTL),
		CreatedAt:       now,
	}
	refresh := &model.OAuthRefreshToken{
		ID:              uuid.NewString(),
		TokenHash:       HashOAuthToken(rawRefresh),
		UserID:          userID,
		ClientID:        clientID,
		AuthorizationID: authID,
		Scope:           scope,
		TokenVersion:    tokenVersion,
		ExpiresAt:       now.Add(oauthRefreshTokenTTL),
		CreatedAt:       now,
	}

	if err := repo.CreateAccessTokenTx(tx, access); err != nil {
		return nil, err
	}
	if err := repo.CreateRefreshTokenTx(tx, refresh); err != nil {
		return nil, err
	}

	return &OAuthTokenPair{
		AccessToken:  OAuthAccessTokenPrefix + rawAccess,
		TokenType:    "Bearer",
		ExpiresIn:    int(oauthAccessTokenTTL.Seconds()),
		RefreshToken: OAuthRefreshTokenPrefix + rawRefresh,
		Scope:        scope,
	}, nil
}

// ExchangeAuthorizationCode authorization_code 换令牌：PKCE 校验（S256、常数
// 时间比较）、redirect_uri 精确比对、授权码单次使用（原子置 used 栅栏）、
// 授权未吊销；签发 access + refresh。整个兑换在同一事务内，闭包内只用 tx。
func (s *OAuthService) ExchangeAuthorizationCode(ctx context.Context, req *OAuthTokenRequest) (*OAuthTokenPair, error) {
	if req.ClientID == "" || req.Code == "" || req.RedirectURI == "" || req.CodeVerifier == "" {
		return nil, oauthGrantError("invalid_request", "code、redirect_uri、code_verifier 与 client_id 均为必填")
	}

	var pair *OAuthTokenPair
	err := s.repo.Transaction(func(tx *gorm.DB) error {
		code, err := s.repo.FindAuthorizationCodeTx(tx, HashApiKey(req.Code))
		if err != nil {
			return oauthGrantError("invalid_grant", "授权码无效")
		}
		if time.Now().After(code.ExpiresAt) {
			return oauthGrantError("invalid_grant", "授权码已过期")
		}
		if code.ClientID != req.ClientID {
			return oauthGrantError("invalid_grant", "授权码与客户端不匹配")
		}
		if code.RedirectURI != req.RedirectURI {
			return oauthGrantError("invalid_grant", "redirect_uri 与授权请求不一致")
		}
		if !VerifyPKCE(req.CodeVerifier, code.CodeChallenge) {
			return oauthGrantError("invalid_grant", "PKCE 校验失败")
		}
		// 单次使用栅栏：原子置 used_at，重放时 RowsAffected=0 直接拒绝。
		consumed, err := s.repo.ConsumeAuthorizationCodeTx(tx, code.ID)
		if err != nil {
			return err
		}
		if !consumed {
			return oauthGrantError("invalid_grant", "授权码已被使用")
		}
		// 授权被吊销即拒绝（吊销授权时令牌全量吊销，但未消费的码还躺在表里）。
		var auth model.OAuthAuthorization
		if err := tx.Where("id = ? AND revoked_at IS NULL", code.AuthorizationID).First(&auth).Error; err != nil {
			return oauthGrantError("invalid_grant", "授权已被撤销")
		}
		// 用户版本在事务内读：改密并发的全量吊销先提交时这里读到新版本，
		// 签发仍会成功但快照是新版（access 校验按当前版本比对，不受影响）。
		var user model.User
		if err := tx.Where("id = ?", code.UserID).First(&user).Error; err != nil {
			return oauthGrantError("invalid_grant", "用户不存在")
		}

		pair, err = issueTokenPairTx(tx, s.repo, user.ID, code.ClientID, auth.ID, code.Scope, user.TokenVersion)
		return err
	})
	if err != nil {
		if _, ok := AsOAuthGrant(err); ok {
			return nil, err
		}
		return nil, oauthGrantError("server_error", "服务器内部错误")
	}

	// 只记元信息：令牌与授权码明文不落日志。
	s.log(ctx).Info("OAuth 令牌签发（authorization_code）",
		slog.String("client_id", req.ClientID),
	)
	return pair, nil
}

// RefreshToken refresh_token 轮换：校验未吊销未过期、客户端匹配、scope 不
// 超出原授权、用户 token_version 未变（改密即拒）；轮换 = 条件吊销旧行
// （RowsAffected=1 才继续）+ 同事务签发新对。
func (s *OAuthService) RefreshToken(ctx context.Context, req *OAuthTokenRequest) (*OAuthTokenPair, error) {
	if req.ClientID == "" || req.RefreshToken == "" {
		return nil, oauthGrantError("invalid_request", "refresh_token 与 client_id 均为必填")
	}
	raw := strings.TrimPrefix(req.RefreshToken, OAuthRefreshTokenPrefix)

	var pair *OAuthTokenPair
	var err error
	err = s.repo.Transaction(func(tx *gorm.DB) error {
		rt, err := s.repo.FindRefreshTokenByHashTx(tx, HashOAuthToken(raw))
		if err != nil {
			return oauthGrantError("invalid_grant", "refresh token 无效或已过期")
		}
		if rt.ClientID != req.ClientID {
			return oauthGrantError("invalid_grant", "refresh token 与客户端不匹配")
		}
		if req.Scope != "" && !ScopeSubset(rt.Scope, req.Scope) {
			return oauthGrantError("invalid_scope", "scope 超出原授权范围")
		}

		// 改密联动：版本已推进的 refresh 直接失败（吊销也由改密事务完成，
		// 这里是双保险，覆盖吊销扫描尚未触达的窗口）。
		var user model.User
		if err := tx.Where("id = ?", rt.UserID).First(&user).Error; err != nil {
			return oauthGrantError("invalid_grant", "用户不存在")
		}
		if user.TokenVersion != rt.TokenVersion {
			return oauthGrantError("invalid_grant", "用户凭证已更新，请重新授权")
		}

		rawAccess, err := randomToken()
		if err != nil {
			return err
		}
		rawRefresh, err := randomToken()
		if err != nil {
			return err
		}

		// RFC 6749 §6：请求带 scope 时按请求收窄新令牌（已验 subset），
		// 不再沿用旧授权的全部 scope。
		granted := rt.Scope
		if req.Scope != "" {
			granted = req.Scope
		}

		nextAccess := &model.OAuthAccessToken{
			ID:              uuid.NewString(),
			TokenHash:       HashOAuthToken(rawAccess),
			UserID:          rt.UserID,
			ClientID:        rt.ClientID,
			AuthorizationID: rt.AuthorizationID,
			Scope:           granted,
			TokenVersion:    user.TokenVersion,
			ExpiresAt:       time.Now().Add(oauthAccessTokenTTL),
			CreatedAt:       time.Now(),
		}
		nextRefresh := &model.OAuthRefreshToken{
			ID:              uuid.NewString(),
			TokenHash:       HashOAuthToken(rawRefresh),
			UserID:          rt.UserID,
			ClientID:        rt.ClientID,
			AuthorizationID: rt.AuthorizationID,
			Scope:           granted,
			TokenVersion:    user.TokenVersion,
			ExpiresAt:       time.Now().Add(oauthRefreshTokenTTL),
			CreatedAt:       time.Now(),
		}

		// 轮换栅栏：旧行已被吊销（改密/撤销授权/并发轮换）则整体中止。
		rotated, err := s.repo.RotateRefreshTokenTx(tx, rt.ID, nextAccess, nextRefresh)
		if err != nil {
			return err
		}
		if !rotated {
			return oauthGrantError("invalid_grant", "refresh token 已失效")
		}

		// 明文只在此刻存在于内存，库里只有哈希。
		pair = &OAuthTokenPair{
			AccessToken:  OAuthAccessTokenPrefix + rawAccess,
			TokenType:    "Bearer",
			ExpiresIn:    int(oauthAccessTokenTTL.Seconds()),
			RefreshToken: OAuthRefreshTokenPrefix + rawRefresh,
			Scope:        granted,
		}
		return nil
	})
	if err != nil {
		if _, ok := AsOAuthGrant(err); ok {
			return nil, err
		}
		return nil, oauthGrantError("server_error", "服务器内部错误")
	}

	s.log(ctx).Info("OAuth 令牌轮换（refresh_token）",
		slog.String("client_id", req.ClientID),
	)
	return pair, nil
}

// ValidateAccessToken 资源服务器访问令牌校验：哈希命中、未吊销未过期
// （FindAccessTokenByHash 已过滤）、用户 token_version 仍是签发快照。
// 供后续 /mcp 中间件使用。
func (s *OAuthService) ValidateAccessToken(ctx context.Context, rawToken string) (*OAuthAccessTokenInfo, error) {
	raw := strings.TrimPrefix(rawToken, OAuthAccessTokenPrefix)
	token, err := s.repo.FindAccessTokenByHash(HashOAuthToken(raw))
	if err != nil {
		return nil, errs.ErrTokenInvalid
	}
	if s.authService != nil {
		current, err := s.authService.IsTokenVersionCurrent(token.UserID, int64(token.TokenVersion))
		if err != nil || !current {
			return nil, errs.ErrTokenInvalid
		}
	}
	return &OAuthAccessTokenInfo{
		UserID:          token.UserID,
		ClientID:        token.ClientID,
		AuthorizationID: token.AuthorizationID,
		Scopes:          token.Scope,
		TokenVersion:    token.TokenVersion,
		ExpiresAt:       token.ExpiresAt,
	}, nil
}

// OAuthAccessTokenInfo ValidateAccessToken 的返回投影。
type OAuthAccessTokenInfo struct {
	UserID          string
	ClientID        string
	AuthorizationID string
	Scopes          string
	TokenVersion    int
	ExpiresAt       time.Time
}

// ---- 授权管理 API ----

// OAuthAuthorizationView GET /api/oauth/authorizations 的条目投影。
type OAuthAuthorizationView struct {
	ID         string `json:"id"`
	ClientName string `json:"client_name"`
	Scopes     string `json:"scopes"`
	CreatedAt  string `json:"created_at"`
}

// ListAuthorizations 本人的未吊销授权清单（client 名、scope、created_at、id）。
func (s *OAuthService) ListAuthorizations(userID string) ([]OAuthAuthorizationView, error) {
	rows, err := s.repo.ListAuthorizations(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	views := make([]OAuthAuthorizationView, 0, len(rows))
	for _, row := range rows {
		views = append(views, OAuthAuthorizationView{
			ID:         row.ID,
			ClientName: row.ClientName,
			Scopes:     row.Scopes,
			CreatedAt:  row.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return views, nil
}

// RevokeAuthorization 吊销本人的授权 + 该授权下全部令牌（即时生效）。
// 非本人或不存在的 id 返回 ErrOAuthAuthorizationNotFound。
func (s *OAuthService) RevokeAuthorization(ctx context.Context, id, userID string) error {
	hit, err := s.repo.RevokeAuthorization(id, userID)
	if err != nil {
		return errs.ErrInternal
	}
	if !hit {
		return errs.ErrOAuthAuthorizationNotFound
	}
	s.log(ctx).Info("OAuth 授权吊销",
		slog.String("user_id", userID),
		slog.String("authorization_id", id),
	)
	return nil
}

// ---- 发现端点 ----

// OAuthProtectedResourceMetadata RFC 9728 资源服务器元数据。
type OAuthProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// OAuthAuthorizationServerMetadata RFC 8414 授权服务器元数据。
type OAuthAuthorizationServerMetadata struct {
	Issuer                   string   `json:"issuer"`
	AuthorizationEndpoint    string   `json:"authorization_endpoint"`
	TokenEndpoint            string   `json:"token_endpoint"`
	RegistrationEndpoint     string   `json:"registration_endpoint"`
	ResponseTypes            []string `json:"response_types"`
	GrantTypes               []string `json:"grant_types"`
	CodeChallengeMethods     []string `json:"code_challenge_methods"`
	ScopesSupported          []string `json:"scopes_supported"`
	TokenEndpointAuthMethods []string `json:"token_endpoint_auth_methods"`
}

// ProtectedResourceMetadata GET /.well-known/oauth-protected-resource。
func (s *OAuthService) ProtectedResourceMetadata(ctx context.Context) *OAuthProtectedResourceMetadata {
	base := s.BaseURL(ctx)
	return &OAuthProtectedResourceMetadata{
		Resource:             base + OAuthResourcePath,
		AuthorizationServers: []string{base},
		ScopesSupported:      OAuthScopesSupported,
	}
}

// AuthorizationServerMetadata GET /.well-known/oauth-authorization-server。
// issuer 根路径即可发现：该端点挂在根路径下，不在 /api 下。
func (s *OAuthService) AuthorizationServerMetadata(ctx context.Context) *OAuthAuthorizationServerMetadata {
	base := s.BaseURL(ctx)
	return &OAuthAuthorizationServerMetadata{
		Issuer:                   base,
		AuthorizationEndpoint:    base + "/oauth/authorize",
		TokenEndpoint:            base + "/oauth/token",
		RegistrationEndpoint:     base + "/oauth/register",
		ResponseTypes:            []string{"code"},
		GrantTypes:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethods:     []string{OAuthChallengeMethodS256},
		ScopesSupported:          OAuthScopesSupported,
		TokenEndpointAuthMethods: []string{"none"},
	}
}
