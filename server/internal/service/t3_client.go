package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// T3 环境协议客户端（HTTP 侧）。
//
// 协议事实全部核对自 T3 源码（/Users/godbobo/work/tools/t3code，2026-10）：
//   - 配对码换 Bearer：POST /oauth/token，form-urlencoded
//     （packages/contracts/src/auth.ts AuthTokenExchangeRequest / AuthAccessTokenResult）
//   - 会话探测：GET /api/auth/session，带 Bearer（environmentHttp.ts auth.session）
//   - 完整线程快照：GET /api/orchestration/threads/:threadId，
//     须带 x-t3-orchestration-protocol: 2（environmentHttp.ts orchestration.threadSnapshot；
//     WS 侧同名读取是分页受限窗口，只有 HTTP 侧返回完整历史，故走 HTTP）
//
// 兑换请求不携带 scope（auth.ts 中 scope 为 optional）：T3 服务端按配对链接的
// 授权授予完整 scope（标准客户端含 orchestration:read + orchestration:operate，
// EnvironmentAuth.exchangeBootstrapCredentialForAccessToken 在 requestedScopes
// 缺省时返回整份 grant.scopes）。授予的 scope 原样落库；会话能力（能否操作）
// 由精确 token 的 scope 列判定，见 t3HasOperateScope。INT-82 时代的只读旧环境
// 不自动升级，重新配对后自然带 operate。

const (
	t3ScopeOrchestrationRead    = "orchestration:read"
	t3ScopeOrchestrationOperate = "orchestration:operate"

	t3GrantTypeTokenExchange   = "urn:ietf:params:oauth:grant-type:token-exchange"
	t3TokenTypeBootstrap       = "urn:t3:params:oauth:token-type:environment-bootstrap"
	t3TokenTypeAccessToken     = "urn:ietf:params:oauth:token-type:access_token"
	t3OrchestrationProtocolHdr = "x-t3-orchestration-protocol"
	t3OrchestrationProtocolV   = "2"

	// 上游 HTTP 调用预算：配对/探测/线程详情各自独立计时。
	t3HTTPTimeout = 15 * time.Second

	// 上游响应体大小上限：线程投影可能很大，但不能无限读。
	t3HTTPMaxBodyBytes = 64 << 20 // 64MB
)

// t3BearerCredentials 是一次成功配对产出的会话凭证。
type t3BearerCredentials struct {
	AccessToken string
	ExpiresAt   time.Time
	Scope       string // 服务端实际授予的 scope 串
}

type t3HTTPClient struct {
	client       *http.Client
	maxBodyBytes int64
}

