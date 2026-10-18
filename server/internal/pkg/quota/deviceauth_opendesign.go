package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenDesign 设备授权协议（INT-117）：与服务端官方 CLI vela login 同款流程，
// 两个端点均无需认证，token 端点用 body 里的设备密钥。协议的全部细节——
// 路径、请求/响应字段名、状态词表、401/404 语义——都收在本文件，
// service 层只编排会话与建号。

const (
	// openDesignDeviceAuthStartPath / openDesignDeviceAuthTokenPath 是上游设备
	// 授权的两个端点；token 端点以路径参数带 deviceId。
	openDesignDeviceAuthStartPath = "/api/v1/cli/device-authorizations"
	openDesignDeviceAuthTokenPath = "/api/v1/cli/device-authorizations/%s/token"

	// openDesignDeviceAuthClientBody 标识发起方；上游把它记在授权记录里。
	openDesignDeviceAuthClientBody = `{"cliVersion":"fast-ship","host":"fast-ship","profile":"prod"}`

	// openDesignDeviceAuthPollIntervalDefault 是上游未给 pollIntervalSeconds 时的轮询节奏。
	openDesignDeviceAuthPollIntervalDefault = 2 * time.Second
)

// OpenDesignDeviceAuth 返回 OpenDesign 控制面的设备授权协议实现。
func OpenDesignDeviceAuth() *DeviceAuth {
	return &DeviceAuth{Start: openDesignDeviceAuthStart, Poll: openDesignDeviceAuthToken}
}

// openDesignDeviceAuthStart 建一次授权。上游返回 201，body 含
// deviceId/deviceSecret/userCode/activationUrl/expiresAt/pollIntervalSeconds/status。
func openDesignDeviceAuthStart(ctx context.Context, client *http.Client, baseURL string) (DeviceAuthStart, error) {
	status, body, err := httpPostJSON(ctx, client,
		strings.TrimRight(baseURL, "/")+openDesignDeviceAuthStartPath,
		json.RawMessage(openDesignDeviceAuthClientBody))
	if err != nil {
		return DeviceAuthStart{}, err
	}
	if err := requireOK("OpenDesign", status); err != nil {
		return DeviceAuthStart{}, err
	}
	root, err := jsonRoot(body)
	if err != nil {
		return DeviceAuthStart{}, err
	}
	var start DeviceAuthStart
	start.DeviceID, _ = asString(root["deviceId"])
	start.DeviceSecret, _ = asString(root["deviceSecret"])
	start.UserCode, _ = asString(root["userCode"])
	start.ActivationURL, _ = asString(root["activationUrl"])
	if t, ok := flexibleDate(root["expiresAt"]); ok {
		start.ExpiresAt = t
	}
	if secs, ok := asNumber(root["pollIntervalSeconds"]); ok && secs > 0 {
		start.PollInterval = time.Duration(secs) * time.Second
	}
	if start.PollInterval <= 0 {
		start.PollInterval = openDesignDeviceAuthPollIntervalDefault
	}
	// activationUrl 由前端 target=_blank 直接打开：scheme 必须限定 http/https，
	// 否则上游被劫持或接口变更时会把用户带去任意 scheme。
	if u, err := url.Parse(start.ActivationURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return DeviceAuthStart{}, ProviderError("OpenDesign 返回的授权链接不合法，接口可能已变更。", 0)
	}
	return start, nil
}

// openDesignDeviceAuthToken 代查一次 token。真实上游语义（2026-10 实测）：
// 未批准 → 200 + status "pending"（controlKey/runtimeKey/user 为 null）；
// 代码过期 → 200 + status "expired"（不是 404）；设备密钥错误 → 401；授权不存在 → 404；
// 批准后 → 200 + controlKey。响应里另有 runtimeKey（模型面密钥）：结构上不声明、
// 读到即丢，不进凭据组、不进日志。
func openDesignDeviceAuthToken(ctx context.Context, client *http.Client, baseURL, deviceID, deviceSecret string) (DeviceAuthPoll, error) {
	target := strings.TrimRight(baseURL, "/") + fmt.Sprintf(openDesignDeviceAuthTokenPath, url.PathEscape(deviceID))
	status, body, err := httpPostJSON(ctx, client, target, map[string]string{"deviceSecret": deviceSecret})
	if err != nil {
		return DeviceAuthPoll{}, err
	}
	switch status {
	case http.StatusUnauthorized, http.StatusNotFound:
		// 401 设备密钥失效 / 404 授权不存在：会话已不可用，统一 invalid。
		return DeviceAuthPoll{State: DeviceAuthInvalid, Message: "设备授权已失效，请重新发起授权。"}, nil
	}
	if err := requireOK("OpenDesign", status); err != nil {
		return DeviceAuthPoll{}, err
	}
	root, err := jsonRoot(body)
	if err != nil {
		return DeviceAuthPoll{}, err
	}
	if key, ok := asString(root["controlKey"]); ok {
		return DeviceAuthPoll{State: DeviceAuthApproved, Credentials: Credentials{"control_key": key}}, nil
	}
	statusText, _ := asString(root["status"])
	switch strings.ToLower(strings.TrimSpace(statusText)) {
	case "expired", "canceled", "cancelled":
		return DeviceAuthPoll{State: DeviceAuthExpired, Message: "验证码已过期，请重新发起授权。"}, nil
	case "denied", "rejected":
		return DeviceAuthPoll{State: DeviceAuthDenied, Message: "授权已被拒绝，请重新发起授权。"}, nil
	default:
		// 其它 2xx 且无 controlKey 也非终态：用户还没批准的正常等待。
		return DeviceAuthPoll{State: DeviceAuthPending}, nil
	}
}
