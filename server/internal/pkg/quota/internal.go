package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// userAgent 标识本服务的取数请求。各供应商只校验鉴权头，不依赖特定 UA。
const userAgent = "fast-ship-quota"

// maxBodyBytes 限制单次响应读取量，防止异常响应占满内存。
const maxBodyBytes = 4 << 20

// httpGet 用给定请求头发起 GET，返回状态码与响应体。网络层失败统一归为 provider_error。
func httpGet(ctx context.Context, client *http.Client, url string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, ProviderError("供应商接口地址无效。", 0)
	}
	req.Header.Set("User-Agent", userAgent)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, ProviderError("无法连接供应商接口，请稍后重试。", 0)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return resp.StatusCode, nil, ProviderError("读取供应商接口响应失败。", resp.StatusCode)
	}
	return resp.StatusCode, body, nil
}

// requireOK 把非 2xx 的状态码映射成结构化错误：401/403 是凭据问题，其余是接口问题。
func requireOK(providerName string, status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return CredentialError(fmt.Sprintf("%s 凭据无效或已过期，请重新粘贴（HTTP %d）。", providerName, status), status)
	case status == http.StatusTooManyRequests:
		return ProviderError(fmt.Sprintf("%s 接口限流，请稍后再试。", providerName), status)
	case status >= 500:
		return ProviderError(fmt.Sprintf("%s 接口暂时不可用（HTTP %d），请稍后重试。", providerName, status), status)
	default:
		return ProviderError(fmt.Sprintf("%s 接口返回异常状态（HTTP %d）。", providerName, status), status)
	}
}

// jsonRoot 把响应体解析成 JSON 对象；非对象或非法 JSON 都算解析失败。
func jsonRoot(data []byte) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, ProviderError("供应商返回了无法解析的响应，接口可能已变更。", 0)
	}
	return root, nil
}

// asNumber 从 JSON 值里取数字，兼容数字与数字字符串（部分供应商会把数值编成字符串）。
func asNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		n, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// asString 取非空字符串。
func asString(value any) (string, bool) {
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

// asObject 取嵌套对象。
func asObject(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

// asBool 取布尔值（JSON true/false）。
func asBool(value any) (bool, bool) {
	b, ok := value.(bool)
	return b, ok
}

// firstNumber 按顺序在多个键里找第一个可解析的数字。
func firstNumber(obj map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if n, ok := asNumber(obj[key]); ok {
			return n, true
		}
	}
	return 0, false
}

// firstString 按顺序在多个键里找第一个非空字符串。
func firstString(obj map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if s, ok := asString(obj[key]); ok {
			return s, true
		}
	}
	return "", false
}

// flexibleDate 解析供应商常见的几种时间表示：Unix 秒 / 毫秒时间戳（数字或数字字符串）、
// RFC3339 / ISO8601 字符串。返回 UTC 时间。1e12 以上的数字按毫秒处理。
func flexibleDate(value any) (time.Time, bool) {
	switch v := value.(type) {
	case float64:
		return unixDate(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return time.Time{}, false
		}
		if n, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return unixDate(n)
		}
		if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return t.UTC(), true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

func unixDate(n float64) (time.Time, bool) {
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return time.Time{}, false
	}
	if n > 1e12 {
		// 毫秒时间戳。
		return time.UnixMilli(int64(n)).UTC(), true
	}
	// 下界与 CodexBar 的 OpenCodeWebParsing.dateValue 一致：过小的正数
	// （相对秒数、占位值）不当作 Unix 秒，按解析失败处理。
	if n <= 1e9 {
		return time.Time{}, false
	}
	return time.Unix(int64(n), 0).UTC(), true
}

// firstDate 按键序逐个尝试，返回第一个能解析成时间的值（而不是第一个存在的键），
// 与 CodexBar 的回退语义一致：前面的键解析失败不影响后面的键。
func firstDate(obj map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if t, ok := flexibleDate(obj[key]); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// PercentPtr 返回指向 v 的指针，用于构造可空的数值字段（百分比 / used / limit 等）。
func PercentPtr(v float64) *float64 { return &v }

// kindRank 定义窗口的固定展示顺序：5 小时 → 每日 → 每周 → 每月 → 其他。
func kindRank(kind string) int {
	switch kind {
	case WindowFiveHour:
		return 0
	case WindowDaily:
		return 1
	case WindowWeekly:
		return 2
	case WindowMonthly:
		return 3
	default:
		return 4
	}
}

// sortWindows 按窗口类型排序（稳定），同类型保持供应商给出的先后顺序。
func sortWindows(windows []Window) {
	sort.SliceStable(windows, func(i, j int) bool {
		return kindRank(windows[i].Kind) < kindRank(windows[j].Kind)
	})
}

// windowForMinutes 把以分钟计的窗口长度映射成统一的类型与中文短名。
// 28–31 天的窗口都按月度处理（不同供应商的账期长度略有差异）。
func windowForMinutes(minutes int) (kind, label string) {
	switch {
	case minutes == 300:
		return WindowFiveHour, "5 小时"
	case minutes == 1440:
		return WindowDaily, "每日"
	case minutes == 10080:
		return WindowWeekly, "每周"
	case minutes >= 28*1440 && minutes <= 31*1440:
		return WindowMonthly, "每月"
	default:
		return WindowOther, ""
	}
}

// percentWindow 构造一个以百分比计量的窗口。
func percentWindow(kind, label string, percent float64, resetAt *time.Time) Window {
	p := ClampPercent(percent)
	return Window{Kind: kind, Label: label, UsedPercent: &p, Unit: UnitPercent, ResetAt: resetAt}
}

// resetPtr 把解析出的时间归一成 UTC 指针；零值视为缺失。
func resetPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