func newT3HTTPClient() *t3HTTPClient {
	return &t3HTTPClient{
		client: &http.Client{
			Timeout: t3HTTPTimeout,
			// 不跟随重定向：配对地址是用户输入的远端，
			// 跟随跳转可能把 Bearer 带到非预期主机（凭证泄漏面）。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxBodyBytes: t3HTTPMaxBodyBytes,
	}
}

// exchangeBearer 用一次性配对码换取 Bearer 会话（不申请 scope，授予值由
// 配对链接的授权决定）。任何错误都返回带上下文的 error，由调用方映射成
// 40005/50202。
func (c *t3HTTPClient) exchangeBearer(ctx context.Context, httpBaseURL, pairingCredential string) (*t3BearerCredentials, error) {
	form := url.Values{}
	form.Set("grant_type", t3GrantTypeTokenExchange)
	form.Set("subject_token", pairingCredential)
	form.Set("subject_token_type", t3TokenTypeBootstrap)
	form.Set("requested_token_type", t3TokenTypeAccessToken)
	form.Set("client_label", "Fast Ship")
	form.Set("client_device_type", "bot")

	endpoint := strings.TrimSuffix(httpBaseURL, "/") + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("构造 token 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	respBody, status, err := c.doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("token 请求失败: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("token 兑换被拒绝（HTTP %d）: %s", status, t3ErrorMessage(respBody))
	}

	var tokenResp struct {
		AccessToken     string `json:"access_token"`
		IssuedTokenType string `json:"issued_token_type"`
		TokenType       string `json:"token_type"`
		ExpiresIn       any    `json:"expires_in"`
		Scope           string `json:"scope"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("token 响应解析失败: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token 响应缺少 access_token")
	}
	if !strings.EqualFold(tokenResp.TokenType, "Bearer") {
		return nil, fmt.Errorf("token 响应类型 %q 不是 Bearer，拒绝使用", tokenResp.TokenType)
	}

	expiresIn := t3NumberValue(tokenResp.ExpiresIn)
	creds := &t3BearerCredentials{
		AccessToken: tokenResp.AccessToken,
		Scope:       tokenResp.Scope,
	}
	if expiresIn > 0 {
		creds.ExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	} else {
		// 服务端未给有效期时按已过期记账：读取前不再尝试续期，
		// 探测返回 repair 引导用户重新配对。
		creds.ExpiresAt = time.Now()
	}
	return creds, nil
}

// probeSession 探测环境的可达性与凭证有效性。
// 状态判定（单请求同时回答两个问题）：
//   - 200 且 authenticated=true → ok
//   - 401/403（服务端应答了但拒绝凭证）→ repair
//   - 其余状态码/网络错误/超时 → unreachable
func (c *t3HTTPClient) probeSession(ctx context.Context, httpBaseURL, bearerToken string) (T3EnvironmentStatus, string) {
	endpoint := strings.TrimSuffix(httpBaseURL, "/") + "/api/auth/session"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return T3EnvironmentStatusUnreachable, "构造探测请求失败"
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	respBody, status, err := c.doJSON(req)
	if err != nil {
		return T3EnvironmentStatusUnreachable, shortenT3Error(err)
	}
	if status == http.StatusOK {
		var state struct {
			Authenticated bool `json:"authenticated"`
		}
		if err := json.Unmarshal(respBody, &state); err == nil && state.Authenticated {
			return T3EnvironmentStatusOK, ""
		}
		return T3EnvironmentStatusRepair, "服务端应答 200 但会话未被接受"
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return T3EnvironmentStatusRepair, "凭证已失效，请重新配对"
	}
	return T3EnvironmentStatusUnreachable, fmt.Sprintf("探测返回 HTTP %d: %s", status, t3ErrorMessage(respBody))
}

// fetchThreadDetail 拉取一个线程的完整投影（HTTP 快照端点，非分页窗口）。
// 返回原始 JSON 的 projection 对象，由归一化层解读。
func (c *t3HTTPClient) fetchThreadDetail(ctx context.Context, httpBaseURL, bearerToken, threadID string) (json.RawMessage, error) {
	endpoint := strings.TrimSuffix(httpBaseURL, "/") + "/api/orchestration/threads/" + url.PathEscape(threadID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造线程详情请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set(t3OrchestrationProtocolHdr, t3OrchestrationProtocolV)

	respBody, status, err := c.doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("线程详情请求失败: %w", err)
	}
	switch status {
	case http.StatusOK:
		var snapshot struct {
			SnapshotSequence int             `json:"snapshotSequence"`
			Projection       json.RawMessage `json:"projection"`
		}
		if err := json.Unmarshal(respBody, &snapshot); err != nil {
			return nil, fmt.Errorf("线程快照解析失败: %w", err)
		}
		if len(snapshot.Projection) == 0 {
			return nil, fmt.Errorf("线程快照缺少 projection")
		}
		return snapshot.Projection, nil
	case http.StatusNotFound:
		return nil, errT3ThreadNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errT3CredentialRejected
	default:
		return nil, fmt.Errorf("线程详情返回 HTTP %d: %s", status, t3ErrorMessage(respBody))
	}
}

// fetchProjectSnapshot 拉取项目全表（GET /api/projects，无需 orchestration
// protocol 头，要求 orchestration:read；contracts environmentHttp.ts projects
// group、project/http.ts snapshot handler）。返回 {projects:[Project], updatedAt}
// 中的 Project 列表：过滤 deletedAt 非空的项（上游软删即下线），映射成 shell
// 侧同构的 t3ProjectShell，供启动/绑定路径替代全量 shell 快照。
//
// 错误分类：401/403 → errT3CredentialRejected（凭证失效走 repair 引导重新配对）；
// 404 一律按上游错误——该端点只在较新 T3 上存在，缺端点是版本不兼容而非
// 「项目不存在」，绝不能映射成 errT3ThreadNotFound；其余状态码与解析失败都是
// 上游错误。
func (c *t3HTTPClient) fetchProjectSnapshot(ctx context.Context, httpBaseURL, bearerToken string) ([]t3ProjectShell, error) {
	endpoint := strings.TrimSuffix(httpBaseURL, "/") + "/api/projects"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造项目快照请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	respBody, status, err := c.doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("项目快照请求失败: %w", err)
	}
	switch status {
	case http.StatusOK:
		var snapshot struct {
			Projects []struct {
				ID                    string            `json:"id"`
				Title                 string            `json:"title"`
				WorkspaceRoot         string            `json:"workspaceRoot"`
				DefaultModelSelection *t3ModelSelection `json:"defaultModelSelection"`
				DeletedAt             *string           `json:"deletedAt"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(respBody, &snapshot); err != nil {
			return nil, fmt.Errorf("项目快照解析失败: %w", err)
		}
		projects := make([]t3ProjectShell, 0, len(snapshot.Projects))
		for _, project := range snapshot.Projects {
			if project.DeletedAt != nil {
				continue
			}
			shell := t3ProjectShell{
				ID:                    project.ID,
				Title:                 project.Title,
				DefaultModelSelection: project.DefaultModelSelection,
			}
			if project.WorkspaceRoot != "" {
				root := project.WorkspaceRoot
				shell.WorkspaceRoot = &root
			}
			projects = append(projects, shell)
		}
		return projects, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errT3CredentialRejected
	default:
		// 404（端点缺失/版本不兼容）与其余状态码一样按上游错误处理。
		return nil, fmt.Errorf("项目快照返回 HTTP %d: %s", status, t3ErrorMessage(respBody))
	}
}

// doJSON 执行请求并读响应体。读到上限 +1 字节即明确拒绝——响应体被截断的
// JSON 不能当成完整投影继续解析（截断点可能恰好还是合法 JSON）。
func (c *t3HTTPClient) doJSON(req *http.Request) ([]byte, int, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	limit := c.maxBodyBytes
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取响应失败: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, fmt.Errorf("响应超过 %d 字节上限，已拒绝解析", limit)
	}
	return body, resp.StatusCode, nil
}

