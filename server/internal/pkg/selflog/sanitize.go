package selflog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

// sensitiveKeys 是整值打码的确切字段名（大小写不敏感，取点分路径最后一段）。
// token_id / api_key_id 等标识字段不在此列，避免排障字段被误伤。
var sensitiveKeys = map[string]struct{}{
	"password":      {},
	"passwd":        {},
	"secret":        {},
	"authorization": {},
	"token":         {},
	"api_key":       {},
	"apikey":        {},
	"credential":    {},
	"cookie":        {},
}

// valuePatterns 按值的形状脱敏，兜底捕获未按敏感 key 命名的明文凭证。
var valuePatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Fast Ship 自家凭证：API Key 与运行/刷新令牌。
	{regexp.MustCompile(`fsk_[A-Za-z0-9_-]{8,}`), "fsk_" + redacted},
	{regexp.MustCompile(`fsr_[A-Za-z0-9_-]{8,}`), "fsr_" + redacted},
	// GitHub Token 全家族（classic PAT / OAuth / App / 安装 / user / refresh）与细粒度 PAT。
	{regexp.MustCompile(`gh[pousrf]_[A-Za-z0-9]{16,}`), "gh?_" + redacted},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), "github_pat_" + redacted},
	// Bearer / Basic 凭证与 JWT 三段式形状。
	{regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), redacted},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`), redacted},
}

// sanitizeString 对任意字符串做形状脱敏。
func sanitizeString(s string) string {
	if s == "" {
		return s
	}
	for _, p := range valuePatterns {
		if p.re.MatchString(s) {
			s = p.re.ReplaceAllString(s, p.repl)
		}
	}
	return s
}

// isSensitiveKey 判断属性名是否为确切秘密字段。点分路径取最后一段，
// 与 slog WithGroup 展平后的 key（如 http.authorization）对齐。
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if i := strings.LastIndex(lower, "."); i >= 0 {
		lower = lower[i+1:]
	}
	_, ok := sensitiveKeys[lower]
	return ok
}

// sanitizeValue 对字符串属性值脱敏：敏感 key 整值替换，其余做形状替换。
func sanitizeValue(key, value string) string {
	if isSensitiveKey(key) {
		return redacted
	}
	return sanitizeString(value)
}

// sanitizeAttrValue 递归脱敏附加字段：先按 key 判定，再按值类型展开。
// map/slice/struct 先落到 JSON 再走同一套规则，避免非 string 敏感值漏网。
func sanitizeAttrValue(key string, value interface{}) interface{} {
	if isSensitiveKey(key) {
		return redacted
	}
	if err, ok := value.(error); ok {
		return sanitizeString(err.Error())
	}
	switch t := value.(type) {
	case nil:
		return nil
	case string:
		return sanitizeString(t)
	case []byte:
		return sanitizeString(string(t))
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return t
	case json.Number:
		return t
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, v := range t {
			out[k] = sanitizeAttrValue(k, v)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, v := range t {
			out[i] = sanitizeAttrValue("", v)
		}
		return out
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return sanitizeString(fmt.Sprintf("%v", t))
		}
		var generic interface{}
		if err := json.Unmarshal(raw, &generic); err != nil {
			return sanitizeString(string(raw))
		}
		return sanitizeAttrValue(key, generic)
	}
}
