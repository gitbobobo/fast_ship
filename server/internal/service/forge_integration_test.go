package service

import (
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ForgeIntegrationService（INT-166）定向测试：CRUD 三态、凭证只写不回显、
// 内置 github-default 只读映射、基址校验接线、被引用拒删、连接检测结论。

type forgeTestEnv struct {
	svc       *ForgeIntegrationService
	repo      *repository.ForgeIntegrationRepository
	cfg       *config.Config
	db        *gorm.DB
	baseCalls *[]string // validateBaseURL 收到的 raw，供「无关更新不碰网络」断言
}

func forgeTestService(t *testing.T) *forgeTestEnv {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ForgeIntegration{}, &model.Project{}, &model.User{}, &model.DeploymentSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		GitHubApp:  config.GitHubAppConfig{},
	}
	repo := repository.NewForgeIntegrationRepository(db)
	githubSvc := NewIntegrationService(repository.NewDeploymentSettingsRepository(db), cfg, zap.NewNop())
	svc := NewForgeIntegrationService(repo, githubSvc, cfg, zap.NewNop())

	// 假校验：与 forges.ValidateBaseURL 同口径的形状规则（https 放行、http 仅
	// 私网放行、userinfo/query/fragment 拒收、host 含 "private" 视为解析到
	// 私网、其余拒绝），但不做真实 DNS。
	baseCalls := &[]string{}
	svc.SetValidateBaseURL(func(_ context.Context, raw string, allowPrivateNetwork bool) (string, error) {
		*baseCalls = append(*baseCalls, raw)
		trimmed := strings.TrimSpace(raw)
		if !allowPrivateNetwork && strings.Contains(trimmed, "private") {
			return "", forges.ErrPrivateAddress
		}
		u, perr := url.Parse(trimmed)
		if perr != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return "", forges.ErrInvalidBaseURL
		}
		switch {
		case strings.HasPrefix(trimmed, "https://"):
			return strings.TrimRight(trimmed, "/"), nil
		case strings.HasPrefix(trimmed, "http://") && allowPrivateNetwork:
			return strings.TrimRight(trimmed, "/"), nil
		case strings.HasPrefix(trimmed, "http://"):
			return "", forges.ErrSchemeNotAllowed
		default:
			return "", forges.ErrPrivateAddress
		}
	})
	return &forgeTestEnv{svc: svc, repo: repo, cfg: cfg, db: db, baseCalls: baseCalls}
}

// stubProbe 注入固定检测序列并记录每次收到的 apiBase 与 token。
func stubProbe(svc *ForgeIntegrationService, probes ...ForgeProbe) *[]string {
	calls := &[]string{}
	idx := 0
	svc.SetProbe(func(ctx context.Context, client *http.Client, apiBaseURL, token string) ForgeProbe {
		*calls = append(*calls, apiBaseURL+"|"+token)
		p := probes[idx%len(probes)]
		idx++
		return p
	})
	return calls
}

func forgeCreateReq(name, base, token string) *api.CreateForgeIntegrationRequest {
	return &api.CreateForgeIntegrationRequest{Name: name, BaseUrl: base, Token: token}
}

func TestForgeIntegrationListBuiltinFirst(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	list, err := env.svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("empty deployment should still list builtin, got %+v", list)
	}
	builtin := list[0]
	if builtin.Id != model.ForgeIntegrationBuiltinGitHubID || !builtin.Builtin ||
		builtin.Kind != api.Github || builtin.BaseUrl != "https://github.com" {
		t.Fatalf("builtin projection = %+v", builtin)
	}
	if builtin.ApiBaseUrl != "https://api.github.com" {
		t.Fatalf("builtin api base = %q", builtin.ApiBaseUrl)
	}
	if builtin.CreatedAt != nil || builtin.UpdatedAt != nil {
		t.Fatal("builtin has no stored row, timestamps should be null")
	}

	// forgejo 行跟在内置后面，按创建顺序。
	if _, err := env.svc.Create(ctx, forgeCreateReq("b-second", "https://b.example.com", "tok-2")); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if _, err := env.svc.Create(ctx, forgeCreateReq("a-first", "https://a.example.com", "tok-1")); err != nil {
		t.Fatalf("create a: %v", err)
	}
	list, err = env.svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 || list[0].Id != model.ForgeIntegrationBuiltinGitHubID ||
		list[1].Name != "b-second" || list[2].Name != "a-first" {
		t.Fatalf("list order = %+v", list)
	}
	if list[1].Builtin || list[1].Kind != api.Forgejo {
		t.Fatalf("forgejo row = %+v", list[1])
	}
}

