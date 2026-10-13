package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// 配对链接解析，语义对齐 T3 官方客户端 packages/shared/src/remote.ts 的
// resolveRemotePairingTarget（2026-10 与仓库源码核对）：
//
//   - 直接链接：`https://host/#token=CODE`（hash 优先）或 `?token=CODE`
//   - hosted 包装链接：`https://relay/?host=<backend-url>&token=CODE&label=...`，
//     此时真正要连的是 host 参数指向的后端
//   - 协议支持 http/https/ws/wss；缺省协议时 IP 字面量补 http、其余补 https
//   - 解析出的基址只保留 origin（path/search/hash 丢弃），ws→http、wss→https

var t3SupportedPairingSchemes = map[string]bool{
	"http": true, "https": true, "ws": true, "wss": true,
}

// t3PairingTarget 是一次配对要连接的目标：后端基址 + 一次性配对码。
type t3PairingTarget struct {
	HTTPBaseURL string // 形如 https://host （尾随 /）
	WSBaseURL   string // 形如 wss://host （尾随 /）
	Credential  string // 配对码
	Host        string // 后端 origin，用于展示/校验
}

// resolveT3PairingTarget 把用户输入的配对链接解析成连接目标。
func resolveT3PairingTarget(pairingURL string) (*t3PairingTarget, error) {
	trimmed := strings.TrimSpace(pairingURL)
	if trimmed == "" {
		return nil, fmt.Errorf("配对链接为空")
	}

	// 缺省协议：IP 字面量按本地服务对待补 http，其余补 https（与 T3 客户端一致）。
	// 先看字符串再补协议：url.Parse 对 "ip:port/#token=x" 会先报错。
	if !t3HasExplicitScheme(trimmed) {
		probeHost := trimmed
		if idx := strings.IndexAny(probeHost, "/?#"); idx >= 0 {
			probeHost = probeHost[:idx]
		}
		if isIPLiteral(probeHost) {
			trimmed = "http://" + trimmed
		} else {
			trimmed = "https://" + trimmed
		}
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("配对链接无法解析: %w", err)
	}
	if !t3SupportedPairingSchemes[parsed.Scheme] {
		return nil, fmt.Errorf("不支持的配对链接协议 %q（仅支持 http/https/ws/wss）", parsed.Scheme)
	}

	// hosted 包装链接：host query 参数才是真正的后端地址
	if hostedHost := strings.TrimSpace(parsed.Query().Get("host")); hostedHost != "" {
		token := t3PairingTokenFromURL(parsed)
		if token == "" {
			return nil, fmt.Errorf("hosted 配对链接缺少 token")
		}
		return newT3PairingTarget(hostedHost, token)
	}

	token := t3PairingTokenFromURL(parsed)
	if token == "" {
		return nil, fmt.Errorf("配对链接缺少配对码（token）")
	}

	// 直接链接：只保留 origin
	parsed.Path = "/"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return newT3PairingTarget(parsed.String(), token)
}

var t3ExplicitSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

func t3HasExplicitScheme(raw string) bool {
	return t3ExplicitSchemeRe.MatchString(raw)
}

// t3PairingTokenFromURL 读配对码：hash 里优先，其次 query（与 T3 客户端一致）。
func t3PairingTokenFromURL(u *url.URL) string {
	if u.Fragment != "" {
		hashParams, err := url.ParseQuery(u.Fragment)
		if err == nil {
			if token := strings.TrimSpace(hashParams.Get("token")); token != "" {
				return token
			}
		}
	}
	return strings.TrimSpace(u.Query().Get("token"))
}

// t3NormalizeBaseURL 把用户输入的主机/链接规范化成带协议的 origin。
func t3NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("后端地址为空")
	}
	withoutLeadingSlashes := strings.TrimLeft(trimmed, "/")
	normalized := withoutLeadingSlashes
	if !strings.Contains(withoutLeadingSlashes, "://") {
		if isIPLiteral(withoutLeadingSlashes) {
			normalized = "http://" + withoutLeadingSlashes
		} else {
			normalized = "https://" + withoutLeadingSlashes
		}
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("后端地址无效: %w", err)
	}
	if !t3SupportedPairingSchemes[parsed.Scheme] {
		return "", fmt.Errorf("不支持的后端地址协议 %q", parsed.Scheme)
	}
	parsed.Path = "/"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// newT3PairingTarget 由任意受支持协议的基址构造连接目标：
// HTTP 面把 ws→http、wss→https；WS 面反向转换。
func newT3PairingTarget(base, credential string) (*t3PairingTarget, error) {
	normalized, err := t3NormalizeBaseURL(base)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("后端地址无效: %w", err)
	}

	httpScheme := parsed.Scheme
	switch httpScheme {
	case "ws":
		httpScheme = "http"
	case "wss":
		httpScheme = "https"
	}
	wsScheme := "ws"
	if httpScheme == "https" {
		wsScheme = "wss"
	}

	httpBase := *parsed
	httpBase.Scheme = httpScheme
	wsBase := *parsed
	wsBase.Scheme = wsScheme

	return &t3PairingTarget{
		HTTPBaseURL: httpBase.String(),
		WSBaseURL:   wsBase.String(),
		Credential:  credential,
		Host:        parsed.Host,
	}, nil
}

// isIPLiteral 判断 host 是否为 IP 字面量（决定缺省协议用 http 还是 https），
// 语义对齐 T3 移动端 pairing.ts 的 isIpLiteral：先取 hostname（去端口/方括号），
// 含 ":" 视为 IPv6；否则须为 4 段全数字且每段 ≤255。
func isIPLiteral(host string) bool {
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	if idx := strings.IndexAny(host, "/?#"); idx >= 0 {
		host = host[:idx]
	}
	if host == "" {
		return false
	}
	if strings.Count(host, ":") > 1 {
		return true // IPv6
	}
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx] // host:port
	}
	octets := strings.Split(host, ".")
	if len(octets) != 4 {
		return false
	}
	for _, octet := range octets {
		if octet == "" || len(octet) > 3 {
			return false
		}
		value := 0
		for _, ch := range octet {
			if ch < '0' || ch > '9' {
				return false
			}
			value = value*10 + int(ch-'0')
		}
		if value > 255 {
			return false
		}
	}
	return true
}
