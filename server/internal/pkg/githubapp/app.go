package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AppAuth GitHub App 身份：用 App 私钥签 RS256 JWT（约 9 分钟有效）换安装令牌，
// 安装令牌按 installation_id 进程内缓存（提前 1 分钟过期），agent 侧永远接触
// 不到这些凭证。

const (
	appJWTTTL      = 9 * time.Minute
	tokenSkew      = 30 * time.Second // 签发回拨，容忍本机与 GitHub 的时钟差
	tokenRefreshIn = time.Minute      // 缓存命中条件：距过期还有 1 分钟以上
)

// AppCredentials 进程内持有的 App 身份。私钥只从 PEM 文件读进内存，
// 不进数据库、不进环境变量正文、不写日志。
type AppCredentials struct {
	AppID          string
	PrivateKeyPath string
	APIBaseURL     string // 空取官方 https://api.github.com；测试注入 httptest 用

	keyOnce sync.Once
	key     *rsa.PrivateKey
	keyErr  error
}

// loadKey 惰性读取并解析 PEM 私钥（PKCS#1 或 PKCS#8）。
func (c *AppCredentials) loadKey() (*rsa.PrivateKey, error) {
	c.keyOnce.Do(func() {
		raw, err := os.ReadFile(c.PrivateKeyPath)
		if err != nil {
			c.keyErr = fmt.Errorf("githubapp: 读取私钥失败: %w", err)
			return
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			c.keyErr = errors.New("githubapp: 私钥文件不是有效的 PEM")
			return
		}
		if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			c.key = key
			return
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			c.keyErr = fmt.Errorf("githubapp: 解析私钥失败: %w", err)
			return
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			c.keyErr = errors.New("githubapp: 私钥必须是 RSA")
			return
		}
		c.key = rsaKey
	})
	return c.key, c.keyErr
}

// appJWT 签发一次性 App JWT（iss=AppID，约 9 分钟）。
func (c *AppCredentials) appJWT(now time.Time) (string, error) {
	key, err := c.loadKey()
	if err != nil {
		return "", err
	}
	claims := jwt.RegisteredClaims{
		Issuer:    c.AppID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-tokenSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(appJWTTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

type cachedInstallToken struct {
	token     string
	expiresAt time.Time
}

// InstallationTokenStore 安装令牌缓存：一个 App 一份，goroutine 安全。
// 缓存只在进程内（令牌本身 1 小时过期，重新换取成本一次 JWT 签名 + 一次 POST）。
type InstallationTokenStore struct {
	creds *AppCredentials
	http  *httpClient

	mu    sync.Mutex
	cache map[int64]cachedInstallToken
}

func NewInstallationTokenStore(creds *AppCredentials) *InstallationTokenStore {
	return &InstallationTokenStore{
		creds: creds,
		http:  &httpClient{baseURL: apiBase(creds.APIBaseURL)},
		cache: make(map[int64]cachedInstallToken),
	}
}

func apiBase(configured string) string {
	if s := strings.TrimRight(configured, "/"); s != "" {
		return s
	}
	return "https://api.github.com"
}

// Token 返回该安装的可用令牌；缓存未命中或临过期时换新。换新请求跟调用方 ctx。
func (s *InstallationTokenStore) Token(ctx context.Context, installationID int64, now time.Time) (string, error) {
	s.mu.Lock()
	cached, ok := s.cache[installationID]
	if ok && now.Add(tokenRefreshIn).Before(cached.expiresAt) {
		s.mu.Unlock()
		return cached.token, nil
	}
	s.mu.Unlock()

	jwtTok, err := s.creds.appJWT(now)
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := s.http.doJSON(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installationID),
		jwtTok, nil, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("githubapp: 安装令牌响应缺少 token 字段")
	}

	s.mu.Lock()
	s.cache[installationID] = cachedInstallToken{token: out.Token, expiresAt: out.ExpiresAt}
	s.mu.Unlock()
	return out.Token, nil
}

// Invalidate 使某安装的缓存失效（401 时调用，避免拿着坏令牌继续用满一小时）。
func (s *InstallationTokenStore) Invalidate(installationID int64) {
	s.mu.Lock()
	delete(s.cache, installationID)
	s.mu.Unlock()
}

// GenerateAppKeyPEM 生成一对 RSA 私钥的 PEM（测试用，生产密钥由 GitHub 生成下载）。
func GenerateAppKeyPEM() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), nil
}
