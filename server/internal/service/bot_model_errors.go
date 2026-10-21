package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
)

// bot_model_errors.go 上游模型失败的分类层（INT-137）。不同兼容供应商的报错
// 形状各异，这里把它们收敛成可行动的错误码，供三处共用：
//   - 主线失败的 run_error 事件：中文说明 + 可操作动作（前端错误条）；
//   - 定时任务的 provider 熔断：只认认证失效与额度用完；
//   - 未输出前的有限重试：只认限流、5xx 与网络中断。
//
// 分类只读结构化状态码与常见文案，不做供应商专属适配。

const (
	botModelErrAuthFailed     = "provider_auth_failed"
	botModelErrQuotaExhausted = "provider_quota_exhausted"
	botModelErrRateLimited    = "provider_rate_limited"
	botModelErrUnavailable    = "provider_unavailable"
	botModelErrModelNotFound  = "model_not_found"
	botModelErrStreamTimeout  = "model_stream_timeout"
	botModelErrRequestFailed  = "model_request_failed"
)

// botModelActionAIProviders 是前端动作标识：跳到 AI 提供商设置页。
const botModelActionAIProviders = "ai_providers_settings"

// botModelRetryAfterCap 是重试等待的上限：上游给的 Retry-After 再长也不在
// 主线槽里干等。
const botModelRetryAfterCap = 60 * time.Second

// botModelErrorAction 是给前端的可操作按钮；Kind 由前端映射到具体页面。
type botModelErrorAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// botModelErrorInfo 是一次模型失败的可操作分类。
type botModelErrorInfo struct {
	Code    string
	Message string // 给用户看的中文说明
	Detail  string // 上游原始报错（截断），供排查
	Action  *botModelErrorAction
}

// botStreamTimeoutError 是流式看门狗触发的超时；Phase 区分首包与片段间空闲，
// Timeout 是当时生效的阈值。
type botStreamTimeoutError struct {
	Phase   string
	Timeout time.Duration
}

func (e *botStreamTimeoutError) Error() string {
	phase := "首个片段"
	if e.Phase == "idle" {
		phase = "片段间空闲"
	}
	return "模型流超时（" + phase + "超过 " + e.Timeout.String() + "）"
}

func botAIProvidersAction() *botModelErrorAction {
	return &botModelErrorAction{Kind: botModelActionAIProviders, Label: "去设置"}
}

// botClassifyModelError 把任意模型/传输错误映射为可操作分类。
func botClassifyModelError(err error) botModelErrorInfo {
	if err == nil {
		return botModelErrorInfo{Code: botModelErrUnavailable, Message: "模型请求失败"}
	}
	var timeout *botStreamTimeoutError
	if errors.As(err, &timeout) {
		return botModelErrorInfo{Code: botModelErrStreamTimeout, Message: timeout.Error(), Detail: timeout.Error()}
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		lower := strings.ToLower(apiErr.Code + " " + apiErr.Message + " " + apiErr.Type)
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return botModelErrorInfo{
				Code:    botModelErrAuthFailed,
				Message: "AI 提供商认证失败：密钥无效或已失效，请到设置页更新密钥",
				Detail:  botErrMessage(err),
				Action:  botAIProvidersAction(),
			}
		case status == http.StatusPaymentRequired || botLooksQuotaExhausted(lower):
			// 供应商常用 429 + insufficient_quota 表达「额度用完」，所以额度
			// 判定必须排在限流之前。
			return botModelErrorInfo{
				Code:    botModelErrQuotaExhausted,
				Message: "AI 提供商额度已用完或余额不足，请充值或更换提供商",
				Detail:  botErrMessage(err),
				Action:  botAIProvidersAction(),
			}
		case status == http.StatusTooManyRequests:
			return botModelErrorInfo{
				Code:    botModelErrRateLimited,
				Message: "AI 提供商限流中，请稍后再试",
				Detail:  botErrMessage(err),
			}
		case status == http.StatusNotFound || botLooksModelMissing(lower):
			return botModelErrorInfo{
				Code:    botModelErrModelNotFound,
				Message: "模型不存在或当前账号不可用，请在机器人设置里更换模型",
				Detail:  botErrMessage(err),
			}
		case status >= 500:
			return botModelErrorInfo{
				Code:    botModelErrUnavailable,
				Message: "AI 提供商服务暂时不可用，请稍后重试",
				Detail:  botErrMessage(err),
			}
		}
	}

	// 文本兜底（INT-137）：部分兼容端点在 HTTP 200 里返回 {"error":...}，SDK
	// 降级成普通 error，上面的结构化分支看不见。按同一批短语收敛；额度优先
	// 于认证与模型判定，不匹配 billing（泛词误伤太多）。
	lowerAll := strings.ToLower(err.Error())
	switch {
	case botLooksQuotaExhausted(lowerAll):
		return botModelErrorInfo{
			Code:    botModelErrQuotaExhausted,
			Message: "AI 提供商额度已用完或余额不足，请充值或更换提供商",
			Detail:  botErrMessage(err),
			Action:  botAIProvidersAction(),
		}
	case botLooksAuthFailed(lowerAll):
		return botModelErrorInfo{
			Code:    botModelErrAuthFailed,
			Message: "AI 提供商认证失败：密钥无效或已失效，请到设置页更新密钥",
			Detail:  botErrMessage(err),
			Action:  botAIProvidersAction(),
		}
	case botLooksModelMissing(lowerAll):
		return botModelErrorInfo{
			Code:    botModelErrModelNotFound,
			Message: "模型不存在或当前账号不可用，请在机器人设置里更换模型",
			Detail:  botErrMessage(err),
		}
	}

	if botIsNetworkError(err) {
		return botModelErrorInfo{
			Code:    botModelErrUnavailable,
			Message: "无法连接 AI 提供商（网络中断或连接被重置）",
			Detail:  botErrMessage(err),
		}
	}
	return botModelErrorInfo{Code: botModelErrRequestFailed, Message: "模型请求失败", Detail: botErrMessage(err)}
}

