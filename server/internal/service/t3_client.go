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
// Fast Ship 只请求 orchestration:read，且校验返回 scope 恰好等于请求值，
// 拒绝任何更宽的会话。

const (
	t3ScopeOrchestrationRead = "orchestration:read"

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

// exchangeBearer 用一次性配对码换取仅 orchestration:read 的 Bearer 会话。
// 任何错误都返回带上下文的 error，由调用方映射成 40005/50202。
func (c *t3HTTPClient) exchangeBearer(ctx context.Context, httpBaseURL, pairingCredential string) (*t3BearerCredentials, error) {
	form := url.Values{}
	form.Set("grant_type", t3GrantTypeTokenExchange)
	form.Set("subject_token", pairingCredential)
	form.Set("subject_token_type", t3TokenTypeBootstrap)
	form.Set("requested_token_type", t3TokenTypeAccessToken)
	form.Set("scope", t3ScopeOrchestrationRead)
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
	if !t3ScopeExactlyRead(tokenResp.Scope) {
		return nil, fmt.Errorf("服务端授予的 scope %q 超出 orchestration:read，拒绝建立会话", tokenResp.Scope)
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

// t3ScopeExactlyRead 校验授予 scope 恰好是 orchestration:read 一项。
// 按 RFC 6749 以单个空格切分（与 T3 的 parseOAuthScope 一致，空段即非法）。
func t3ScopeExactlyRead(scope string) bool {
	scopes := strings.Split(scope, " ")
	if len(scopes) != 1 {
		return false
	}
	return scopes[0] == t3ScopeOrchestrationRead
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