func TestForgeIntegrationCreateEncryptsAndDefaults(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	out, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name:                "内网 Forgejo",
		BaseUrl:             "http://forge.lan/",
		Token:               "pat-secret-123",
		WebhookSecret:       ptrStr("wh-456"),
		AllowPrivateNetwork: ptrBool(true),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.TokenSet != true || out.WebhookSecretSet != true || out.HasCaCert || out.Builtin {
		t.Fatalf("projection flags = %+v", out)
	}
	if out.Kind != api.Forgejo || out.BaseUrl != "http://forge.lan" {
		t.Fatalf("kind/base = %s/%s", out.Kind, out.BaseUrl)
	}
	if out.ApiBaseUrl != "http://forge.lan/api/v1" {
		t.Fatalf("api base should default to {base}/api/v1, got %q", out.ApiBaseUrl)
	}
	if !out.AllowPrivateNetwork {
		t.Fatal("allow_private_network should be true")
	}
	// 响应不含凭证本体。
	if strings.Contains(apiJSON(t, out), "pat-secret-123") {
		t.Fatal("token plaintext leaked into response")
	}

	// 落库的是密文，可用密钥解回原文。
	row, err := env.repo.FindByID(out.Id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	tokenPlain, err := crypto.Decrypt(row.TokenEncrypted, []byte(env.cfg.Encryption.Key))
	if err != nil || string(tokenPlain) != "pat-secret-123" {
		t.Fatalf("token roundtrip = %q, %v", tokenPlain, err)
	}
	whPlain, err := crypto.Decrypt(row.WebhookSecretEncrypted, []byte(env.cfg.Encryption.Key))
	if err != nil || string(whPlain) != "wh-456" {
		t.Fatalf("webhook roundtrip = %q, %v", whPlain, err)
	}
	if strings.Contains(string(row.TokenEncrypted), "pat-secret-123") {
		t.Fatal("token stored as plaintext")
	}
}

func TestForgeIntegrationCreateValidation(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *api.CreateForgeIntegrationRequest
		want int
	}{
		{"name 缺失", forgeCreateReq("", "https://a.example.com", "tok"), 40001},
		{"base 缺失", forgeCreateReq("n", "", "tok"), 40001},
		{"token 缺失", forgeCreateReq("n", "https://a.example.com", ""), 40001},
		{"http 默认拒绝", forgeCreateReq("n", "http://a.example.com", "tok"), 40001},
		{"私网域名拒绝", forgeCreateReq("n", "https://private.forge.internal", "tok"), 40001},
	}
	for _, tc := range cases {
		_, err := env.svc.Create(ctx, tc.req)
		appErr := appErr(t, err)
		if appErr.Code != tc.want {
			t.Errorf("%s: code = %d, want %d (%v)", tc.name, appErr.Code, tc.want, err)
		}
	}

	// CA PEM 不可解析拒绝。
	_, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name: "n", BaseUrl: "https://a.example.com", Token: "tok", CaCertPem: ptrStr("junk"),
	})
	if appErr(t, err).Code != 40001 {
		t.Fatalf("bad CA code = %d", appErr(t, err).Code)
	}

	// 指向 github.com 的 Forgejo 集成是死配置（parseRepoBinding 对该主机
	// 永远先归内置 github-default）——保存期直接拒绝并点明原因。
	_, err = env.svc.Create(ctx, forgeCreateReq("n", "https://github.com", "tok"))
	if aerr := appErr(t, err); aerr.Code != 40001 || !strings.Contains(aerr.Message, "GitHub 主机") {
		t.Fatalf("dead github host should be rejected with clear message, got %v", err)
	}

	// token 必填先于基址校验：缺 token 的请求不该先做 DNS/SSRF 校验再报基址。
	_, err = env.svc.Create(ctx, forgeCreateReq("n", "not-a-url", ""))
	if aerr := appErr(t, err); aerr.Code != 40001 || !strings.Contains(aerr.Message, "token") {
		t.Fatalf("missing token should report token required first, got %v", err)
	}
}