// botLooksQuotaExhausted 是额度文案判定：只认明确的额度/余额含义。不带
// billing / payment required 这类泛词——前者会误伤普通账务报错，后者已由
// 402 状态码覆盖。
func botLooksQuotaExhausted(lower string) bool {
	for _, m := range []string{
		"insufficient_quota", "insufficient quota", "quota exceeded", "exceeded your current quota",
		"credit balance", "余额不足", "额度不足", "额度已用完",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// botLooksAuthFailed 是认证失效文案判定（文本兜底用）。
func botLooksAuthFailed(lower string) bool {
	for _, m := range []string{
		"invalid api key", "incorrect api key", "invalid_api_key",
		"unauthorized", "authentication", "api key not valid",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// botLooksModelMissing 是模型不存在文案判定。
func botLooksModelMissing(lower string) bool {
	for _, m := range []string{
		"model_not_found", "model not found", "no such model", "does not exist", "unknown model",
		"invalid model", "unsupported model",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// botIsNetworkError 判定连接级失败：断开、重置、EOF 与拨号超时。
func botIsNetworkError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, m := range []string{
		"connection reset", "connection refused", "broken pipe", "unexpected eof", "eof",
		"tls handshake timeout", "no such host", "i/o timeout", "server closed",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// botCircuitTrippingCode 报告该错误是否要打开 provider 熔断：只有「再试也不
// 会好」的认证失效与额度用完才熔断；限流与网络抖动交给重试。
func botCircuitTrippingCode(code string) bool {
	return code == botModelErrAuthFailed || code == botModelErrQuotaExhausted
}

// botShouldRetryModelError 报告「尚未输出任何片段」的失败是否值得重试，并
// 返回上游要求的最小等待（Retry-After，封顶 60s；无则 0）。判定先走错误
// 分类：认证失效、额度用完、模型不存在与流超时再试也不会好，一律不重试
// ——429 里混着 insufficient_quota 时状态码本身不可信（INT-137）；其余仍是
// 限流、5xx、408 与网络中断可重试。
func botShouldRetryModelError(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	switch botClassifyModelError(err).Code {
	case botModelErrAuthFailed, botModelErrQuotaExhausted, botModelErrModelNotFound, botModelErrStreamTimeout:
		return 0, false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		if status == http.StatusTooManyRequests {
			return botRetryAfter(apiErr), true
		}
		if status >= 500 || status == http.StatusRequestTimeout {
			return botRetryAfter(apiErr), true
		}
		return 0, false
	}
	return 0, botIsNetworkError(err)
}

// botRetryAfter 解析 Retry-After（秒数或 HTTP 日期），封顶 60s。
func botRetryAfter(apiErr *openai.Error) time.Duration {
	if apiErr == nil || apiErr.Response == nil {
		return 0
	}
	raw := strings.TrimSpace(apiErr.Response.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	var wait time.Duration
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0
		}
		wait = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(raw); err == nil {
		wait = time.Until(at)
		if wait < 0 {
			return 0
		}
	} else {
		return 0
	}
	if wait > botModelRetryAfterCap {
		return botModelRetryAfterCap
	}
	return wait
}
