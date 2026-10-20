package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/catalog"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func newProviderTestService(t *testing.T, env *botTestEnv) *AIProviderService {
	t.Helper()
	return NewAIProviderService(env.db, env.cfg, zap.NewNop())
}

func TestAIProviderCRUDIsolation(t *testing.T) {
	env := setupBotTestEnv(t)
	svc := newProviderTestService(t, env)
	alice := createBotTestUser(t, env.db, "pa")
	bob := createBotTestUser(t, env.db, "pb")

	created, err := svc.Create(alice, &CreateAIProviderRequest{
		CatalogID: "custom", Name: "内网网关", BaseURL: "https://gw.internal.example.com/v1/", APIKey: "sk-alice-1111",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 地址规范化去尾 /。
	if created.BaseURL != "https://gw.internal.example.com/v1" {
		t.Fatalf("base_url 应规范化: %q", created.BaseURL)
	}
	if created.APIKeyLast4 != "1111" || created.BotCount != 0 {
		t.Fatalf("unexpected response: %+v", created)
	}
	if strings.Contains(botJSON(created), "sk-alice-1111") {
		t.Fatalf("响应泄露明文 key")
	}

	// 目录提供商：地址由目录决定，请求里的 base_url 忽略；name 缺省取目录名。
	fromCatalog, err := svc.Create(bob, &CreateAIProviderRequest{
		CatalogID: "deepseek", BaseURL: "https://ignored.example.com", APIKey: "sk-bob-2222",
	})
	if err != nil {
		t.Fatalf("create catalog provider: %v", err)
	}
	api, _ := catalog.ResolveBaseURL("deepseek")
	if fromCatalog.BaseURL != catalog.NormalizeBaseURL(api) {
		t.Fatalf("目录 provider 地址应由目录决定: %q", fromCatalog.BaseURL)
	}
	if fromCatalog.Name != "DeepSeek" || fromCatalog.CatalogID != "deepseek" {
		t.Fatalf("目录 provider 名称与归属: %+v", fromCatalog)
	}

	// 校验失败面：缺 catalog_id / 缺 key / 空白 key / custom 缺地址 / 未知目录。
	for i, tc := range []struct {
		req CreateAIProviderRequest
	}{
		{CreateAIProviderRequest{CatalogID: "", APIKey: "sk-x"}},
		{CreateAIProviderRequest{CatalogID: "custom", BaseURL: "https://x.example.com", APIKey: "  "}},
		{CreateAIProviderRequest{CatalogID: "custom", APIKey: "sk-x"}},
		{CreateAIProviderRequest{CatalogID: "custom", BaseURL: "ftp://x.example.com", APIKey: "sk-x"}},
		{CreateAIProviderRequest{CatalogID: "no-such-catalog", APIKey: "sk-x"}},
	} {
		if _, err := svc.Create(alice, &tc.req); err == nil {
			t.Fatalf("用例 %d 应拒绝: %+v", i, tc.req)
		}
	}

	// 隔离：bob 读不到/改不了/删不了 alice 的 provider。
	if _, err := svc.Get(bob, created.ID); err != errs.ErrAIProviderNotFound {
		t.Fatalf("bob get 应 40433: %v", err)
	}
	if _, err := svc.Update(bob, created.ID, &UpdateAIProviderRequest{Name: strPtrBot("hijack")}); err == nil {
		t.Fatalf("bob update 应拒绝")
	}
	if err := svc.Delete(bob, created.ID); err != errs.ErrAIProviderNotFound {
		t.Fatalf("bob delete 应 40433: %v", err)
	}

	// 更新：目录 provider 的 base_url 由目录决定（请求忽略）；custom 可改地址；
	// catalog_id 不可改（没有字段可传）。key 轮换只回新末四位。
	if _, err := svc.Update(alice, created.ID, &UpdateAIProviderRequest{Name: strPtrBot("网关 2"), BaseURL: strPtrBot("https://gw2.internal.example.com/v1")}); err != nil {
		t.Fatalf("update custom: %v", err)
	}
	rotated, err := svc.Update(alice, created.ID, &UpdateAIProviderRequest{APIKey: strPtrBot("sk-rotate-3333")})
	if err != nil || rotated.APIKeyLast4 != "3333" {
		t.Fatalf("rotate: %v %+v", err, rotated)
	}
	got, err := svc.Get(alice, created.ID)
	if err != nil || got.Name != "网关 2" || got.BaseURL != "https://gw2.internal.example.com/v1" {
		t.Fatalf("get after update: %v %+v", err, got)
	}
	catUpdated, err := svc.Update(bob, fromCatalog.ID, &UpdateAIProviderRequest{BaseURL: strPtrBot("https://should-be-ignored.example.com")})
	if err != nil {
		t.Fatalf("update catalog provider: %v", err)
	}
	if catUpdated.BaseURL != catalog.NormalizeBaseURL(api) {
		t.Fatalf("目录 provider 的 base_url 不得被改: %q", catUpdated.BaseURL)
	}

	// 列表按用户隔离。
	aliceItems, err := svc.List(alice)
	if err != nil || len(aliceItems) != 1 {
		t.Fatalf("alice list: %v %d", err, len(aliceItems))
	}
	bobItems, err := svc.List(bob)
	if err != nil || len(bobItems) != 1 {
		t.Fatalf("bob list: %v %d", err, len(bobItems))
	}
}

func TestAIProviderDeleteInUseConflict(t *testing.T) {
	env := setupBotTestEnv(t)
	providerSvc := newProviderTestService(t, env)
	userID := createBotTestUser(t, env.db, "pinuse")

	providerID := env.createProvider(t, userID, "sk-use-9999")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "引用者", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	// 被引用时 40933，且 provider 仍在。
	if err := providerSvc.Delete(userID, providerID); err != errs.ErrAIProviderInUse {
		t.Fatalf("被引用删除应 40933: %v", err)
	}
	if _, err := providerSvc.Get(userID, providerID); err != nil {
		t.Fatalf("provider 应仍在: %v", err)
	}

	// bot_count 反映引用数。
	got, err := providerSvc.Get(userID, providerID)
	if err != nil || got.BotCount != 1 {
		t.Fatalf("bot_count 应为 1: %v %+v", err, got)
	}

	// 换到另一 provider 后原 provider 可删；删除后再读 404。
	other := env.createProvider(t, userID, "sk-use-0001")
	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{ProviderID: &other}); err != nil {
		t.Fatalf("switch provider: %v", err)
	}
	if err := providerSvc.Delete(userID, providerID); err != nil {
		t.Fatalf("解除引用后应可删除: %v", err)
	}
	if _, err := providerSvc.Get(userID, providerID); err != errs.ErrAIProviderNotFound {
		t.Fatalf("删除后应 40433: %v", err)
	}
	// bot 仍可读，派生字段来自现存的新 provider。
	fresh, err := env.bots.Get(userID, bot.ID)
	if err != nil || fresh.ProviderID != other {
		t.Fatalf("bot 应指向新 provider: %v %+v", err, fresh)
	}
}

// provider 创建后必须能被同 user 的多个 bot 引用；轮换密钥后所有引用它的
// 机器人在下一次读取生效（密钥只在 provider 行存一份）。
func TestAIProviderSharedByBotsAndKeyRotation(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "pshared")

	providerID := env.createProvider(t, userID, "sk-share-4444")
	first, err := env.bots.Create(userID, &CreateBotRequest{Name: "一", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot 1: %v", err)
	}
	second, err := env.bots.Create(userID, &CreateBotRequest{Name: "二", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot 2: %v", err)
	}

	if first.BaseURL != second.BaseURL || first.APIKeyLast4 != "4444" || second.APIKeyLast4 != "4444" {
		t.Fatalf("两个 bot 都应读到 provider 派生配置: %+v %+v", first, second)
	}
	if first.ProviderName == "" || first.CatalogID != "custom" {
		t.Fatalf("bot 响应应带 provider 名与目录归属: %+v", first)
	}

	if _, err := newProviderTestService(t, env).Update(userID, providerID, &UpdateAIProviderRequest{APIKey: strPtrBot("sk-share-5555")}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	for _, bot := range []*BotResponse{first, second} {
		fresh, err := env.bots.Get(userID, bot.ID)
		if err != nil || fresh.APIKeyLast4 != "5555" {
			t.Fatalf("轮换后 %s 应读到新末四位: %v %+v", bot.Name, err, fresh)
		}
	}
}

// Catalog 端点返回内置 models.dev 子集：确定性顺序、含重点提供商与字段形状。
func TestAIProviderCatalogProjection(t *testing.T) {
	env := setupBotTestEnv(t)
	svc := newProviderTestService(t, env)
	items := svc.Catalog()
	if len(items) < 100 {
		t.Fatalf("目录应约 200 家，实得 %d", len(items))
	}
	byID := map[string]CatalogProvider{}
	for _, p := range items {
		byID[p.ID] = p
	}
	for _, id := range []string{"zai", "deepseek", "alibaba", "volcengine"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("目录应包含 %s", id)
		}
	}
	zai := byID["zai"]
	if zai.API == "" || len(zai.Models) == 0 {
		t.Fatalf("zai 应带 api 与模型清单")
	}
	raw, err := json.Marshal(zai.Models[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"id"`, `"name"`, `"context_window"`, `"max_output_tokens"`, `"reasoning"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("模型投影缺字段 %s: %s", field, raw)
		}
	}
}

// TestAIProviderTestValidation 校验 provider 测试连接的参数面（不打网络）。
func TestAIProviderTestValidation(t *testing.T) {
	env := setupBotTestEnv(t)
	svc := newProviderTestService(t, env)

	if _, err := svc.Test(&TestAIProviderRequest{APIKey: strPtrBot(" ")}); err == nil {
		t.Fatalf("空 key 应拒绝")
	}
	if _, err := svc.Test(&TestAIProviderRequest{APIKey: strPtrBot("sk-x"), CatalogID: strPtrBot("no-such")}); err == nil {
		t.Fatalf("未知 catalog 应拒绝")
	}
	if _, err := svc.Test(&TestAIProviderRequest{APIKey: strPtrBot("sk-x"), CatalogID: strPtrBot("custom")}); err == nil {
		t.Fatalf("custom 缺 model 应拒绝")
	}
	if _, err := svc.Test(&TestAIProviderRequest{APIKey: strPtrBot("sk-x"), CatalogID: strPtrBot("custom"), BaseURL: strPtrBot("ftp://x")}); err == nil {
		t.Fatalf("custom 坏地址应拒绝")
	}
	// 目录 provider 不给 model：用目录第一个模型发起（打不可达地址也返回检查
	// 结果，不报错——可达性问题体现在 checks 里）。
	res, err := svc.Test(&TestAIProviderRequest{
		APIKey: strPtrBot("sk-probe-7777"), CatalogID: strPtrBot("deepseek"),
	})
	if err != nil {
		t.Fatalf("目录 provider 测试不应报错: %v", err)
	}
	if len(res.Checks) != 2 {
		t.Fatalf("应有 streaming/tool_call 两项检查: %+v", res)
	}
	if strings.Contains(botJSON(res), "sk-probe-7777") {
		t.Fatalf("测试结果泄露 key")
	}
	_ = uuid.NewString
}

// TestAIProviderTestProbesRunConcurrently（Codex 复审 P2）：三项探测并行发起，
// 总耗时受单次探测超时约束而不是串行相加——串行在慢/黑洞提供商上会超过 web
// 侧 35s 的客户端超时，用户在拿到逐项结果前就被 abort。用一个「三个请求都到
// 齐才响应」的屏障服务器证明并行：串行执行时第一个请求会卡满屏障超时。
func TestAIProviderTestProbesRunConcurrently(t *testing.T) {
	var mu sync.Mutex
	arrived := 0
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrived++
		n := arrived
		mu.Unlock()
		if n == 3 {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"demo-model"}]}`)
			return
		}
		writeFakeChatSSE(w, "pong")
	}))
	t.Cleanup(srv.Close)

	env := setupBotTestEnv(t)
	svc := NewAIProviderService(env.db, env.cfg, zap.NewNop())
	start := time.Now()
	res, err := svc.Test(&TestAIProviderRequest{
		CatalogID: strPtrBot(catalog.CustomCatalogID),
		BaseURL:   strPtrBot(srv.URL + "/v1"),
		APIKey:    strPtrBot("sk-test-concurrent-0001"),
		Model:     strPtrBot("demo-model"),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !res.Ok || len(res.Checks) != 2 {
		t.Fatalf("两项检查都应通过: %+v", res)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("三项探测应并行发起，实得耗时 %s（串行会卡满屏障超时）", elapsed)
	}
}