func TestForgeIntegrationUpdateThreeStates(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()
	created, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name: "forge", BaseUrl: "https://old.example.com", Token: "tok-old", WebhookSecret: ptrStr("wh-old"),
		CaCertPem: ptrStr(validCAPem(t)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rowAtCreate, _ := env.repo.FindByID(created.Id)
	callsAfterCreate := len(*env.baseCalls)

	// 全省略：什么都不动，也不碰基址校验。
	out, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{})
	if err != nil {
		t.Fatalf("update noop: %v", err)
	}
	if out.BaseUrl != "https://old.example.com" || !out.HasCaCert {
		t.Fatalf("noop update = %+v", out)
	}
	if len(*env.baseCalls) != callsAfterCreate {
		t.Fatal("noop update should not revalidate URLs")
	}
	row, _ := env.repo.FindByID(created.Id)
	if !bytesEqual(row.TokenEncrypted, rowAtCreate.TokenEncrypted) {
		t.Fatal("omitted token must keep old ciphertext")
	}
	if !bytesEqual(row.WebhookSecretEncrypted, rowAtCreate.WebhookSecretEncrypted) {
		t.Fatal("omitted webhook secret must keep old ciphertext")
	}

	// 显式空串：token / webhook_secret 保留旧值；ca_cert_pem 清空。
	_, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		Token: ptrStr(""), WebhookSecret: ptrStr(""), CaCertPem: ptrStr(""),
	})
	if err != nil {
		t.Fatalf("update empty strings: %v", err)
	}
	row, _ = env.repo.FindByID(created.Id)
	if !bytesEqual(row.TokenEncrypted, rowAtCreate.TokenEncrypted) ||
		!bytesEqual(row.WebhookSecretEncrypted, rowAtCreate.WebhookSecretEncrypted) {
		t.Fatal("empty string secrets must keep old ciphertext")
	}
	if row.CACertPEM != "" {
		t.Fatal("empty ca_cert_pem should clear")
	}

	// 换 token：密文更新且可解回新值。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{Token: ptrStr("tok-new")}); err != nil {
		t.Fatalf("update token: %v", err)
	}
	row, _ = env.repo.FindByID(created.Id)
	plain, err := crypto.Decrypt(row.TokenEncrypted, []byte(env.cfg.Encryption.Key))
	if err != nil || string(plain) != "tok-new" {
		t.Fatalf("new token roundtrip = %q, %v", plain, err)
	}

	// name 不允许清空。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{Name: ptrStr(" ")}); appErr(t, err).Code != 40001 {
		t.Fatalf("empty name code = %d", appErr(t, err).Code)
	}

	// 基址变更重新校验，且 http 基址只在私网放行开启时可用。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("http://forge.lan"), AllowPrivateNetwork: ptrBool(true),
	}); err != nil {
		t.Fatalf("update base with private flag: %v", err)
	}
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		AllowPrivateNetwork: ptrBool(false),
	}); appErr(t, err).Code != 40001 {
		// 关闭放行后，http 基址按公网口径复核必须失败，且行保持原值。
		t.Fatalf("flag-off revalidation code = %d", appErr(t, err).Code)
	} else if aerr := appErr(t, err); !strings.Contains(aerr.Message, "站点地址改用公网 https") {
		// 单关开关撞上基址校验时，消息要点明操作顺序而不是只有校验原文。
		t.Fatalf("flag-off message should hint base_url order, got %q", aerr.Message)
	}
	row, _ = env.repo.FindByID(created.Id)
	if !row.AllowPrivateNetwork {
		t.Fatal("failed validation must not flip the stored flag")
	}

	// base 先换成 https，让后面的复核失败落在钉死的 api_base_url 字段上
	// （resolveURLs 先校 base——两边都违规时提示指向 base 是对的）。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://forge.lan"),
	}); err != nil {
		t.Fatalf("update base to https: %v", err)
	}
	// api_base_url 三态：自定义值写入；显式空串重置为 {base_url}/api/v1。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		ApiBaseUrl: ptrStr("http://forge.lan/custom/api"),
	}); err != nil {
		t.Fatalf("update custom api base: %v", err)
	}
	// 钉死的 api_base_url 撞复核时提示要指向 API 基址，别把管理员引向
	// 只改 base_url（R28 P2：按失败字段分开措辞）。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		AllowPrivateNetwork: ptrBool(false),
	}); appErr(t, err).Code != 40001 {
		t.Fatalf("flag-off on pinned api code = %d", appErr(t, err).Code)
	} else if aerr := appErr(t, err); !strings.Contains(aerr.Message, "API 基址") {
		t.Fatalf("flag-off message should point at api_base_url, got %q", aerr.Message)
	}
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		ApiBaseUrl: ptrStr(""),
	})
	if err != nil {
		t.Fatalf("reset api base: %v", err)
	}
	if out.ApiBaseUrl != "https://forge.lan/api/v1" {
		t.Fatalf("empty api_base_url should reset to derived default, got %q", out.ApiBaseUrl)
	}
}

