package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// OAuthHandler OAuth 2.1 授权服务（INT-222）的协议与管理端点。
//
// 两类响应格式并存：
//   - 协议端点（/oauth/register、/oauth/token、/.well-known/*）按 RFC 7591 /
//     6749 / 8414 / 9728 返回原生 JSON，不套 Fast Ship envelope；
//   - 管理 API（/api/oauth/*）按 Fast Ship envelope 返回（前端契约）。
type OAuthHandler struct {
	oauthService *service.OAuthService
}

func NewOAuthHandler(oauthService *service.OAuthService) *OAuthHandler {
	return &OAuthHandler{oauthService: oauthService}
}

// ---- 协议端点 ----

type dynamicRegisterRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	// 其余 RFC 7591 字段（software_id、contacts 等）按规范忽略：上面的
	// struct 没有对应字段，ShouldBindJSON 自然丢弃未知键。
}

type dynamicRegisterResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func oauthError(c *gin.Context, status int, code, description string) {
	c.JSON(status, oauthErrorResponse{Error: code, ErrorDescription: description})
}

// Register POST /oauth/register：RFC 7591 动态客户端注册，无认证。
func (h *OAuthHandler) Register(c *gin.Context) {
	var req dynamicRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_client_metadata", "请求体不是合法 JSON")
		return
	}

	client, err := h.oauthService.RegisterClient(c.Request.Context(), req.ClientName, req.RedirectURIs, req.TokenEndpointAuthMethod)
	if err != nil {
		// 校验类失败统一 400；invalid_redirect_uri 仅当 redirect_uri 是
		// 触发点时使用，其余元数据问题归 invalid_client_metadata。
		if strings.Contains(errMessage(err), "redirect_uri") {
			oauthError(c, http.StatusBadRequest, "invalid_redirect_uri", errMessage(err))
			return
		}
		oauthError(c, http.StatusBadRequest, "invalid_client_metadata", errMessage(err))
		return
	}

	c.JSON(http.StatusCreated, dynamicRegisterResponse{
		ClientID:                client.ClientID,
		ClientIDIssuedAt:        client.CreatedAt.Unix(),
		ClientName:              client.ClientName,
		RedirectURIs:            service.RedirectURIsOf(client),
		TokenEndpointAuthMethod: "none",
	})
}

// errMessage 取 AppError 文案；非 AppError 按内部错误兜底（不外泄细节）。
func errMessage(err error) string {
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return "请求无效"
}

// Token POST /oauth/token：RFC 6749 token 端点（form 编码 + 可选 Basic 认证）。
func (h *OAuthHandler) Token(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		oauthError(c, http.StatusBadRequest, "invalid_request", "请求体必须是 form 编码")
		return
	}

	req := &service.OAuthTokenRequest{
		GrantType:    c.PostForm("grant_type"),
		ClientID:     firstNonEmpty(c.PostForm("client_id"), basicAuthClientID(c)),
		Code:         c.PostForm("code"),
		RedirectURI:  c.PostForm("redirect_uri"),
		CodeVerifier: c.PostForm("code_verifier"),
		RefreshToken: c.PostForm("refresh_token"),
		Scope:        c.PostForm("scope"),
	}

	var pair *service.OAuthTokenPair
	var err error
	switch req.GrantType {
	case "authorization_code":
		pair, err = h.oauthService.ExchangeAuthorizationCode(c.Request.Context(), req)
	case "refresh_token":
		pair, err = h.oauthService.RefreshToken(c.Request.Context(), req)
	case "":
		oauthError(c, http.StatusBadRequest, "invalid_request", "grant_type 为必填")
		return
	default:
		oauthError(c, http.StatusBadRequest, "unsupported_grant_type", "仅支持 authorization_code 与 refresh_token")
		return
	}
	if err != nil {
		if grant, ok := service.AsOAuthGrant(err); ok {
			oauthError(c, http.StatusBadRequest, grant.Code, grant.Description)
			return
		}
		oauthError(c, http.StatusBadRequest, "invalid_grant", "令牌请求失败")
		return
	}

	// RFC 6749 §5.1：响应必须带 no-store。
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, pair)
}

func basicAuthClientID(c *gin.Context) string {
	id, _, ok := c.Request.BasicAuth()
	if !ok {
		return ""
	}
	return id
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// WellKnownProtectedResource GET /.well-known/oauth-protected-resource（RFC 9728）。
func (h *OAuthHandler) WellKnownProtectedResource(c *gin.Context) {
	if h.oauthService.BaseURL(c.Request.Context()) == "" {
		oauthError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "尚未配置 public_url，请先在「设置 → 部署」填写对外访问地址")
		return
	}
	c.JSON(http.StatusOK, h.oauthService.ProtectedResourceMetadata(c.Request.Context()))
}

// WellKnownAuthorizationServer GET /.well-known/oauth-authorization-server（RFC 8414）。
// 挂在根路径下，issuer 根路径即可发现。
func (h *OAuthHandler) WellKnownAuthorizationServer(c *gin.Context) {
	if h.oauthService.BaseURL(c.Request.Context()) == "" {
		oauthError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "尚未配置 public_url，请先在「设置 → 部署」填写对外访问地址")
		return
	}
	c.JSON(http.StatusOK, h.oauthService.AuthorizationServerMetadata(c.Request.Context()))
}

// ---- 管理 API（/api/oauth/*，JWT 必须，Fast Ship envelope） ----

type authorizePostRequest struct {
	ClientID            string   `json:"client_id"`
	RedirectURI         string   `json:"redirect_uri"`
	CodeChallenge       string   `json:"code_challenge"`
	CodeChallengeMethod string   `json:"code_challenge_method"`
	State               string   `json:"state"`
	ProjectIDs          []string `json:"project_ids"`
	Write               bool     `json:"write"`
}

type authorizePostResponse struct {
	Redirect string `json:"redirect"`
}

// AuthorizeGet GET /api/oauth/authorize：授权页预取（客户端名 + 项目清单）。
func (h *OAuthHandler) AuthorizeGet(c *gin.Context) {
	preview, err := h.oauthService.AuthorizePreview(
		c.Request.Context(),
		middleware.GetUserID(c),
		c.Query("client_id"),
		c.Query("redirect_uri"),
		c.Query("code_challenge"),
		c.Query("code_challenge_method"),
	)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, preview)
}

// AuthorizePost POST /api/oauth/authorize：授权提交，签发授权码。
func (h *OAuthHandler) AuthorizePost(c *gin.Context) {
	var req authorizePostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	submit := &service.OAuthAuthorizeSubmit{
		ClientID:            req.ClientID,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		State:               req.State,
		ProjectIDs:          req.ProjectIDs,
		Write:               req.Write,
	}
	redirect, err := h.oauthService.AuthorizeSubmit(c.Request.Context(), middleware.GetUserID(c), submit)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, authorizePostResponse{Redirect: redirect})
}

// ListAuthorizations GET /api/oauth/authorizations：本人授权清单。
func (h *OAuthHandler) ListAuthorizations(c *gin.Context) {
	views, err := h.oauthService.ListAuthorizations(middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, views)
}

// DeleteAuthorization DELETE /api/oauth/authorizations/:id：吊销授权 + 名下
// 全部令牌（仅本人）。
func (h *OAuthHandler) DeleteAuthorization(c *gin.Context) {
	if err := h.oauthService.RevokeAuthorization(c.Request.Context(), c.Param("id"), middleware.GetUserID(c)); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}
