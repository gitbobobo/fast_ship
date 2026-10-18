// Package quota 实现各 AI 编码服务订阅套餐额度的取数与解析。
//
// 职责边界：本包只负责「用一组凭据向某个供应商拉一次额度，解析成统一的
// Snapshot」。加密、落库、用户隔离、调度、退避与 HTTP 接口都在 service /
// repository / handler 层，不在这里。
//
// 详细契约见 docs/internal/quota.md 与 INT-109 的冻结契约。
package quota

import (
	"context"
	"math"
	"net/http"
	"time"
)

// 窗口类型：5 小时 / 每日 / 每周 / 每月 / 其他。
const (
	WindowFiveHour = "five_hour"
	WindowDaily    = "daily"
	WindowWeekly   = "weekly"
	WindowMonthly  = "monthly"
	WindowOther    = "other"
)

// 窗口计量单位。
const (
	UnitPercent = "percent"
	UnitUSD     = "usd"
	UnitCount   = "count"
)

// Window 是一个额度窗口。UsedPercent 允许为空（供应商没给百分比也算不出时）。
type Window struct {
	Kind        string     `json:"kind"`
	Label       string     `json:"label"`
	UsedPercent *float64   `json:"used_percent"`
	Used        *float64   `json:"used,omitempty"`
	Limit       *float64   `json:"limit,omitempty"`
	Unit        string     `json:"unit"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
}

// Balance 是可选的余额 / 剩余额度（如美元）。
type Balance struct {
	Label    string  `json:"label"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// Snapshot 是一次成功抓取的完整结果。Note 是供应商特有的补充说明
// （如「无 Design Plan 额度，按钱包计费」），空串表示无。
type Snapshot struct {
	PlanName  string    `json:"plan_name"`
	Windows   []Window  `json:"windows"`
	Balance   *Balance  `json:"balance"`
	Note      string    `json:"note,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Credentials 是按 credential_fields 的 key 组织的明文凭据。
type Credentials map[string]string

// CredentialField 描述一个供应商需要的一个凭据字段。
type CredentialField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder"`
	Help        string `json:"help"`
}

// Fetcher 拉取并解析一次额度。baseURL 是该供应商的 origin（可被配置覆盖）。
type Fetcher func(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error)

// Provider 是一个供应商的元数据与取数实现。Validate 是可选的凭据结构校验钩子
// （nil 表示不校验）：service 在必填检查后调用，返回非空消息以 40031 上抛
// （空串 = 通过），让 create/update/probe 在发请求前就明确报错
// （如 OpenDesign 的模型面密钥）。DeviceAuth 非空表示支持浏览器设备授权，
// 协议实现在供应商自己的适配器文件里，service 只编排会话。
type Provider struct {
	ID               string
	Name             string
	PlanHint         string
	CredentialFields []CredentialField
	Instructions     []string
	Warning          string
	DefaultBaseURL   string
	DeviceAuth       *DeviceAuth
	Fetch            Fetcher
	Validate         func(Credentials) string
}

// DeviceAuthState 是一次设备授权轮询的统一结论，与上游具体状态词表解耦。
type DeviceAuthState string

const (
	DeviceAuthPending  DeviceAuthState = "pending"
	DeviceAuthApproved DeviceAuthState = "approved"
	DeviceAuthExpired  DeviceAuthState = "expired"
	DeviceAuthDenied   DeviceAuthState = "denied"
	DeviceAuthInvalid  DeviceAuthState = "invalid" // 设备密钥失效/授权不存在
)

// DeviceAuthStart 是一次设备授权的上游视图（设备密钥只留在服务端内存）。
type DeviceAuthStart struct {
	DeviceID      string
	DeviceSecret  string
	UserCode      string
	ActivationURL string
	ExpiresAt     time.Time
	PollInterval  time.Duration
}

// DeviceAuthPoll 是一次上游轮询的结论。Approved 时 Credentials 是按
// provider.CredentialFields 的 key 组织好的凭据组（例如 {"control_key": ...}）；
// Message 是终态的用户可读原因，pending 时为空。
type DeviceAuthPoll struct {
	State       DeviceAuthState
	Credentials Credentials
	Message     string
}

// DeviceAuth 是一个供应商的浏览器设备授权协议实现（发起 + 单次轮询）；
// 上游路径、字段名、状态词表与错误语义都收在各自适配器里。
type DeviceAuth struct {
	Start func(ctx context.Context, client *http.Client, baseURL string) (DeviceAuthStart, error)
	Poll  func(ctx context.Context, client *http.Client, baseURL, deviceID, deviceSecret string) (DeviceAuthPoll, error)
}

// ErrorKind 区分「凭据失效」与「接口异常」——前者提示用户换凭据，
// 后者提示接口可能变更，两者都不清空已有数据。
type ErrorKind string

const (
	ErrKindCredential ErrorKind = "credential_invalid"
	ErrKindProvider   ErrorKind = "provider_error"
)

// FetchError 是取数失败的结构化错误。Message 必须已脱敏，可直接展示给用户。
type FetchError struct {
	Kind       ErrorKind
	Message    string
	StatusCode int
}

func (e *FetchError) Error() string { return e.Message }

// CredentialError 表示凭据失效（401/403）。
func CredentialError(message string, statusCode int) *FetchError {
	return &FetchError{Kind: ErrKindCredential, Message: message, StatusCode: statusCode}
}

// ProviderError 表示接口异常（网络、5xx、解析失败、限流等）。
func ProviderError(message string, statusCode int) *FetchError {
	return &FetchError{Kind: ErrKindProvider, Message: message, StatusCode: statusCode}
}

// specs 是供应商的固定顺序（也是前端展示顺序）。新增供应商只加这里与一个实现文件。
// DefaultBaseURL 是默认 origin，Registry 会按配置覆盖。
func specs() []Provider {
	return []Provider{
		{
			ID:             "commandcode",
			Name:           "Command Code",
			PlanHint:       "goat 套餐",
			DefaultBaseURL: "https://api.commandcode.ai",
			CredentialFields: []CredentialField{
				{Key: "api_key", Label: "Command Code API Key", Secret: true, Required: true, Placeholder: "user_...", Help: "在 Command Code 设置里创建的 API key，以 user_ 开头。"},
			},
			Instructions: []string{
				"登录 commandcode.ai，进入设置页",
				"创建或复制形如 user_... 的 API key",
			},
			Fetch: fetchCommandCode,
		},
		{
			ID:             "opencodego",
			Name:           "OpenCode Go",
			PlanHint:       "Go 订阅",
			DefaultBaseURL: "https://opencode.ai",
			CredentialFields: []CredentialField{
				{Key: "api_key", Label: "OpenCode API Key", Secret: true, Required: true, Help: "OpenCode Go 订阅对应的 API key。"},
			},
			Instructions: []string{
				"登录 opencode.ai，进入订阅 / API key 页面",
				"复制 OpenCode API key",
			},
			Fetch: fetchOpenCodeGo,
		},
		{
			ID:             "zai",
			Name:           "智谱 GLM Coding Plan",
			PlanHint:       "Coding Plan（国内 BigModel）",
			DefaultBaseURL: "https://open.bigmodel.cn",
			CredentialFields: []CredentialField{
				{Key: "api_key", Label: "智谱 API Key", Secret: true, Required: true, Help: "智谱开放平台（bigmodel.cn）的 API key。"},
			},
			Instructions: []string{
				"登录 bigmodel.cn，进入「API Key」页面",
				"创建一个 API key 并复制（国内站点，不是 z.ai）",
			},
			Fetch: fetchZai,
		},
		{
			ID:             "grok",
			Name:           "Grok",
			PlanHint:       "SuperGrok",
			DefaultBaseURL: "https://cli-chat-proxy.grok.com",
			CredentialFields: []CredentialField{
				{Key: "token", Label: "Bearer Token", Secret: true, Required: true, Placeholder: "eyJ...", Help: "grok login 后 ~/.grok/auth.json 里的 bearer token。"},
			},
			Instructions: []string{
				"在本机执行 grok login",
				"打开 ~/.grok/auth.json，复制其中的 bearer token",
			},
			Warning: "这是会话令牌，权限较大且会过期；过期后需要重新粘贴（首期不做自动刷新）。",
			Fetch:   fetchGrok,
		},
		{
			ID:             "codex",
			Name:           "Codex",
			PlanHint:       "ChatGPT Plus",
			DefaultBaseURL: "https://chatgpt.com",
			CredentialFields: []CredentialField{
				{Key: "access_token", Label: "Access Token / PAT", Secret: true, Required: true, Placeholder: "eyJ...", Help: "ChatGPT 网页会话的 OAuth access token，或带 usage 权限的 PAT。"},
				{Key: "account_id", Label: "Account ID（可选）", Help: "多账号时填 chatgpt-account-id。"},
			},
			Instructions: []string{
				"登录 chatgpt.com，打开开发者工具 Network",
				"复制 backend-api 请求里的 Authorization Bearer 值",
			},
			Warning: "会话令牌权限较大且会过期。本服务不做服务端刷新（避免使你本机 Codex CLI 的 refresh token 失效），过期后请重新粘贴。",
			Fetch:   fetchCodex,
		},
		{
			ID:             "devin",
			Name:           "Devin",
			PlanHint:       "Max 套餐",
			DefaultBaseURL: "https://app.devin.ai",
			CredentialFields: []CredentialField{
				{Key: "token", Label: "Bearer Token", Secret: true, Required: true, Help: "Usage & Limits 请求的 Authorization 值（含 Bearer 前缀也可）。"},
				{Key: "org_id", Label: "Organization ID", Required: true, Placeholder: "org-...", Help: "同一请求的 x-cog-org-id 请求头值。"},
			},
			Instructions: []string{
				"登录 app.devin.ai 并进入组织的 Usage & Limits 页面",
				"打开开发者工具 Network，找到 billing/quota/usage 请求",
				"复制 Authorization 与 x-cog-org-id 两个请求头",
			},
			Warning: "这是浏览器网页会话令牌，权限较大且会过期；过期后需要重新粘贴。",
			Fetch:   fetchDevin,
		},
		{
			ID:             "factory",
			Name:           "Factory Droid",
			PlanHint:       "Pro 套餐",
			DefaultBaseURL: "https://api.factory.ai",
			CredentialFields: []CredentialField{
				{Key: "api_key", Label: "Factory API Key", Secret: true, Required: true, Placeholder: "fk-...", Help: "在 Factory 设置页创建的 API key。"},
			},
			Instructions: []string{
				"打开 app.factory.ai/settings/api-keys",
				"创建一个 API key 并复制",
			},
			Fetch: fetchFactory,
		},
		{
			ID:             "opendesign",
			Name:           "OpenDesign",
			PlanHint:       "Design Plan",
			DefaultBaseURL: "https://amr-api.open-design.ai",
			DeviceAuth:     OpenDesignDeviceAuth(),
			CredentialFields: []CredentialField{{
				Key: "control_key", Label: "控制面密钥", Secret: true, Required: true,
				Placeholder: "vela_ctrl_…",
				Help:        "OpenDesign 控制面（amr-api.open-design.ai）的 vela_ctrl_… 密钥；网页「API Key」页给的 sk-amr-… 只能调模型，查不了额度。",
			}},
			Instructions: []string{
				"点「使用浏览器授权」，在打开的页面点批准（推荐）",
				"已有控制面密钥时可直接粘贴，它以 vela_ctrl_ 开头",
			},
			Warning:  "网页「API Key」页给的 sk-amr-… 是调模型用的密钥，查不了额度；填了会直接报错。",
			Fetch:    fetchOpenDesign,
			Validate: validateOpenDesignCredentials,
		},
	}
}

// Registry 返回全部供应商（固定顺序）；baseURLs 按供应商 id 覆盖默认 origin。
func Registry(baseURLs map[string]string) []Provider {
	providers := specs()
	for i := range providers {
		if override := baseURLs[providers[i].ID]; override != "" {
			providers[i].DefaultBaseURL = override
		}
	}
	return providers
}

// Lookup 按 id 找一个供应商；baseURLs 语义同 Registry。
func Lookup(baseURLs map[string]string, id string) (Provider, bool) {
	for _, p := range Registry(baseURLs) {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Percent 计算 used/limit 的百分比并夹到 [0,100]、四舍五入到 1 位小数。
func Percent(used, limit float64) *float64 {
	if limit <= 0 {
		return nil
	}
	return PercentPtr(ClampPercent(used / limit * 100))
}

// ClampPercent 把百分比夹到 [0,100] 并四舍五入到 1 位小数。
func ClampPercent(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return math.Round(v*10) / 10
}

// Round1 把金额等数值四舍五入到 1 位小数。
func Round1(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Round(v*10) / 10
}