// 只改 base_url 不动 api_base_url 时：存量 api 若是旧 base 的推导值
// ({old}/api/v1) 必须跟随新 base——落库的是意图（空=推导），改 base 后
// 生效值自然指向新主机，不跟随会把 PAT 发往旧主机；管理员显式钉死的 api
// 则保留不跟随。
func TestForgeIntegrationUpdateDerivedAPIBaseFollows(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()
	created, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name: "forge", BaseUrl: "https://a.example.com", Token: "tok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ApiBaseUrl != "https://a.example.com/api/v1" {
		t.Fatalf("create api derived = %q", created.ApiBaseUrl)
	}
	// 存储意图：推导行的 api_base_url 列存空串，响应投影展开生效值。
	row, _ := env.repo.FindByID(created.Id)
	if row.APIBaseURL != "" {
		t.Fatalf("derived api should store empty intent, got %q", row.APIBaseURL)
	}

	// 推导值跟随：只改 base，api 未显式变更 → 生效值落到新 base 的推导值。
	out, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://b.example.com"),
	})
	if err != nil {
		t.Fatalf("update base: %v", err)
	}
	if out.ApiBaseUrl != "https://b.example.com/api/v1" {
		t.Fatalf("derived api should follow new base, got %q", out.ApiBaseUrl)
	}

	// 回显守卫：客户端把生效值原样提交（编辑框预填）不等于显式钉死——
	// 存储仍是推导意图，继续跟随 base。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl:    ptrStr("https://d.example.com"),
		ApiBaseUrl: ptrStr("https://d.example.com/api/v1"),
	}); err != nil {
		t.Fatalf("echo effective api: %v", err)
	}
	row, _ = env.repo.FindByID(created.Id)
	if row.APIBaseURL != "" {
		t.Fatalf("echo of derived effective must stay derived, stored %q", row.APIBaseURL)
	}

	// 显式钉死：自定义 api 后再改 base，api 保留显式值（列存展开值）。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		ApiBaseUrl: ptrStr("https://api.custom.example.com/v1"),
	}); err != nil {
		t.Fatalf("pin custom api: %v", err)
	}
	row, _ = env.repo.FindByID(created.Id)
	if row.APIBaseURL != "https://api.custom.example.com/v1" {
		t.Fatalf("explicit api should store expanded value, got %q", row.APIBaseURL)
	}
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://c.example.com"),
	})
	if err != nil {
		t.Fatalf("update base again: %v", err)
	}
	if out.ApiBaseUrl != "https://api.custom.example.com/v1" {
		t.Fatalf("explicit api must not follow, got %q", out.ApiBaseUrl)
	}

	// 显式空串解除钉死回到推导。
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		ApiBaseUrl: ptrStr(""),
	})
	if err != nil {
		t.Fatalf("unpin api: %v", err)
	}
	if out.ApiBaseUrl != "https://c.example.com/api/v1" {
		t.Fatalf("unpinned api should derive from current base, got %q", out.ApiBaseUrl)
	}
	row, _ = env.repo.FindByID(created.Id)
	if row.APIBaseURL != "" {
		t.Fatalf("unpinned api should store empty intent, got %q", row.APIBaseURL)
	}

	// 旧 base 展开值回显：GET→改 base→PUT 的调用方回写的是「旧生效值」，
	// 只比新推导值会把它钉死在旧主机上——必须同样判回显保持推导。
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl:    ptrStr("https://e.example.com"),
		ApiBaseUrl: ptrStr("https://c.example.com/api/v1"), // 上一响应的展开值
	})
	if err != nil {
		t.Fatalf("echo old-base effective api: %v", err)
	}
	if out.ApiBaseUrl != "https://e.example.com/api/v1" {
		t.Fatalf("old-base echo must stay derived and follow new base, got %q", out.ApiBaseUrl)
	}
	row, _ = env.repo.FindByID(created.Id)
	if row.APIBaseURL != "" {
		t.Fatalf("old-base echo must store empty intent, got %q", row.APIBaseURL)
	}

	// 归一化变体回显：客户端对展开值做过自己的归一（显式默认端口、尾
	// 斜杠），按 URLKey 等价键仍判回显保持推导，不误钉死。
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl:    ptrStr("https://f.example.com"),
		ApiBaseUrl: ptrStr("https://e.example.com:443/api/v1/"),
	})
	if err != nil {
		t.Fatalf("normalized-variant echo api: %v", err)
	}
	if out.ApiBaseUrl != "https://f.example.com/api/v1" {
		t.Fatalf("variant echo must stay derived and follow new base, got %q", out.ApiBaseUrl)
	}
	row, _ = env.repo.FindByID(created.Id)
	if row.APIBaseURL != "" {
		t.Fatalf("variant echo must store empty intent, got %q", row.APIBaseURL)
	}

	// 回显不碰网络：命中新旧推导值的提交在 validateBaseURL（含保存时
	// DNS 校验）之前短路——回显值主机已下线（base 迁移正是旧主机退役后
	// 才改 base_url）时「保持推导」的更新不该被 40001 挡。断言 apiInput
	// 根本没进校验器（baseCalls 只新增 base 一条）。
	callsBefore := len(*env.baseCalls)
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl:    ptrStr("https://g.example.com"),
		ApiBaseUrl: ptrStr("https://f.example.com/api/v1"), // 旧 base 的展开值
	})
	if err != nil {
		t.Fatalf("echo must not be blocked by dns validation: %v", err)
	}
	if out.ApiBaseUrl != "https://g.example.com/api/v1" {
		t.Fatalf("echo must stay derived and follow new base, got %q", out.ApiBaseUrl)
	}
	for _, c := range (*env.baseCalls)[callsBefore:] {
		if strings.Contains(c, "/api/") {
			t.Fatalf("echoed api must not reach validateBaseURL, calls %v", (*env.baseCalls)[callsBefore:])
		}
	}

	// 「推导值+多余成分」不算回显：query/fragment/userinfo/尾裸 ? 形状不
	// 合法就不短路，落回完整校验报 40001——显式输入不能被抹平成推导
	// 静默放行。
	for _, bad := range []string{
		"https://g.example.com/api/v1?x=1",
		"https://u:p@g.example.com/api/v1",
		"https://g.example.com/api/v1#f",
		"https://g.example.com/api/v1?",
	} {
		if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
			ApiBaseUrl: ptrStr(bad),
		}); err == nil {
			t.Fatalf("api_base_url %q should be rejected, not echoed to derived", bad)
		}
	}
	row, _ = env.repo.FindByID(created.Id)
	if row.APIBaseURL != "" {
		t.Fatalf("bad-shape submissions must not alter stored intent, got %q", row.APIBaseURL)
	}
}

