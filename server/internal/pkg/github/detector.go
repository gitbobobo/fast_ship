package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	gh "github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"
)

// 检测状态与 Token 类型，与 model.UserGitHubToken 的列取值保持一致。
// 只读检测（GET /user），不通过创建 Issue/Tag/Release 试探写权限。
const (
	DetectStatusOK      = "ok"
	DetectStatusInvalid = "invalid"
	DetectStatusFailed  = "failed"

	DetectTypeClassic     = "classic"
	DetectTypeFineGrained = "fine_grained"
	DetectTypeUnknown     = "unknown"
)

const fineGrainedDetail = "Fine-grained Token 无法自动获取完整授权"

// detectFailedDetail 是检测失败写进 API 的固定文案。不拼接 err.Error()：
// go-github / url.Error 可能把请求 URL（含 userinfo）带进错误字符串。
const detectFailedDetail = "检测失败：网络或 GitHub 服务异常"

// TokenDetection 是一次只读检测的结果，字段直接对应 Token 库的检测列。
// Status 区分「凭证无效」（GitHub 明确拒绝）与「检测失败」（网络/服务端故障）——
// 网络失败不能把 Token 判成无效。
type TokenDetection struct {
	Status    string
	TokenType string
	Scopes    []string
	Login     string
	Detail    string
}

// TokenDetector 用 GET /user 校验 Token 并尽量提取权限信息：
// Classic PAT 从 X-OAuth-Scopes 响应头读 scopes；Fine-grained Token 该头恒为空，
// 无法自动获取完整授权，只按 github_pat_ 前缀识别类型并如实说明。
type TokenDetector struct {
	baseURL string
	client  *http.Client
	timeout time.Duration
}

// NewTokenDetector 创建指向 github.com 的检测器；测试经 NewTokenDetectorWithClient 注入替身。
func NewTokenDetector() *TokenDetector {
	return &TokenDetector{
		baseURL: "https://api.github.com/",
		client:  &http.Client{},
		timeout: 10 * time.Second,
	}
}

func NewTokenDetectorWithClient(baseURL string, client *http.Client) *TokenDetector {
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return &TokenDetector{baseURL: baseURL, client: client, timeout: 10 * time.Second}
}

// Detect 执行一次检测，任何路径都不 panic、不返回 error——结果全部折进 TokenDetection。
func (d *TokenDetector) Detect(ctx context.Context, token string) TokenDetection {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	httpClient := d.client
	if token != "" {
		httpClient = oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	}
	client := gh.NewClient(httpClient)
	if baseURL, err := url.Parse(d.baseURL); err == nil {
		client.BaseURL = baseURL
	}

	user, resp, err := client.Users.Get(ctx, "")
	if err != nil {
		return d.classifyError(token, err)
	}

	detection := TokenDetection{
		Status:    DetectStatusOK,
		TokenType: DetectTypeUnknown,
		Scopes:    []string{},
		Login:     user.GetLogin(),
	}
	if resp != nil {
		if scopes := parseScopesHeader(resp.Header.Get("X-OAuth-Scopes")); len(scopes) > 0 {
			detection.TokenType = DetectTypeClassic
			detection.Scopes = scopes
			return detection
		}
	}
	if strings.HasPrefix(token, "github_pat_") {
		detection.TokenType = DetectTypeFineGrained
		detection.Detail = fineGrainedDetail
	}
	return detection
}

// classifyError 区分凭证无效与检测失败：401 是 GitHub 明确拒绝凭证；
// 限流、5xx、超时、连接失败都是检测失败，不得判成凭证无效。
func (d *TokenDetector) classifyError(token string, err error) TokenDetection {
	var errResp *gh.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil && errResp.Response.StatusCode == http.StatusUnauthorized {
		return TokenDetection{Status: DetectStatusInvalid, TokenType: DetectTypeUnknown, Scopes: []string{}, Detail: "GitHub 拒绝了该 Token（401）"}
	}
	if strings.HasPrefix(token, "github_pat_") {
		return TokenDetection{Status: DetectStatusFailed, TokenType: DetectTypeFineGrained, Scopes: []string{}, Detail: detectFailedDetail + "；" + fineGrainedDetail}
	}
	return TokenDetection{Status: DetectStatusFailed, TokenType: DetectTypeUnknown, Scopes: []string{}, Detail: detectFailedDetail}
}

// parseScopesHeader 解析 X-OAuth-Scopes（逗号分隔，项前后可带空格）；空头返回 nil。
func parseScopesHeader(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	scopes := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	return scopes
}
