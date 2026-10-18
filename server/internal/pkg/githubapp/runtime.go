package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// RuntimeConfig 当前生效的 GitHub App 明文身份：集成页保存的数据库配置解密
// 结果，或 config.yaml 兜底。只在内存短暂存在，不写日志。
type RuntimeConfig struct {
	AppID         string
	PrivateKeyPEM []byte
}

// UserConfigSource 按用户每次调用提供当前生效身份；ok=false 表示该用户未配置。
// 集成页保存后下一次取值即生效，无需重启。
type UserConfigSource func(ctx context.Context, userID string) (RuntimeConfig, bool, error)

// Clients 按用户解析 githubapi.GitHubClient（INT-119 用户级集成）：ClientFor
// 向 source 现取该用户身份，按（用户, AppID+PEM 指纹）缓存安装令牌仓库与共享
// REST 客户端；同一用户指纹变化后下一次调用自动重建，不同用户的 client 互不
// 共享。未配置时返回 50000「GitHub App 未配置」。
type Clients struct {
	source UserConfigSource
	// apiBase 安装令牌换新与 REST 共用的 API base（空取官方）；测试注入 httptest 用。
	apiBase string

	mu    sync.Mutex
	cache map[string]clientsEntry
}

type clientsEntry struct {
	fingerprint string
	client      githubapi.GitHubClient
}

func NewClients(source UserConfigSource) *Clients {
	return &Clients{source: source, cache: map[string]clientsEntry{}}
}

// ClientFor 返回该用户当前生效的客户端；同一用户同一配置复用缓存实例。
func (c *Clients) ClientFor(ctx context.Context, userID string) (githubapi.GitHubClient, error) {
	cfgApp, ok, err := c.source(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok || cfgApp.AppID == "" || len(cfgApp.PrivateKeyPEM) == 0 {
		return nil, errs.New(errs.ErrInternal.Code, "GitHub App 未配置")
	}
	sum := sha256.Sum256(append([]byte(cfgApp.AppID+"\n"), cfgApp.PrivateKeyPEM...))
	fp := hex.EncodeToString(sum[:])

	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[userID]
	if !ok || entry.client == nil || fp != entry.fingerprint {
		creds := &AppCredentials{AppID: cfgApp.AppID, PrivateKeyPEM: cfgApp.PrivateKeyPEM, APIBaseURL: c.apiBase}
		entry = clientsEntry{fingerprint: fp, client: NewClient(NewInstallationTokenStore(creds))}
		c.cache[userID] = entry
	}
	return entry.client, nil
}

// StaticClients 恒返回同一 GitHubClient 的解析器：fake 自测路径（debug 模式
// 不读数据库，身份与密钥全走 config）与测试注入用。
type StaticClients struct {
	Client githubapi.GitHubClient
}

func (s StaticClients) ClientFor(context.Context, string) (githubapi.GitHubClient, error) {
	return s.Client, nil
}

// Detection 一次连接检测的结论：status 为 ok / invalid / failed，detail 是给人
// 看的固定文案（不拼底层错误串，不含任何密钥）。
type Detection struct {
	Status string
	Detail string
}

const (
	DetectionOK      = "ok"
	DetectionInvalid = "invalid"
	DetectionFailed  = "failed"

	// detectionTimeout 检测请求的兜底超时（设置页同步等待，不能挂太久）。
	detectionTimeout = 15 * time.Second
)

// Verify 用 App 私钥签 JWT 调 GET {apiBase}/app 做只读连接检测：私钥解析失败
// 与 401 是 invalid（凭证问题）；其他状态码与网络错误是 failed。
func Verify(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) Detection {
	key, err := ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return Detection{Status: DetectionInvalid, Detail: "私钥不是有效的 RSA PEM"}
	}
	jwtTok, err := signAppJWT(appID, key, time.Now())
	if err != nil {
		return Detection{Status: DetectionInvalid, Detail: "私钥不是有效的 RSA PEM"}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase(apiBaseURL)+"/app", nil)
	if err != nil {
		return Detection{Status: DetectionFailed, Detail: "网络错误，未能连接 GitHub"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwtTok)

	client := &http.Client{Timeout: detectionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return Detection{Status: DetectionFailed, Detail: "网络错误，未能连接 GitHub"}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var app struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err == nil {
			_ = json.Unmarshal(raw, &app)
		}
		label := app.Name
		if label == "" {
			label = app.Slug
		}
		if label != "" {
			return Detection{Status: DetectionOK, Detail: "已连接 GitHub App：" + label}
		}
		return Detection{Status: DetectionOK, Detail: "已连接 GitHub App"}
	case resp.StatusCode == http.StatusUnauthorized:
		return Detection{Status: DetectionInvalid, Detail: "GitHub 拒绝该 App 身份（401）"}
	default:
		return Detection{Status: DetectionFailed, Detail: fmt.Sprintf("检测失败（HTTP %d）", resp.StatusCode)}
	}
}