// TestForgeIntegrationUpdateStaleBaseNotChained 覆盖存量 base 失效场景：
// 实例 DNS 失效/迁移期间，「只改 api_base_url」「只开内网放行」的无关
// 字段更新不该被 base 的保存时复验连坐 40001——管理员正是靠这两条路径
// 自救。收窄开关（公网口径复核）与 base 变更仍必须全量校验。创建后把
// 校验器换成对旧主机必败的版本，模拟 DNS 失效。
func TestForgeIntegrationUpdateStaleBaseNotChained(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()
	created, err := env.svc.Create(ctx, &api.CreateForgeIntegrationRequest{
		Name: "forge", BaseUrl: "https://stale.example.com", Token: "tok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	env.svc.SetValidateBaseURL(func(_ context.Context, raw string, allowPrivate bool) (string, error) {
		*env.baseCalls = append(*env.baseCalls, raw)
		trimmed := strings.TrimSpace(raw)
		if strings.Contains(trimmed, "stale") {
			return "", forges.ErrPrivateAddress
		}
		switch {
		case strings.HasPrefix(trimmed, "https://"):
			return strings.TrimRight(trimmed, "/"), nil
		case strings.HasPrefix(trimmed, "http://") && allowPrivate:
			return strings.TrimRight(trimmed, "/"), nil
		default:
			return "", forges.ErrSchemeNotAllowed
		}
	})

	// 只改 api_base_url：base 不碰校验器，更新放行——把 API 端点钉到
	// 可达主机正是 base 失效时的自救路径。
	out, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		ApiBaseUrl: ptrStr("https://api.example.com/v1"),
	})
	if err != nil {
		t.Fatalf("api-only update must not revalidate unchanged base: %v", err)
	}
	if out.ApiBaseUrl != "https://api.example.com/v1" {
		t.Fatalf("api pin lost, got %q", out.ApiBaseUrl)
	}

	// 只开内网放行（放宽口径，存量 base 依然合法）：同样不该复验。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		AllowPrivateNetwork: ptrBool(true),
	}); err != nil {
		t.Fatalf("enable private-network flag must not revalidate unchanged base: %v", err)
	}

	// 收窄开关（关闭内网放行）必须复核存量 base 的公网口径——失效
	// 主机在这里仍然拦得住。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		AllowPrivateNetwork: ptrBool(false),
	}); err == nil {
		t.Fatal("disabling private-network on a stale base must be rejected")
	}

	// base 变更本身仍全量校验：改成另一失效主机被拒，改成可达主机放行
	// （后者也是另一条自救路径）。
	if _, err := env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://stale2.example.com"),
	}); err == nil {
		t.Fatal("changing base to a stale host must be rejected")
	}
	out, err = env.svc.Update(ctx, created.Id, &api.UpdateForgeIntegrationRequest{
		BaseUrl: ptrStr("https://migrated.example.com"),
	})
	if err != nil {
		t.Fatalf("changing base to a reachable host must succeed: %v", err)
	}
	if out.BaseUrl != "https://migrated.example.com" {
		t.Fatalf("base migration lost, got %q", out.BaseUrl)
	}
}