// t3GrantedScopes 把落库的 scope 串切成列表（RFC 6749 以单个空格分隔，
// 与 T3 的 parseOAuthScope 一致；空段丢弃）。
func t3GrantedScopes(scope string) []string {
	parts := strings.Split(scope, " ")
	granted := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			granted = append(granted, part)
		}
	}
	return granted
}

// t3HasOperateScope 判定会话是否可操作：唯一依据是精确持有
// orchestration:operate（T3 RpcAuthorization 的写方法全部要求该 scope，
// legacy 宽 scope 集不含它，无需再做父级推导）。
func t3HasOperateScope(scope string) bool {
	for _, granted := range t3GrantedScopes(scope) {
		if granted == t3ScopeOrchestrationOperate {
			return true
		}
	}
	return false
}

func t3ErrorMessage(body []byte) string {
	var envelope struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Reason  string `json:"reason"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		trimmed := strings.TrimSpace(string(body))
		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
		}
		return trimmed
	}
	switch {
	case envelope.Message != "":
		return envelope.Message
	case envelope.Error != "":
		return envelope.Error
	case envelope.Code != "":
		if envelope.Reason != "" {
			return envelope.Code + " (" + envelope.Reason + ")"
		}
		return envelope.Code
	default:
		return "无错误详情"
	}
}

// 供上层把上游分支翻译成 Fast Ship 业务错误的哨兵错误。
var (
	errT3ThreadNotFound     = fmt.Errorf("线程不存在")
	errT3CredentialRejected = fmt.Errorf("凭证被拒绝")
)

func t3NumberValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func shortenT3Error(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// resolvePairedHTTPURL 把 T3 返回的附件路径解析到配对 origin 上。
// 禁止字符串拼接把 `@host` / `//host` / 绝对 URL 带到别的主机。
func resolvePairedHTTPURL(httpBaseURL, relativePath string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(httpBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("配对地址无效")
	}
	ref, err := url.Parse(strings.TrimSpace(relativePath))
	if err != nil || relativePath == "" {
		return "", fmt.Errorf("附件地址无效")
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host {
		return "", fmt.Errorf("附件地址必须落在配对主机上")
	}
	if resolved.User != nil {
		return "", fmt.Errorf("附件地址必须落在配对主机上")
	}
	return resolved.String(), nil
}

// postRawBytes 把原始字节流式 POST 到上游相对路径（签名上传 URL）。
// body 原样转发不落盘；contentLength 用于上游的 Content-Length 校验
// （缺失或与声明的 sizeBytes 不符会被 T3 拒绝）。
func (c *t3HTTPClient) postRawBytes(ctx context.Context, httpBaseURL, relativePath, contentType string, contentLength int64, body io.Reader) (int, []byte, error) {
	endpoint, err := resolvePairedHTTPURL(httpBaseURL, relativePath)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return 0, nil, fmt.Errorf("构造上传请求失败: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = contentLength
	respBody, status, err := c.doJSON(req)
	if err != nil {
		return 0, nil, fmt.Errorf("上传请求失败: %w", err)
	}
	return status, respBody, nil
}

// getRawBytes GET 上游相对路径并返回字节与 Content-Type（附件内容代理）。
func (c *t3HTTPClient) getRawBytes(ctx context.Context, httpBaseURL, relativePath, bearerToken string) (status int, contentType string, body []byte, err error) {
	endpoint, err := resolvePairedHTTPURL(httpBaseURL, relativePath)
	if err != nil {
		return 0, "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", nil, fmt.Errorf("构造附件请求失败: %w", err)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", nil, fmt.Errorf("附件请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, c.maxBodyBytes+1))
	if readErr != nil {
		return resp.StatusCode, "", nil, fmt.Errorf("读取附件失败: %w", readErr)
	}
	if int64(len(data)) > c.maxBodyBytes {
		return resp.StatusCode, "", nil, fmt.Errorf("附件超过 %d 字节上限，已拒绝转发", c.maxBodyBytes)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), data, nil
}