func TestForgeIntegrationDeleteRules(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	// 内置不可删。
	if err := env.svc.Delete(ctx, model.ForgeIntegrationBuiltinGitHubID); appErr(t, err).Code != 40001 {
		t.Fatalf("delete builtin code = %d", appErr(t, err).Code)
	}

	created, err := env.svc.Create(ctx, forgeCreateReq("forge", "https://f.example.com", "tok"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 被项目引用：40935 且消息带项目名。
	proj := model.Project{ID: uuid.NewString(), UserID: uuid.NewString(), Name: "引用项目A", CodeIntegrationID: created.Id}
	if err := env.db.Create(&proj).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	err = env.svc.Delete(ctx, created.Id)
	delErr := appErr(t, err)
	if delErr.Code != 40935 {
		t.Fatalf("referenced delete code = %d (%v)", delErr.Code, err)
	}
	if !strings.Contains(delErr.Message, "引用项目A") {
		t.Fatalf("message should list project name: %s", delErr.Message)
	}

	// 解除引用后可删。
	if err := env.db.Model(&model.Project{}).Where("id = ?", proj.ID).
		Update("code_integration_id", "").Error; err != nil {
		t.Fatalf("clear ref: %v", err)
	}
	if err := env.svc.Delete(ctx, created.Id); err != nil {
		t.Fatalf("delete after unref: %v", err)
	}
	if _, err := env.repo.FindByID(created.Id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("row should be gone, err = %v", err)
	}

	// 不存在的集成 40435。
	if err := env.svc.Delete(ctx, uuid.NewString()); appErr(t, err).Code != 40435 {
		t.Fatalf("missing delete code = %d", appErr(t, err).Code)
	}
}

func TestForgeIntegrationTestEndpoint(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	// 内置集成引导回既有端点。
	_, err := env.svc.Test(ctx, model.ForgeIntegrationBuiltinGitHubID)
	if appErr(t, err).Code != 40001 || !strings.Contains(appErr(t, err).Message, "/deployment/github-app/test") {
		t.Fatalf("builtin test err = %v", err)
	}

	created, err := env.svc.Create(ctx, forgeCreateReq("forge", "https://f.example.com", "tok-abc"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	calls := stubProbe(env.svc, ForgeProbe{
		Status: model.DeploymentDetectionOK, Version: "1.22.0", Username: "fast-ship-bot",
	})
	out, err := env.svc.Test(ctx, created.Id)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if out.Status != api.ForgeIntegrationTestStatusOk || out.Version != "1.22.0" || out.Username != "fast-ship-bot" {
		t.Fatalf("test result = %+v", out)
	}
	if len(*calls) != 1 || (*calls)[0] != "https://f.example.com/api/v1|tok-abc" {
		t.Fatalf("probe calls = %v", *calls)
	}

	// 检测失败（invalid）随响应返回，不是 500。
	stubProbe(env.svc, ForgeProbe{Status: model.DeploymentDetectionInvalid, Detail: "token 被实例拒绝（HTTP 401）"})
	out, err = env.svc.Test(ctx, created.Id)
	if err != nil {
		t.Fatalf("invalid test should still be 200: %v", err)
	}
	if out.Status != api.ForgeIntegrationTestStatusInvalid || out.Detail == "" {
		t.Fatalf("invalid result = %+v", out)
	}

	if _, err := env.svc.Test(ctx, uuid.NewString()); appErr(t, err).Code != 40435 {
		t.Fatalf("missing test code = %d", appErr(t, err).Code)
	}
}

// TestForgeIntegrationGet 单个读取：内置走只读映射，forgejo 行走投影。
func TestForgeIntegrationGet(t *testing.T) {
	env := forgeTestService(t)
	ctx := context.Background()

	got, err := env.svc.Get(ctx, model.ForgeIntegrationBuiltinGitHubID)
	if err != nil {
		t.Fatalf("get builtin: %v", err)
	}
	if !got.Builtin {
		t.Fatalf("builtin get = %+v", got)
	}
	// 部署未配 App 身份时 token_set=false。
	if got.TokenSet {
		t.Fatal("no App configured, token_set should be false")
	}

	created, err := env.svc.Create(ctx, forgeCreateReq("forge", "https://f.example.com", "tok"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err = env.svc.Get(ctx, created.Id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Id != created.Id || !got.TokenSet || got.UpdatedAt == nil {
		t.Fatalf("forgejo get = %+v", got)
	}
	if _, err := env.svc.Get(ctx, uuid.NewString()); appErr(t, err).Code != 40435 {
		t.Fatalf("missing get code = %d", appErr(t, err).Code)
	}
}

// TestForgeProjectResponseIntegrationRefs 项目响应带出集成引用：空列 null，
// 有值非 null。
func TestForgeProjectResponseIntegrationRefs(t *testing.T) {
	empty := model.Project{ID: uuid.NewString(), UserID: uuid.NewString(), Name: "p1"}
	resp, err := (&ProjectService{}).toResponse(&empty, nil)
	if err != nil {
		t.Fatalf("toResponse: %v", err)
	}
	if resp.CodeIntegrationId != nil || resp.PublicIntegrationId != nil {
		t.Fatalf("empty refs should be nil, got %+v", resp)
	}

	refd := model.Project{ID: uuid.NewString(), UserID: uuid.NewString(), Name: "p2",
		CodeIntegrationID: "int-1", PublicIntegrationID: "int-2"}
	resp, err = (&ProjectService{}).toResponse(&refd, nil)
	if err != nil {
		t.Fatalf("toResponse: %v", err)
	}
	if resp.CodeIntegrationId == nil || *resp.CodeIntegrationId != "int-1" ||
		resp.PublicIntegrationId == nil || *resp.PublicIntegrationId != "int-2" {
		t.Fatalf("refs = %+v", resp)
	}
}

// TestForgeIntegrationRealValidatorScheme 构造器缺省接 forges.ValidateBaseURL：
// http 基址被拒、公网 IP 字面量可过（不做真实 DNS）。
func TestForgeIntegrationRealValidatorScheme(t *testing.T) {
	env := forgeTestService(t)
	env.svc.validateBaseURL = forges.ValidateBaseURL
	ctx := context.Background()

	if _, err := env.svc.Create(ctx, forgeCreateReq("n", "http://f.example.com", "tok")); appErr(t, err).Code != 40001 {
		t.Fatalf("http base should be rejected by real validator: %v", err)
	}
	out, err := env.svc.Create(ctx, forgeCreateReq("n", "https://192.0.2.10:8443/", "tok"))
	if err != nil {
		t.Fatalf("public ip literal should pass: %v", err)
	}
	if out.BaseUrl != "https://192.0.2.10:8443" {
		t.Fatalf("normalization = %q", out.BaseUrl)
	}
}

// forgeAPIGet 的同主机重定向：跳出 API 路径前缀的跳转必须剥掉 PAT——集成端
// 把 /api/v1/* 指到记录请求头的同主机端点时，凭证不落进那边的访问日志；
// 仍在 API 树内的跳转保留 Authorization（反代正规化路径属正常跳转）。
func TestForgeAPIGetRedirectStripsAuthOutsideAPIPrefix(t *testing.T) {
	// 凭证作用域政策已在 pkg/forges 的 client 层（Config.AuthPathPrefix），
	// 端到端跳转剥头用例见 forges.TestClientRedirectStripsAuthOutsidePrefix；
	// 这里只断言 forgeAPIGet 不再自带第二套政策——client 是什么政策就用什么。
	var sawAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.URL.Path+"|"+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	status, _, err := forgeAPIGet(context.Background(), srv.Client(), srv.URL+"/api/v1/version", "secret-pat")
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if len(sawAuth) != 1 || !strings.HasSuffix(sawAuth[0], "token secret-pat") {
		t.Fatalf("requests = %v", sawAuth)
	}
}

// 安全策略拦截的探测错误透出具体哨兵（含被 %w 包进拨号错误的形态），
// 与真实断网文案区分——管理员该改配置/跳转目标而不是查网络。
func TestProbeErrDetailPolicyVsNetwork(t *testing.T) {
	for _, sentinel := range []error{
		forges.ErrPrivateAddress, forges.ErrCrossHostRedirect,
		forges.ErrSchemeNotAllowed, forges.ErrSchemeDowngrade,
	} {
		detail := probeErrDetail(fmt.Errorf("dial: %w", sentinel))
		if !strings.Contains(detail, "安全策略") || !strings.Contains(detail, sentinel.Error()) {
			t.Fatalf("policy detail = %q (sentinel %v)", detail, sentinel)
		}
	}
	if detail := probeErrDetail(errors.New("connection refused")); detail != "网络错误，未能连接 Forgejo" {
		t.Fatalf("network detail = %q", detail)
	}
}

// 仓库只读 PAT（无 read:user scope）在 GET /user 拿 403：已认证而非被拒，
// 探测应判 ok 并说明缺 scope，不误报 invalid。
func TestProbeForgeUserScopeForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"1.22.0"}`))
		case "/api/v1/user":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	probe := probeForge(context.Background(), srv.Client(), srv.URL+"/api/v1", "pat")
	if probe.Status != model.DeploymentDetectionOK || probe.Username != "" ||
		!strings.Contains(probe.Detail, "read:user") {
		t.Fatalf("probe = %+v", probe)
	}
}

// 实例返回的超长 login 与 version 同口径截断（64 rune）——异常/恶意实例
// 的探测字段不能不受控地进响应。
func TestProbeForgeLongFieldsTruncated(t *testing.T) {
	longLogin := strings.Repeat("超", 200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"` + strings.Repeat("9", 200) + `"}`))
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"` + longLogin + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	probe := probeForge(context.Background(), srv.Client(), srv.URL+"/api/v1", "pat")
	if probe.Status != model.DeploymentDetectionOK {
		t.Fatalf("probe = %+v", probe)
	}
	if got := len([]rune(probe.Username)); got != 64 {
		t.Fatalf("username should truncate to 64 runes, got %d", got)
	}
	if got := len([]rune(probe.Version)); got != 64 {
		t.Fatalf("version should truncate to 64 runes, got %d", got)
	}
}

// /version 与 /user 双双 403：可能是 WAF 按来源拦或 token 无 API 读权限，
// 判 failed 而不是「连接正常」。
func TestProbeForgeBothForbiddenFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	probe := probeForge(context.Background(), srv.Client(), srv.URL+"/api/v1", "pat")
	if probe.Status != model.DeploymentDetectionFailed ||
		!strings.Contains(probe.Detail, "403") {
		t.Fatalf("both-403 probe = %+v", probe)
	}
}

// ---- 小工具 ----

func ptrStr(s string) *string { return &s }

func ptrBool(b bool) *bool { return &b }

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func apiJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// validCAPem 生成一个自签 CA 的 PEM（内存即弃，仅供解析校验）。
func validCAPem(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(crand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
