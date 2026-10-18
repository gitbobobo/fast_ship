package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-115 测试：模型窗口自动探测与缓存（需求一）、压缩快速失败与失败不重试
// （需求二）、运行指标契约（需求四）。探测服务只服务 /models 与
// /chat/completions，模型请求计数与之分开，便于断言「缓存命中不打网络」。

// windowProbeSrv 是探测测试服务：/models 返回可变的状态码与正文并计数；
// /chat/completions 回最小 SSE 文本（连接探测与主线都能走通）。
type windowProbeSrv struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits int
	code int
	body string
}

func newWindowProbeServer(t *testing.T, code int, body string) *windowProbeSrv {
	t.Helper()
	w := &windowProbeSrv{code: code, body: body}
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.hits++
		c, b := w.code, w.body
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(c)
		fmt.Fprint(rw, b)
	})
	mux.HandleFunc("/chat/completions", func(rw http.ResponseWriter, r *http.Request) {
		writeFakeChatSSE(rw, "ok")
	})
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *windowProbeSrv) URL() string { return w.srv.URL }

func (w *windowProbeSrv) hitCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits
}

func (w *windowProbeSrv) setModels(code int, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.code, w.body = code, body
}

// createBotRowAt 直接落一行 bot（绕过 Create 的后台刷新，避免与断言竞速）。
func createBotRowAt(t *testing.T, env *botTestEnv, userID, baseURL, modelName string) *model.Bot {
	t.Helper()
	cipher, last4, err := env.bots.encryptAPIKey("sk-window-4321")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	now := time.Now().UTC()
	bot := &model.Bot{
		ID: uuid.NewString(), UserID: userID, Name: "win-" + uuid.NewString()[:6],
		SystemPromptText:      buildBotSystemPrompt("简洁回答。"),
		PromptRevision:        1,
		PlatformPromptVersion: botPlatformPromptVersion,
		ToolsetVersion:        botToolsetVersion,
		LastToldVersion:       botToolsetVersion,
		BaseURL:               baseURL, Model: modelName,
		APIKeyEncrypted: cipher, APIKeyLast4: last4,
		NextMessageSeq: 1, NextEventSeq: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := env.bots.bots.Create(bot); err != nil {
		t.Fatalf("create bot row: %v", err)
	}
	return bot
}

func findWindowRow(t *testing.T, env *botTestEnv, baseURL, modelName string) *model.BotModelWindow {
	t.Helper()
	row, err := env.bots.windows.FindByAddrModel(botNormalizeBaseURL(baseURL), modelName)
	if err != nil {
		return nil
	}
	return row
}

// ---------- A：探测、缓存与解析顺序 ----------

func TestBotWindowDetectDeepSeekShape(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200,
		`{"object":"list","data":[{"id":"deepseek-flash","object":"model","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winseek")
	bot := createBotRowAt(t, env, userID, ps.URL(), "deepseek-flash")

	cap, err := env.bots.detectModelWindow(context.Background(), ps.URL(), "sk-x", "deepseek-flash")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if cap.WindowTokens != 1048576 || cap.MaxOutputTokens != 262144 || !cap.Detected || cap.Source != "detected" {
		t.Fatalf("探测结果不符: %+v", cap)
	}

	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "deepseek-flash"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	capability := env.bots.modelCapability(bot, "deepseek-flash")
	if capability.WindowTokens != 1048576 || capability.MaxOutputTokens != 262144 || capability.Source != "detected" || !capability.Detected {
		t.Fatalf("缓存解析不符: %+v", capability)
	}
	// 输入预算扣除输出空间。
	if got := env.bots.inputBudgetFor(bot, "deepseek-flash"); got != 1048576-262144 {
		t.Fatalf("inputBudgetFor 应为窗口-输出上限，实得 %d", got)
	}
}

func TestBotWindowDetectLenientShapes(t *testing.T) {
	env := setupBotTestEnv(t)
	cases := []struct {
		name    string
		body    string
		wantWin int64
		wantOut int64
	}{
		{"models键+字符串数字", `{"models":[{"model":"m1","context_length":"4096","max_tokens":"1024"}]}`, 4096, 1024},
		{"裸数组", `[{"name":"m1","max_context_tokens":8192}]`, 8192, 0},
		{"大小写不敏感命中", `{"data":[{"id":"M1","context_window":512}]}`, 512, 0},
	}
	for i, tc := range cases {
		ps := newWindowProbeServer(t, 200, tc.body)
		cap, err := env.bots.detectModelWindow(context.Background(), ps.URL(), "sk-x", "m1")
		if err != nil {
			t.Fatalf("%s: detect: %v", tc.name, err)
		}
		if cap.WindowTokens != tc.wantWin || cap.MaxOutputTokens != tc.wantOut {
			t.Fatalf("%s: got %+v", tc.name, cap)
		}
		if i == 0 && cap.Detected != true {
			t.Fatalf("%s: 应标记 detected", tc.name)
		}
	}
}

func TestBotWindowMissingFieldsFallsBack(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","owned_by":"x"}]}`)
	userID := createBotTestUser(t, env.db, "winmiss")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")

	if _, err := env.bots.detectModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("缺失窗口字段应返回 error")
	}
	// 未装缓存 → 回退默认窗口，输出上限缺失不得当成 0 参与。
	cap := env.bots.modelCapability(bot, "m1")
	if cap.Source != "fallback" || cap.MaxOutputTokens != 0 {
		t.Fatalf("应回退 fallback: %+v", cap)
	}
	if cap.WindowTokens != env.cfg.Bots.Context.WindowTokensFor("m1") {
		t.Fatalf("fallback 窗口应等于 WindowTokensFor: %+v", cap)
	}
	if got := env.bots.inputBudgetFor(bot, "m1"); got != env.cfg.Bots.Context.WindowTokensFor("m1") {
		t.Fatalf("无输出上限时预算应等于窗口: %d", got)
	}
	// refresh 落失败标记行（ContextWindow=0，短 TTL 负缓存）：解析层只认 >0，
	// 能力解析仍回退，且负缓存期内不再对同一地址白探。
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("refresh 应返回 error")
	}
	row := findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.ContextWindow != 0 {
		t.Fatalf("窗口缺失应落失败标记行: %+v", row)
	}
	if cap := env.bots.modelCapability(bot, "m1"); cap.Source != "fallback" {
		t.Fatalf("失败标记不得参与能力解析: %+v", cap)
	}
	hits := ps.hitCount()
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("第二次 refresh 仍应失败")
	}
	// 负缓存只约束 ensureModelWindow 的新鲜度判断；显式 refresh 是主动探测，
	// 不被失败标记拦住。
	if h := ps.hitCount(); h != hits+1 {
		t.Fatalf("显式 refresh 不应被负缓存拦住: %d → %d", hits, h)
	}
}

func TestBotWindowKeyedByAddrAndModel(t *testing.T) {
	env := setupBotTestEnv(t)
	psA := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576}]}`)
	psB := newWindowProbeServer(t, 200, `{"data":[]}`)
	userID := createBotTestUser(t, env.db, "winaddr")
	botA := createBotRowAt(t, env, userID, psA.URL(), "m1")
	botB := createBotRowAt(t, env, userID, psB.URL(), "m1")

	if err := env.bots.refreshModelWindow(context.Background(), psA.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh A: %v", err)
	}
	if cap := env.bots.modelCapability(botA, "m1"); cap.Source != "detected" || cap.WindowTokens != 1048576 {
		t.Fatalf("地址 A 应命中缓存: %+v", cap)
	}
	// 同名模型不同地址不串用：地址 B 未命中 → fallback。
	if cap := env.bots.modelCapability(botB, "m1"); cap.Source != "fallback" {
		t.Fatalf("地址 B 不得串用地址 A 的缓存: %+v", cap)
	}
	// 带尾部斜杠的同一地址归一化后命中。
	if cap := env.bots.capabilityFor(psA.URL()+"/", "m1"); cap.Source != "detected" {
		t.Fatalf("地址应规范化后命中: %+v", cap)
	}
}

// 大小写不敏感兜底命中后，缓存必须按「配置的原样模型名」也能查到：否则
// 探测成功也永远走 fallback，且大输入每轮同步重探（第四轮复审 G1）。
func TestBotWindowPersistAliasForRequestedCaseVariant(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200,
		`{"data":[{"id":"M1","context_window":65536,"max_output_tokens":16384}]}`)

	// 配置 m1、列表只返回 M1：别名行让按原名的精确查询命中。
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	row := findWindowRow(t, env, ps.URL(), "m1")
	if row == nil {
		t.Fatalf("配置名 m1 应有缓存行（列表条目 M1 的大小写别名）")
	}
	if row.ContextWindow != 65536 || row.MaxOutputTokens != 16384 {
		t.Fatalf("别名行元数据应与列表条目一致: %+v", row)
	}
	if cap := env.bots.capabilityFor(ps.URL(), "m1"); cap.Source != "detected" || cap.WindowTokens != 65536 {
		t.Fatalf("capabilityFor 按配置名应命中 detected: %+v", cap)
	}

	// 精确同名不产生多余别名：全新地址上列表名与配置名完全一致，探测后该
	// 地址只有列表条目这一行。
	psExact := newWindowProbeServer(t, 200,
		`{"data":[{"id":"m1","context_window":131072,"max_output_tokens":2048}]}`)
	if err := env.bots.refreshModelWindow(context.Background(), psExact.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh exact: %v", err)
	}
	var count int64
	env.db.Model(&model.BotModelWindow{}).
		Where("base_url = ?", botNormalizeBaseURL(psExact.URL())).Count(&count)
	if count != 1 {
		t.Fatalf("精确同名探测应只有一行缓存，实得 %d", count)
	}
	if row := findWindowRow(t, env, psExact.URL(), "m1"); row == nil || row.ContextWindow != 131072 {
		t.Fatalf("精确同名行应正常落缓存: %+v", row)
	}
}

func TestBotWindowManualOverridesCache(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576}]}`)
	userID := createBotTestUser(t, env.db, "winmanual")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	env.cfg.Bots.Context.ModelWindows = map[string]int64{"m1": 123456}
	cap := env.bots.modelCapability(bot, "m1")
	if cap.Source != "manual" || cap.WindowTokens != 123456 || cap.Detected {
		t.Fatalf("manual 覆盖应优先于缓存: %+v", cap)
	}
	if got := env.bots.inputBudgetFor(bot, "m1"); got != 123456 {
		t.Fatalf("manual 无输出上限时预算应等于覆盖值: %d", got)
	}
}

func TestBotWindowDetectFailurePaths(t *testing.T) {
	env := setupBotTestEnv(t)
	// HTTP 500。
	ps := newWindowProbeServer(t, 500, `{"error":"boom"}`)
	if _, err := env.bots.detectModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("500 应返回 error")
	}
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("refresh 应返回 error")
	}
	// 失败落短 TTL 负缓存标记（ContextWindow=0），不参与能力解析。
	if row := findWindowRow(t, env, ps.URL(), "m1"); row == nil || row.ContextWindow != 0 {
		t.Fatalf("失败应落失败标记行: %+v", row)
	}
	// 超时：探测服务挂住请求，ctx 提前到期。
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	t.Cleanup(func() { close(hang); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := env.bots.detectModelWindow(ctx, srv.URL, "sk-x", "m1"); err == nil {
		t.Fatalf("超时应返回 error")
	}
	// 失败后解析仍可用（回退），不阻塞预算计算。
	userID := createBotTestUser(t, env.db, "wintimeout")
	bot := createBotRowAt(t, env, userID, srv.URL, "m1")
	if cap := env.bots.modelCapability(bot, "m1"); cap.Source != "fallback" || cap.WindowTokens <= 0 {
		t.Fatalf("探测失败后仍应回退可用: %+v", cap)
	}
}

func TestBotWindowProbeFailureKeepsSuccessfulCache(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winkeep")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")

	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("首探应成功: %v", err)
	}
	// 上游瞬时失败 / 坏密钥：地址+模型共享一条缓存，既有成功条目不得被
	// 0 值负缓存覆盖，否则同上游其他机器人会降级到回退预算。
	ps.setModels(500, `{"error":"boom"}`)
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("失败探测应返回 error")
	}
	row := findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 {
		t.Fatalf("成功缓存不得被失败标记覆盖: %+v", row)
	}
	// 第五轮复审：成功行同时记下失败退避时间（数据原样，只动 failed_at）。
	if row == nil || row.FailedAt == nil || time.Since(*row.FailedAt) > time.Minute {
		t.Fatalf("成功行应记录失败退避时间: %+v", row)
	}
	if cap := env.bots.modelCapability(bot, "m1"); cap.Source != "detected" || cap.WindowTokens != 1048576 {
		t.Fatalf("能力解析应仍用成功缓存: %+v", cap)
	}
}

func TestBotConnectionFailureKeepsSuccessfulCache(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winconnkeep")
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("首探应成功: %v", err)
	}
	// 测试连接用坏密钥触发上游失败：不得把共享的成功缓存覆盖成 0 值标记。
	ps.setModels(500, `{"error":"boom"}`)
	res, err := env.bots.TestConnection(userID, &TestConnectionRequest{
		BaseURL: strPtrBot(ps.URL()), Model: strPtrBot("m1"), APIKey: strPtrBot("sk-bad"),
	})
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if res.ContextSource != "detected" || res.ContextWindow != 1048576 || res.MaxOutputTokens != 262144 {
		t.Fatalf("测试连接应沿用既有成功缓存: %+v", res)
	}
	row := findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 {
		t.Fatalf("成功缓存不得被测试连接的失败标记覆盖: %+v", row)
	}
}

func TestBotAuxBudgetsAvoidsDoubleReservation(t *testing.T) {
	// 窗口 8192、输出上限 8000、包装 200：完整窗口定价仍能发出一个小摘要；
	// 若先扣输出空间（得到 192 的伪窗口）会在发请求前就判失败。
	reqOut, inputBudget, ok := botAuxBudgets(8192, 8000, 100000, 200)
	if !ok {
		t.Fatalf("小窗口应能容纳小摘要")
	}
	if reqOut != 7864 || inputBudget != 128 {
		t.Fatalf("输出应向窗口余量封顶: reqOut=%d input=%d", reqOut, inputBudget)
	}
	// 窗口余量高于探测输出上限时，输出封顶到上限。
	reqOut, inputBudget, ok = botAuxBudgets(100000, 8192, 50000, 200)
	if !ok || reqOut != 8192 || inputBudget != 100000-200-8192 {
		t.Fatalf("输出应向上限封顶: reqOut=%d input=%d ok=%v", reqOut, inputBudget, ok)
	}
	// 包装本身超出窗口：干净失败。
	if _, _, ok := botAuxBudgets(300, 0, 1000, 200); ok {
		t.Fatalf("包装超窗应返回 false")
	}
}

func TestBotWindowCacheHitNoNetwork(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":4096}]}`)
	userID := createBotTestUser(t, env.db, "wincache")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	before := ps.hitCount()
	for i := 0; i < 3; i++ {
		if cap := env.bots.modelCapability(bot, "m1"); cap.WindowTokens != 4096 {
			t.Fatalf("缓存命中应给出探测值: %+v", cap)
		}
		_ = env.bots.inputBudgetFor(bot, "m1")
	}
	if after := ps.hitCount(); after != before {
		t.Fatalf("缓存命中不应打网络: %d → %d", before, after)
	}
}

func TestBotWindowCreateUpdateTriggerRefresh(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":4096},{"id":"m2","context_window":8192}]}`)
	userID := createBotTestUser(t, env.db, "wincrud")

	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "crud", BaseURL: ps.URL(), Model: "m1", APIKey: "sk-x-9999",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForBot(t, "create 后缓存行", 10*time.Second, func() bool {
		return findWindowRow(t, env, ps.URL(), "m1") != nil
	})

	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{Model: strPtrBot("m2")}); err != nil {
		t.Fatalf("update: %v", err)
	}
	waitForBot(t, "update 后新模型缓存行", 10*time.Second, func() bool {
		return findWindowRow(t, env, ps.URL(), "m2") != nil
	})
}

func TestBotWindowRunStartTTLRefresh(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":4096}]}`)
	userID := createBotTestUser(t, env.db, "winttl")
	botRow := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, botRow)

	// 缓存缺失 + 输入估算低于回退触发阈值 → 后台刷新，本轮不等待网络。
	env.bots.ensureModelWindow(botRow, run, "", 0)
	waitForBot(t, "TTL 刷新落缓存", 10*time.Second, func() bool {
		return findWindowRow(t, env, ps.URL(), "m1") != nil
	})

	// 新鲜缓存 → 不再打网络。
	env.bots.ensureModelWindow(botRow, run, "", 0)
	hits := ps.hitCount()
	time.Sleep(500 * time.Millisecond)
	if ps.hitCount() != hits {
		t.Fatalf("新鲜缓存不应触发刷新")
	}

	// FetchedAt 早于 24h → 再次刷新。
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "m1",
		ContextWindow: 4096, Source: "detected", Detail: "GET /models",
		FetchedAt: time.Now().UTC().Add(-25 * time.Hour), UpdatedAt: time.Now().UTC().Add(-25 * time.Hour),
	}); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}
	env.bots.ensureModelWindow(botRow, run, "", 0)
	waitForBot(t, "过期缓存重新探测", 10*time.Second, func() bool {
		row := findWindowRow(t, env, ps.URL(), "m1")
		return row != nil && time.Since(row.FetchedAt) < time.Hour
	})
}

func TestBotConnectionDetectsWindow(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"deepseek-flash","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winconn")

	res, err := env.bots.TestConnection(userID, &TestConnectionRequest{
		BaseURL: strPtrBot(ps.URL()), Model: strPtrBot("deepseek-flash"), APIKey: strPtrBot("sk-x-7777"),
	})
	if err != nil || !res.Ok {
		t.Fatalf("test connection: %v %+v", err, res)
	}
	if res.ContextSource != "detected" || res.ContextWindow != 1048576 || res.MaxOutputTokens != 262144 {
		t.Fatalf("测试连接应并入探测结果: %+v", res)
	}
	if row := findWindowRow(t, env, ps.URL(), "deepseek-flash"); row == nil || row.ContextWindow != 1048576 {
		t.Fatalf("测试连接应写缓存: %+v", row)
	}
	// F6：探测+持久化只发生一次——/models 恰好被打一次（此前响应填完再
	// refresh 会二次探测，两次失败面叠加、响应与缓存还可能不一致）。
	if hits := ps.hitCount(); hits != 1 {
		t.Fatalf("TestConnection 应只探测一次 /models，实得 %d", hits)
	}

	// 探测失败：来源如实回退，不影响 ok；失败标记落库。
	psFail := newWindowProbeServer(t, 500, `{}`)
	res, err = env.bots.TestConnection(userID, &TestConnectionRequest{
		BaseURL: strPtrBot(psFail.URL()), Model: strPtrBot("m1"), APIKey: strPtrBot("sk-x-7777"),
	})
	if err != nil || !res.Ok {
		t.Fatalf("探测失败不应影响连接结果: %v %+v", err, res)
	}
	if res.ContextSource != "fallback" {
		t.Fatalf("探测失败应如实标注 fallback: %+v", res)
	}
	if row := findWindowRow(t, env, psFail.URL(), "m1"); row == nil || row.ContextWindow != 0 {
		t.Fatalf("探测失败应落失败标记行: %+v", row)
	}
}

// ---------- B：压缩前置检查、失败不重试、进度事件 ----------

// compactionPrecheckEnv 构造一个「固定前缀 + 最小摘要预算 ≥ 触发阈值」的
// 场景：默认固定投影约 5.2k token，窗口取 6400（触发阈值 4800）即可命中
// target_unreachable 且不命中「已超窗口」。
func compactionPrecheckEnv(t *testing.T) (*botTestEnv, *model.Bot, *model.BotRun) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "摘要"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "precheck")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 6400
	seedCompactableLedger(t, env, bot.ID, "旧轮次")
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)
	return env, botRow, run
}

func TestBotCompactionPrecheckFastFail(t *testing.T) {
	env, botRow, run := compactionPrecheckEnv(t)

	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err == nil || !strings.Contains(err.Error(), "context_uncompressible") {
		t.Fatalf("前置检查应快速失败: %v", err)
	}
	// F4：精确原因随错误类型带走，不在返回边界降级。
	var ce *botCompactionError
	if !errors.As(err, &ce) || ce.Code != "target_unreachable" || ce.Message == "" {
		t.Fatalf("应返回带精确原因的压缩错误: %v", err)
	}
	if !errors.Is(err, errContextUncompressible) {
		t.Fatalf("哨兵判定必须保持成立: %v", err)
	}
	if out == nil || out.Run == nil || out.Published {
		t.Fatalf("不得发布: %+v", out)
	}
	// 断言没有发出归档/摘要请求：maintenance_requests 里没有 archive/summary 行。
	arch, sum := countMaintByPurpose(env.db, out.Run.ID)
	if arch != 0 || sum != 0 {
		t.Fatalf("快速失败不得发出归档/摘要请求: arch=%d sum=%d", arch, sum)
	}
	var comp model.BotCompaction
	env.db.Where("maintenance_run_id = ?", out.Run.ID).First(&comp)
	if comp.ErrorCode == nil || *comp.ErrorCode != "target_unreachable" {
		t.Fatalf("应记 target_unreachable: %+v", comp.ErrorCode)
	}
	fresh, _ := repository.NewBotMaintenanceRepository(env.db).FindByID(out.Run.ID)
	if fresh.Status != model.BotMaintenanceFailed {
		t.Fatalf("维护运行应 failed: %s", fresh.Status)
	}

	// run 收尾（botLoop 的失败路径）：精确码落到 run.error_code，进度事件与
	// lifecycle detail 带中文说明。
	built, berr := env.bots.buildRunContext(botRow, run)
	if berr != nil {
		t.Fatalf("build: %v", berr)
	}
	env.bots.finishCompactionFailed(botRow, run, built, err)
	if !hasMaintenanceProgressEvent(t, env.db, botRow.ID, "failed") {
		t.Fatalf("应落 failed 进度事件")
	}
	var runRow model.BotRun
	env.db.Where("id = ?", run.ID).First(&runRow)
	if runRow.ErrorCode == nil || *runRow.ErrorCode != "target_unreachable" {
		t.Fatalf("run error_code 应为精确原因 target_unreachable: %+v", runRow.ErrorCode)
	}
	var notice model.BotMessage
	env.db.Where("bot_id = ? AND dedup_key LIKE ?", botRow.ID, "compaction_failed:%").First(&notice)
	var content botLifecycleContent
	if err := json.Unmarshal([]byte(notice.ContentJSON), &content); err != nil || content.Event != "compaction_failed" {
		t.Fatalf("lifecycle 正文不符: %v %s", err, notice.ContentJSON)
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(content.Detail, &detail); err != nil {
		t.Fatalf("detail 应是 JSON 对象: %v", err)
	}
	if detail["code"] != "target_unreachable" {
		t.Fatalf("detail 应含精确 code: %v", detail)
	}
	if note, _ := detail["note"].(string); !strings.Contains(note, "压缩目标不可达") {
		t.Fatalf("detail.note 应为中文原因: %v", detail["note"])
	}
}

func TestBotCompactionPrecheckUncompressible(t *testing.T) {
	env, botRow, run := compactionPrecheckEnv(t)
	// 窗口压到固定前缀以下：命中第一优先级 context_uncompressible。
	env.cfg.Bots.Context.WindowTokens = 4096
	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err == nil || !strings.Contains(err.Error(), "context_uncompressible") {
		t.Fatalf("固定前缀超窗应 context_uncompressible: %v", err)
	}
	arch, sum := countMaintByPurpose(env.db, out.Run.ID)
	if arch != 0 || sum != 0 {
		t.Fatalf("不得发出归档/摘要请求: arch=%d sum=%d", arch, sum)
	}
	var comp model.BotCompaction
	env.db.Where("maintenance_run_id = ?", out.Run.ID).First(&comp)
	if comp.ErrorCode == nil || *comp.ErrorCode != "context_uncompressible" {
		t.Fatalf("应记 context_uncompressible: %+v", comp.ErrorCode)
	}
}

func TestBotCompactionStillPublishesWhenReachable(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[{"text":"归档要点","source_seq":1}]}`},
		fakeModelTurn{Text: "滚动摘要：用户在做机器人主线。"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "winok")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 30000
	// 段内容要足够大：折叠后仍高于目标（goal≈10.1k）才会真正走摘要。
	seedCompactableLedger(t, env, bot.ID, strings.Repeat("很长的旧历史。", 1200))
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.NeedSummary {
		t.Fatalf("本场景应需要摘要: afterFold=%d goal=%d", plan.AfterFoldTokens, plan.GoalTokens)
	}
	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("可达目标应正常压缩: %v", err)
	}
	if !out.Published {
		t.Fatalf("应发布")
	}
	// 正常压缩仍发布进度事件：archive 与 summary。
	if !hasMaintenanceProgressEvent(t, env.db, bot.ID, "archive") {
		t.Fatalf("应落 archive 进度事件")
	}
	if !hasMaintenanceProgressEvent(t, env.db, bot.ID, "summary") {
		t.Fatalf("应落 summary 进度事件")
	}
	state, _ := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if state.Generation != 1 || state.SummaryThroughSeq == 0 {
		t.Fatalf("水位应推进: %+v", state)
	}
}

// hasMaintenanceProgressEvent 报告是否落过 kind=maintenance_progress 且
// phase 匹配的 delta 事件。
func hasMaintenanceProgressEvent(t *testing.T, db *gorm.DB, botID, phase string) bool {
	t.Helper()
	var bodies []string
	if err := db.Model(&model.BotEvent{}).Where("bot_id = ?", botID).
		Order("seq asc").Pluck("payload_json", &bodies).Error; err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, b := range bodies {
		var env0 botEventEnvelope
		if json.Unmarshal([]byte(b), &env0) != nil || env0.Kind != "maintenance_progress" {
			continue
		}
		if m, ok := env0.Payload.(map[string]interface{}); ok && m["phase"] == phase {
			return true
		}
	}
	return false
}

func TestBotCompactionFailureNoAutoRetry(t *testing.T) {
	env, botRow, _ := compactionPrecheckEnv(t)
	userID := botRow.UserID
	var maxSeq int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ?", botRow.ID).Select("MAX(seq)").Scan(&maxSeq)
	offeredBefore := botRow.OfferedInputSeq
	if offeredBefore != maxSeq {
		t.Fatalf("前置：水位应已推进到账本末位 %d，实得 %d", maxSeq, offeredBefore)
	}

	// e2e：SendMessage 建主线 run，构建后估算越过触发阈值 → 阈值压缩快速
	// 失败 → run failed 且水位推进。
	if _, err := env.bots.SendMessage(userID, botRow.ID, &SendMessageRequest{
		Text: "再来一句", ClientMessageID: "fail-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 15*time.Second, func() bool { return env.botSettled(botRow.ID) })

	var botFresh model.Bot
	env.db.Where("id = ?", botRow.ID).First(&botFresh)
	if botFresh.OfferedInputSeq < maxSeq {
		t.Fatalf("失败路径应推进 offered_input_seq: %d < %d", botFresh.OfferedInputSeq, maxSeq)
	}
	var runs []model.BotRun
	env.db.Where("bot_id = ?", botRow.ID).Find(&runs)
	failed := 0
	for _, r := range runs {
		if r.Status == model.BotRunStatusFailed && r.ErrorCode != nil {
			switch *r.ErrorCode {
			case "context_uncompressible", "context_compaction_failed", "target_unreachable", "no_reduction":
				// F4：run error_code 带精确原因码；前置检查场景即 target_unreachable。
				failed++
			}
		}
	}
	if failed == 0 {
		t.Fatalf("应有压缩失败 run: %s", dumpRuns(t, env, botRow.ID))
	}
	// lifecycle 失败消息恰好一条（去重）。
	var lifecycle int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key LIKE ?",
		botRow.ID, "compaction_failed:%").Count(&lifecycle)
	if lifecycle != 1 {
		t.Fatalf("应恰好一条压缩失败 lifecycle 消息: %d", lifecycle)
	}
	var notice model.BotMessage
	env.db.Where("bot_id = ? AND dedup_key LIKE ?", botRow.ID, "compaction_failed:%").First(&notice)
	var content botLifecycleContent
	if err := json.Unmarshal([]byte(notice.ContentJSON), &content); err != nil || content.Event != "compaction_failed" {
		t.Fatalf("lifecycle 正文不符: %v %s", err, notice.ContentJSON)
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(content.Detail, &detail); err != nil {
		t.Fatalf("detail 应是 JSON 对象: %v", err)
	}
	if detail["code"] == nil || detail["generation"] == nil || detail["through_seq"] == nil {
		t.Fatalf("detail 应含 code/generation/through_seq: %v", detail)
	}

	// 扫描器不再自动重复处理同一输入：手动跑一轮扫描，不得建新 run。
	env.bots.scanOnce()
	time.Sleep(200 * time.Millisecond)
	if active := env.activeRunOrNothing(botRow.ID); active != nil {
		t.Fatalf("失败后扫描不应再拉起同一输入: %+v", active)
	}
}

// ---------- C：运行指标契约 ----------

func TestBotChatSnapshotMetrics(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "winmetrics")
	bot := env.createBot(t, userID)
	botRow, _ := env.bots.bots.Get(bot.ID)

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "metrics-fixture",
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: fmt.Sprintf(`{"model":%q,"base_url":%q}`, botRow.Model, botRow.BaseURL),
		CreatedAt:          now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	firstDelta := now.Add(2 * time.Second)
	finished := now.Add(5 * time.Second)
	prompt, completion, cached := int64(900), int64(300), int64(450)
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status:       model.BotModelRequestSucceeded,
		PromptTokens: &prompt, CompletionTokens: &completion, CachedTokens: &cached,
		InputTokensEstimate: 950, InputEstimator: "utf8_bytes/3.5",
		FirstDeltaAt: &firstDelta,
		StartedAt:    now, FinishedAt: &finished,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x",
		TokenEstimator: "chars/4", PromptRevision: 1, ToolsetVersion: botRow.ToolsetVersion,
	}
	if err := env.bots.requests.CreateTx(env.db, req); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Metrics == nil || snap.Metrics.Request == nil {
		t.Fatalf("应有 metrics: %+v", snap.Metrics)
	}
	m := snap.Metrics
	if m.ContextWindow != env.cfg.Bots.Context.WindowTokensFor(botRow.Model) || m.ContextSource != "fallback" {
		t.Fatalf("窗口能力不符: %+v", m)
	}
	r := m.Request
	if r.RequestID != req.ID || r.RunID != run.ID || r.Ordinal != 1 || r.Status != "succeeded" {
		t.Fatalf("请求口径不符: %+v", r)
	}
	if r.PromptTokens == nil || *r.PromptTokens != 900 || r.CompletionTokens == nil ||
		*r.CompletionTokens != 300 || r.CachedTokens == nil || *r.CachedTokens != 450 || !r.UsageComplete {
		t.Fatalf("usage 口径不符: %+v", r)
	}
	if r.GenerationMs != 3000 {
		t.Fatalf("generation_ms 应为 finished-first_delta=3000，实得 %d", r.GenerationMs)
	}
	if r.InputTokensEstimate != 950 || r.InputEstimator != "utf8_bytes/3.5" {
		t.Fatalf("估算口径不符: %+v", r)
	}
	if r.FinishedAt == nil {
		t.Fatalf("finished_at 应有值")
	}
}

func TestBotChatSnapshotMetricsAbsentOrDegenerate(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "winmetrics2")
	bot := env.createBot(t, userID)

	// 没有任何主线请求：metrics 仍产出（F11），窗口能力可见、Request 为 nil。
	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Metrics == nil {
		t.Fatalf("无请求时也应产出 metrics（窗口能力可见）")
	}
	if snap.Metrics.Request != nil {
		t.Fatalf("无请求时 Request 应为 nil: %+v", snap.Metrics.Request)
	}
	if snap.Metrics.ContextWindow != env.cfg.Bots.Context.WindowTokensFor(bot.Model) {
		t.Fatalf("窗口能力应可见: %+v", snap.Metrics)
	}

	// 定时任务会话的请求不算主线口径；主线缺 usage/首片的请求 generation_ms=0。
	now := time.Now().UTC()
	schedRun := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerSchedule, TriggerKey: "sched-metrics",
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindSchedule,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, schedRun); err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}
	if err := env.bots.requests.CreateTx(env.db, &model.BotModelRequest{
		ID: uuid.NewString(), RunID: schedRun.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, StartedAt: now,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
	}); err != nil {
		t.Fatalf("seed schedule request: %v", err)
	}
	mainRun := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "main-metrics",
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, mainRun); err != nil {
		t.Fatalf("seed main run: %v", err)
	}
	if err := env.bots.requests.CreateTx(env.db, &model.BotModelRequest{
		ID: uuid.NewString(), RunID: mainRun.ID, Ordinal: 1,
		Status: model.BotModelRequestFailed, StartedAt: now,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
	}); err != nil {
		t.Fatalf("seed main request: %v", err)
	}

	snap, err = env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if snap.Metrics == nil || snap.Metrics.Request == nil {
		t.Fatalf("应有主线请求的 metrics: %+v", snap.Metrics)
	}
	r := snap.Metrics.Request
	if r.RequestID == "" || r.Status != "failed" {
		t.Fatalf("应取主线最新请求: %+v", r)
	}
	if r.UsageComplete || r.PromptTokens != nil || r.GenerationMs != 0 {
		t.Fatalf("缺 usage/首片时口径应为不可用: %+v", r)
	}
}

// 活动主线 run 的最近一条维护进度随快照返回（第四轮复审 G2）：进度事件在
// 快照游标之前不会经 SSE 重放，刷新/重开聊天后由快照水合运行栏。
func TestBotChatSnapshotMaintenanceProgress(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "winmaint")
	bot := env.createBot(t, userID)

	// 无活动 run：无进度。
	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.MaintenanceProgress != nil {
		t.Fatalf("无活动 run 不应有进度: %+v", snap.MaintenanceProgress)
	}

	// 活动主线 run + 两条进度事件：取最近一条（phase/ message 齐全）。
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "maint-snap",
		Status: model.BotRunStatusRunning, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	env.bots.emitMaintenanceProgress(bot.ID, run.ID, "archive", "正在归档上下文…")
	env.bots.emitMaintenanceProgress(bot.ID, run.ID, "summary", "正在生成上下文摘要（第 2 批）…")

	snap, err = env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if snap.MaintenanceProgress == nil {
		t.Fatalf("活动 run 应带最近一条维护进度")
	}
	p := snap.MaintenanceProgress
	if p.RunID != run.ID || p.Phase != "summary" || p.Message != "正在生成上下文摘要（第 2 批）…" {
		t.Fatalf("应取 seq 最大的一条进度: %+v", p)
	}

	// run 结束后不再返回——已终结 run 的进度不应显示为进行中。
	finished := time.Now().UTC()
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Updates(map[string]interface{}{"finished_at": finished, "status": model.BotRunStatusCompleted}).Error; err != nil {
		t.Fatalf("finish run: %v", err)
	}
	snap, err = env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot 3: %v", err)
	}
	if snap.MaintenanceProgress != nil {
		t.Fatalf("run 结束后不应再返回进度: %+v", snap.MaintenanceProgress)
	}
}

func TestBotModelRequestFirstDeltaPersisted(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{
		Text:  "带首片的回复",
		Usage: &fakeModelUsage{Prompt: 10, Completion: 5, Total: 15},
	})
	userID := createBotTestUser(t, env.db, "windelta")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "fd-1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run completed", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	var req model.BotModelRequest
	env.db.Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", bot.ID).
		Order("started_at DESC").First(&req)
	if req.FirstDeltaAt == nil {
		t.Fatalf("首片时间应落库")
	}
	if req.FinishedAt == nil || req.FinishedAt.Before(*req.FirstDeltaAt) {
		t.Fatalf("首片时间应早于结束时间: %+v", req.FinishedAt)
	}
}

// ---------- F1：前置检查用本周期完整折叠 ----------

// seedBotAssistantDeclares 落一条 assistant(tool_calls) 行，只声明调用不落
// 回复；seedBotToolReplyOnly 单独落一条工具回复行。两者配合构造「并行调用中
// 有一部分未回执」的账本：未闭合的调用块让切点退到该 assistant 之前，而后续
// 已回执的大结果仍可折叠——即「新增折叠落在切点之后的保留区」。
type botSeedCall struct {
	CallID     string
	ProviderID string
}

func seedBotAssistantDeclares(t *testing.T, env *botTestEnv, botID string, calls []botSeedCall) *model.BotMessage {
	t.Helper()
	var a *model.BotMessage
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		seq, err := env.bots.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		tcs := make([]botAssistantToolCall, 0, len(calls))
		for _, c := range calls {
			tcs = append(tcs, botAssistantToolCall{
				CallID: c.CallID, ProviderToolCallID: c.ProviderID,
				ToolName: "fs_issue", Action: "list", Arguments: "{}",
			})
		}
		raw, _ := json.Marshal(botAssistantContent{Text: "", ToolCalls: tcs})
		a = &model.BotMessage{
			ID: uuid.NewString(), BotID: botID, Seq: seq,
			Kind: model.BotMessageKindAssistant, ContentJSON: string(raw),
			CreatedAt: time.Now().UTC(),
		}
		return repository.NewBotMessageRepository(tx).AppendTx(tx, a)
	}); err != nil {
		t.Fatalf("seed assistant declares: %v", err)
	}
	return a
}

func seedBotToolReplyOnly(t *testing.T, env *botTestEnv, botID, callID, providerID, reply string) *model.BotMessage {
	t.Helper()
	var tr *model.BotMessage
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		seq, err := env.bots.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(botToolReplyContent{
			CallID: callID, ProviderToolCallID: providerID,
			ToolName: "fs_issue", Status: "succeeded", Content: reply,
		})
		tr = &model.BotMessage{
			ID: uuid.NewString(), BotID: botID, Seq: seq,
			Kind: model.BotMessageKindToolReply, ContentJSON: string(raw),
			CreatedAt: time.Now().UTC(),
		}
		return repository.NewBotMessageRepository(tx).AppendTx(tx, tr)
	}); err != nil {
		t.Fatalf("seed tool reply: %v", err)
	}
	return tr
}

// TestBotCompactionPrecheckUsesCurrentFolds 回归（F1）：受保护的输入夹在多
// 调用回复之间、本周期新增的折叠位于摘要切点之后的保留区时，前置检查必须用
// plan.Folded（旧 ∪ 新增）估算，不得用上一周期的 plan.State.FoldedResultsJSON
// 把 F_pre 算大、把可达的压缩误判为不可达。
func TestBotCompactionPrecheckUsesCurrentFolds(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "滚动摘要：用户长期在推进主线。"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "prefold")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 30000
	botRow, _ := env.bots.bots.Get(bot.ID)

	// 几何（账本可构建）：a1 并行声明 c1/c2；c2 的大回复立即落账，c1 的回复
	// 晚到、排在保护用户之后。安全切点扫描在 c1 闭合调用块之前找不到安全点，
	// 切点退到 a1 之前（段=[u1]）——c2 的大回复于是落在「切点之后、保护边界
	// 之前」的保留区：旧折叠口径漏掉它，把 F_pre 算大，会把可达压缩误判为
	// 不可达（本回归的场景）。消息账本 append-only：先只落 u1 并量出固定前缀
	//（投影在 u1.Seq 截断时只剩前缀），再按窗口定制 c2 回复大小、依序补齐。
	u1 := seedBotUserMessage(t, env, bot.ID, strings.Repeat("很长的旧历史。", 3700))
	markHistoryConsumed(t, env, botRow, nil)
	probeRun := botFixtureRun(t, env, botRow)
	preF0 := env.bots.estimateProjectionTokens(botRow, probeRun, &botContextOverlay{
		SummaryText: "", SummaryThroughSeq: u1.Seq, Folded: map[string]string{},
	})
	env.db.Where("id = ?", probeRun.ID).Delete(&model.BotRun{})
	window := int64(30000)
	trigger := botFloorRatio(window, env.cfg.Bots.Context.CompressionRatioV())
	trTokens := trigger + 500 - preF0
	if trTokens < 2000 || trTokens > 24000 {
		t.Fatalf("几何异常：preF0=%d 需要的回复=%d 超出可构造范围", preF0, trTokens)
	}
	trRepeats := int(trTokens / 6) // 「很长的工具结果」7字×3字节 ≈ 6 token/次
	seedBotAssistantDeclares(t, env, bot.ID, []botSeedCall{
		{CallID: "call-pc-1", ProviderID: "prov-pc-1"},
		{CallID: "call-pc-2", ProviderID: "prov-pc-2"},
	})
	seedBotToolReplyOnly(t, env, bot.ID, "call-pc-2", "prov-pc-2", strings.Repeat("很长的工具结果", trRepeats))
	seedBotUserMessage(t, env, bot.ID, "保护轮A")
	seedBotUserMessage(t, env, bot.ID, "保护轮B")
	seedBotUserMessage(t, env, bot.ID, "保护轮C")
	seedBotToolReplyOnly(t, env, bot.ID, "call-pc-1", "prov-pc-1", "ok")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-small-2", "fs_issue", "小结果2")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-small-3", "fs_issue", "小结果3")
	seedBotAssistantToolBlock(t, env, bot.ID, "call-small-4", "fs_issue", "小结果4")
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.NeedSummary || plan.FoldedAdded != 1 || plan.ThroughSeq >= plan.ProtectStartSeq {
		t.Fatalf("前置几何不符: needSummary=%v foldedAdded=%d through=%d protect=%d afterFold=%d goal=%d before=%d window=%d",
			plan.NeedSummary, plan.FoldedAdded, plan.ThroughSeq, plan.ProtectStartSeq,
			plan.AfterFoldTokens, plan.GoalTokens, plan.BeforeTokens, plan.WindowTokens)
	}
	// 新增折叠必须在切点之后：tr1 的行 id 在折叠映射里，但行 seq 大于切点。
	foldedSeqOK := false
	for _, row := range plan.AllRows {
		if _, ok := plan.Folded[row.ID]; ok && row.Seq > plan.ThroughSeq {
			foldedSeqOK = true
		}
	}
	if !foldedSeqOK {
		t.Fatalf("本场景应存在位于切点之后的新增折叠")
	}
	// 旧口径（上一周期折叠）会误判不可达；本周期完整折叠则可达。
	manifest := s0Manifest(t, env, plan)
	preOld := env.bots.buildBoundarySnapshot(botRow, plan, "", manifest, plan.State.FoldedResultsJSON, plan.ThroughSeq).AfterTokens
	preNew := env.bots.buildBoundarySnapshot(botRow, plan, "", manifest, botMustJSON(plan.Folded), plan.ThroughSeq).AfterTokens
	if preOld+botMinSummaryTokens < trigger {
		t.Fatalf("回归前提失效：旧口径估算应误判不可达 preOld=%d T=%d", preOld, trigger)
	}
	if preNew+botMinSummaryTokens >= trigger || preNew >= window {
		t.Fatalf("回归前提失效：新口径应可达 preNew=%d T=%d W=%d", preNew, trigger, window)
	}

	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("折叠可达时不得误判不可达: %v", err)
	}
	if !out.Published {
		t.Fatalf("应正常发布: %+v", out)
	}
	state, _ := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if state.Generation != 1 || state.SummaryThroughSeq < u1.Seq {
		t.Fatalf("水位应推进: %+v", state)
	}
	arch, sum := countMaintByPurpose(env.db, out.Run.ID)
	if arch != 1 || sum != 1 {
		t.Fatalf("应恰好一次归档+一次摘要: arch=%d sum=%d", arch, sum)
	}
}

func s0Manifest(t *testing.T, env *botTestEnv, plan *botCompactionPlan) string {
	t.Helper()
	return env.bots.buildIdentifierManifest(plan)
}

// ---------- F2：窗口新鲜度收敛到 ensureModelWindow ----------

func TestBotEnsureModelWindowSyncProbeWhenCompacting(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576}]}`)
	userID := createBotTestUser(t, env.db, "winsync")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, bot)

	// 估算达到回退触发阈值且缓存缺失：有界同步探测，本轮内缓存即可见。
	fallbackTrigger := botFloorRatio(env.bots.inputBudgetFor(bot, "m1"), env.cfg.Bots.Context.CompressionRatioV())
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if row := findWindowRow(t, env, ps.URL(), "m1"); row == nil || row.ContextWindow != 1048576 {
		t.Fatalf("同步探测应在本轮内落缓存: %+v", row)
	}
	// 本轮随后的预算解析立即用探测值。
	if got := env.bots.inputBudgetFor(bot, "m1"); got != 1048576 {
		t.Fatalf("探测后预算应用探测窗口，实得 %d", got)
	}
	// 缓存新鲜后再次调用不打网络。
	hits := ps.hitCount()
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger+1000)
	if ps.hitCount() != hits {
		t.Fatalf("新鲜缓存不应再探测")
	}
}

func TestBotEnsureModelWindowAsyncWhenInputSmall(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576}]}`)
	userID := createBotTestUser(t, env.db, "winasync")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, bot)

	// 估算低于回退触发阈值：本轮不依赖窗口，立即返回（探测退回后台）。
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", 0)
	if row := findWindowRow(t, env, ps.URL(), "m1"); row != nil {
		t.Fatalf("小输入不应同步探测出缓存: %+v", row)
	}
	waitForBot(t, "后台刷新落缓存", 10*time.Second, func() bool {
		return findWindowRow(t, env, ps.URL(), "m1") != nil
	})
}

// newHangingModelsServer 起 /models 挂住、/chat/completions 回最小文本的
// 服务：模拟探测超时，同时主线仍可走通。
func newHangingModelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			<-hang
		default:
			writeFakeChatSSE(rw, "ok")
		}
	}))
	t.Cleanup(func() { close(hang); srv.Close() })
	return srv
}

// TestBotLoopUpgradedBotUsesDetectedWindow 端到端回归（F2）：升级后的存量
// 机器人无窗口缓存、账本估算已达默认小窗口的触发阈值。run 内必须在压缩判断
// 之前同步探到 1M 窗口——既不误判超限压缩，也不把输入判死。
func TestBotLoopUpgradedBotUsesDetectedWindow(t *testing.T) {
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"fake-model","context_window":1048576}]}`)
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "winupgrade")
	bot := createBotRowAt(t, env, userID, ps.URL(), "fake-model")
	// 大账本：估算越过默认窗口（32768）的触发阈值。
	seedBotUserMessage(t, env, bot.ID, strings.Repeat("很长的历史内容。", 4000))
	botRow, _ := env.bots.bots.Get(bot.ID)
	markHistoryConsumed(t, env, botRow, nil)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "upgrade-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })

	var run model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&run)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("探测到 1M 后主线应正常完成: %s err=%+v", run.Status, run.ErrorMessage)
	}
	// 探测确实被同步等到：缓存行已落且为探测值。
	row := findWindowRow(t, env, ps.URL(), "fake-model")
	if row == nil || row.ContextWindow != 1048576 {
		t.Fatalf("run 内应同步探测并落缓存: %+v", row)
	}
	// 未按回退窗口触发压缩。
	var comps int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ?", bot.ID).Count(&comps)
	if comps != 0 {
		t.Fatalf("不应触发压缩: %d", comps)
	}
	// 同步路径的前提取自实际估算：主线请求的估算确实越过了回退触发阈值。
	var req model.BotModelRequest
	env.db.Where("run_id = ?", run.ID).First(&req)
	fallbackTrigger := botFloorRatio(env.cfg.Bots.Context.WindowTokensFor("fake-model"), env.cfg.Bots.Context.CompressionRatioV())
	if req.InputTokensEstimate < fallbackTrigger {
		t.Fatalf("本场景前提是估算越过回退触发阈值 est=%d T=%d", req.InputTokensEstimate, fallbackTrigger)
	}
}

// TestBotLoopProbeTimeoutFallsBackWithoutConsuming 端到端回归（F2）：/models
// 超时的有界等待之后继续用回退窗口——压缩走折叠路径正常发布，主线完成，
// 输入不被探测失败误消费。
func TestBotLoopProbeTimeoutFallsBackWithoutConsuming(t *testing.T) {
	srv := newHangingModelsServer(t)
	origTimeout := botWindowSyncProbeTimeout
	botWindowSyncProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { botWindowSyncProbeTimeout = origTimeout })

	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "wintimeout2")
	bot := createBotRowAt(t, env, userID, srv.URL, "fake-model")
	env.cfg.Bots.Context.WindowTokens = 60000
	botRow, _ := env.bots.bots.Get(bot.ID)

	// 可折叠的大工具回复按当前前缀体积补够，让估算落在 [触发阈值, 窗口)
	// 的软区间。回复块必须排在保护用户之前（否则整段进入保留区、折叠不
	// 可达，前置检查会把输入判死——那正是本用例要排除的路径）。
	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	probeRun := botFixtureRun(t, env, botRow)
	base := env.bots.estimateProjectionTokens(botRow, probeRun, nil)
	env.db.Where("id = ?", probeRun.ID).Delete(&model.BotRun{})
	window := env.bots.inputBudgetFor(botRow, "fake-model")
	trigger := botFloorRatio(window, env.cfg.Bots.Context.CompressionRatioV())
	blocks := int((trigger+(window-trigger)/2-base)/2400) + 1
	if blocks < 1 || blocks > 40 {
		t.Fatalf("几何异常：base=%d blocks=%d", base, blocks)
	}
	for i := 0; i < blocks; i++ {
		seedBotAssistantToolBlock(t, env, bot.ID, fmt.Sprintf("call-fold-%d", i), "fs_issue", strings.Repeat("很长的工具结果", 400))
	}
	seedBotUserMessage(t, env, bot.ID, "保护轮A")
	seedBotUserMessage(t, env, bot.ID, "保护轮B")
	seedBotUserMessage(t, env, bot.ID, "保护轮C")
	markHistoryConsumed(t, env, botRow, nil)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "timeout-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })

	var runRow model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&runRow)
	if runRow.Status != model.BotRunStatusCompleted {
		t.Fatalf("探测失败不得把输入判死: %s err=%+v", runRow.Status, runRow.ErrorMessage)
	}
	// 失败标记落库（短 TTL 负缓存），且不参与能力解析。
	if row := findWindowRow(t, env, srv.URL, "fake-model"); row == nil || row.ContextWindow != 0 {
		t.Fatalf("探测超时应落失败标记: %+v", row)
	}
	// 压缩按回退窗口触发并成功发布（折叠即可达）。
	var comp model.BotCompaction
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&comp)
	if comp.Status != model.BotCompactionFoldOnly && comp.Status != model.BotCompactionSucceeded {
		t.Fatalf("回退窗口下折叠压缩应成功: %s code=%+v", comp.Status, comp.ErrorCode)
	}
	var lifecycle int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key LIKE ?", bot.ID, "compaction_failed:%").Count(&lifecycle)
	if lifecycle != 0 {
		t.Fatalf("不应有压缩失败消息: %d", lifecycle)
	}
}

// ---------- F10：失败负缓存 ----------

func TestBotWindowFailureMarkerShortTTL(t *testing.T) {
	env := setupBotTestEnv(t)
	// 无窗口字段的上游：探测注定失败。
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","owned_by":"x"}]}`)
	userID := createBotTestUser(t, env.db, "winneg")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, bot)

	fallbackTrigger := botFloorRatio(env.bots.inputBudgetFor(bot, "m1"), env.cfg.Bots.Context.CompressionRatioV())
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if row := findWindowRow(t, env, ps.URL(), "m1"); row == nil || row.ContextWindow != 0 {
		t.Fatalf("探测失败应落负缓存标记: %+v", row)
	}
	// 短 TTL 内不再白探。
	hits := ps.hitCount()
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if h := ps.hitCount(); h != hits {
		t.Fatalf("负缓存期内不应重探: %d → %d", hits, h)
	}
	// 超过短 TTL：允许重试。
	stale := time.Now().UTC().Add(-botWindowFailedTTL - time.Minute)
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "m1",
		ContextWindow: 0, Source: "detect_failed", Detail: "seed",
		FetchedAt: stale, UpdatedAt: stale,
	}); err != nil {
		t.Fatalf("seed stale marker: %v", err)
	}
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if h := ps.hitCount(); h != hits+1 {
		t.Fatalf("负缓存过期后应允许重试: %d → %d", hits, h)
	}
}

// ---------- 第五轮复审：成功行失败退避 ----------

// TestBotEnsureModelWindowSuccessRowFailedBackoff（第五轮复审）：成功行
// fetched_at 过期后探测失败，只记失败退避时间——退避期内高上下文工具轮不再
// 反复付 5s 同步探测，成功数据原样供用；退避过期后允许重试，失败只刷新退避，
// 成功探测刷新缓存并清除退避。
func TestBotEnsureModelWindowSuccessRowFailedBackoff(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winbackoff")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, bot)

	// 首探成功，再把行做成「数据过期 + 刚失败过」：fetched_at 25h 前、
	// failed_at 1 分钟前。
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("首探应成功: %v", err)
	}
	stale := time.Now().UTC().Add(-25 * time.Hour)
	recentFail := time.Now().UTC().Add(-time.Minute)
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "m1",
		ContextWindow: 1048576, MaxOutputTokens: 262144,
		Source: "detected", Detail: "GET /models",
		FetchedAt: stale, FailedAt: &recentFail, UpdatedAt: stale,
	}); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}

	// 估算已达触发阈值（会走同步探测的路径）：退避期内不打网络。
	fallbackTrigger := botFloorRatio(env.bots.inputBudgetFor(bot, "m1"), env.cfg.Bots.Context.CompressionRatioV())
	hits := ps.hitCount()
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if h := ps.hitCount(); h != hits {
		t.Fatalf("失败退避期内不应重复同步探测: %d → %d", hits, h)
	}
	// 成功数据原样：窗口/输出不变，能力解析仍用成功缓存。
	row := findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 {
		t.Fatalf("退避不得破坏成功缓存: %+v", row)
	}
	if cap := env.bots.modelCapability(bot, "m1"); cap.Source != "detected" || cap.WindowTokens != 1048576 {
		t.Fatalf("能力解析应仍用成功缓存: %+v", cap)
	}

	// 退避过期（11 分钟前）：允许重试；上游仍失败 → 只刷新退避时间，成功
	// 数据（窗口/输出/fetched_at）保持原值。
	oldFail := time.Now().UTC().Add(-botWindowFailedTTL - time.Minute)
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "m1",
		ContextWindow: 1048576, MaxOutputTokens: 262144,
		Source: "detected", Detail: "GET /models",
		FetchedAt: stale, FailedAt: &oldFail, UpdatedAt: stale,
	}); err != nil {
		t.Fatalf("re-seed expired backoff: %v", err)
	}
	ps.setModels(500, `{"error":"boom"}`)
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if h := ps.hitCount(); h != hits+1 {
		t.Fatalf("退避过期后应允许同步探测: %d → %d", hits, h)
	}
	row = findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 ||
		row.FetchedAt.Sub(stale).Abs() > time.Second {
		t.Fatalf("失败探测不得动成功数据，只记退避: %+v", row)
	}
	if row == nil || row.FailedAt == nil || time.Since(*row.FailedAt) > botWindowFailedTTL {
		t.Fatalf("失败探测应刷新退避时间: %+v", row)
	}

	// 上游恢复：把退避重新拨到过期（新退避本会正确拦住紧接的重试），再次
	// 同步探测成功 → 缓存刷新，failed_at 清除（Upsert 写 NULL）。
	ps.setModels(200, `{"data":[{"id":"m1","context_window":1048576,"max_output_tokens":262144}]}`)
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "m1",
		ContextWindow: 1048576, MaxOutputTokens: 262144,
		Source: "detected", Detail: "GET /models",
		FetchedAt: stale, FailedAt: &oldFail, UpdatedAt: stale,
	}); err != nil {
		t.Fatalf("re-seed expired backoff for success path: %v", err)
	}
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	row = findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.FailedAt != nil {
		t.Fatalf("成功探测应清除失败退避: failed_at=%+v", row)
	}
	if row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 || time.Since(row.FetchedAt) > time.Hour {
		t.Fatalf("成功探测应刷新缓存: %+v", row)
	}
	// 新鲜缓存不再打网络。
	hits = ps.hitCount()
	env.bots.ensureModelWindow(bot, run, "sk-window-4321", fallbackTrigger)
	if h := ps.hitCount(); h != hits {
		t.Fatalf("成功缓存新鲜期内不应再探测: %d → %d", hits, h)
	}
}

// ---------- F8：阈值压缩软失败 run 内不重试 ----------

// writeFakeChatSSEToolCall 回一帧带工具调用的 SSE。
func writeFakeChatSSEToolCall(w http.ResponseWriter, callID, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	chunk := func(delta map[string]interface{}, finish string) {
		raw, _ := json.Marshal(map[string]interface{}{
			"id": "chatcmpl-test", "object": "chat.completion.chunk",
			"created": 1700000000, "model": "fake-model",
			"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}
	chunk(map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{
		"index": 0, "id": callID, "type": "function",
		"function": map[string]string{"name": name, "arguments": args},
	}}}, "")
	chunk(map[string]interface{}{}, "tool_calls")
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func TestBotThresholdCompactionSoftFailNotRetried(t *testing.T) {
	var archiveN, summaryN, mainN int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, `"tools"`):
			mainN++
			if mainN == 1 {
				writeFakeChatSSEToolCall(w, "call-soft-1", "fs_project", `{"action":"list","page":1,"page_size":10}`)
				return
			}
			writeFakeChatSSE(w, "已处理完成。")
		case strings.Contains(s, "记忆归档器"):
			archiveN++
			writeFakeChatSSE(w, `{"items":[]}`)
		case strings.Contains(s, "上下文压缩器"):
			// 确定性摘要失败：每轮工具循环都不该再试。
			summaryN++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"summary_upstream_down"}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"script_exhausted"}}`)
		}
	}))
	t.Cleanup(srv.Close)

	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "softfail")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "soft", BaseURL: srv.URL, Model: "m", APIKey: "sk-x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 60000
	botRow, _ := env.bots.bots.Get(bot.ID)

	// 账本按当前前缀体积定制：估算落在 [触发阈值, 窗口) 的软区间，且没有
	// 可折叠行（纯用户内容）——摘要失败即周期失败。大账本排在保护用户之前，
	// 这样它会进入摘要段而不是保留区（否则前置检查会判不可达）。
	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	probeRun := botFixtureRun(t, env, botRow)
	base := env.bots.estimateProjectionTokens(botRow, probeRun, nil)
	env.db.Where("id = ?", probeRun.ID).Delete(&model.BotRun{})
	window := env.bots.inputBudgetFor(botRow, "m")
	trigger := botFloorRatio(window, env.cfg.Bots.Context.CompressionRatioV())
	bigTokens := trigger + (window-trigger)/2 - base
	if bigTokens < 1000 {
		t.Fatalf("几何异常：base=%d bigTokens=%d", base, bigTokens)
	}
	// 「很长的历史内容。」8字×3字节=24字节 ≈ 6.86 token/次。
	seedBotUserMessage(t, env, bot.ID, strings.Repeat("很长的历史内容。", int(bigTokens*7/48)+1))
	seedBotUserMessage(t, env, bot.ID, "保护轮A")
	seedBotUserMessage(t, env, bot.ID, "保护轮B")
	seedBotUserMessage(t, env, bot.ID, "保护轮C")
	markHistoryConsumed(t, env, botRow, nil)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "soft-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })

	var runRow model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&runRow)
	if runRow.Status != model.BotRunStatusCompleted {
		t.Fatalf("阈值软失败应放行主请求并正常收尾: %s err=%+v", runRow.Status, runRow.ErrorMessage)
	}
	// 回归断言：一次 run 只发一次归档 + 一次摘要，不随工具轮次重试。
	if archiveN != 1 || summaryN != 1 {
		t.Fatalf("阈值压缩软失败后不得重试: archive=%d summary=%d", archiveN, summaryN)
	}
	if mainN != 2 {
		t.Fatalf("应有两次主线请求（工具轮+收尾轮）: %d", mainN)
	}
	var maintReqs []model.BotMaintenanceRequest
	env.db.Where("maintenance_run_id IN (SELECT id FROM bot_maintenance_runs WHERE bot_id = ?)", bot.ID).
		Order("ordinal ASC").Find(&maintReqs)
	arch, sum := 0, 0
	for _, r := range maintReqs {
		switch r.Purpose {
		case model.BotPurposeArchive:
			arch++
		case model.BotPurposeSummary:
			sum++
		}
	}
	if arch != 1 || sum != 1 {
		t.Fatalf("维护账本应恰好 1 归档 1 摘要: arch=%d sum=%d", arch, sum)
	}
	var comp model.BotCompaction
	env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&comp)
	if comp.ErrorCode == nil || *comp.ErrorCode != "context_compaction_failed" {
		t.Fatalf("应记 context_compaction_failed: %+v", comp.ErrorCode)
	}
}

// ---------- F9：摘要输出上限封顶 ----------

func TestBotSummaryOutputCappedByDetectedLimit(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "滚动摘要：用户在做机器人主线。"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "sumcap")
	bot := createBotRowAt(t, env, userID, env.fake.URL(), "fake-model")
	// 探测结果：窗口 128k、输出上限 8k。
	if err := env.bots.persistModelWindow(env.fake.URL(), "fake-model", &botModelCapability{
		WindowTokens: 131072, MaxOutputTokens: 8192, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	env.cfg.Bots.Context.WindowTokens = 131072
	seedCompactableLedger(t, env, bot.ID, strings.Repeat("很长的旧历史。", 8000))
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	if auxCap := env.bots.modelCapability(botRow, "fake-model").MaxOutputTokens; auxCap != 8192 {
		t.Fatalf("前置：应解析到输出上限 8192，实得 %d", auxCap)
	}
	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil || !out.Published {
		t.Fatalf("封顶不应破坏可达压缩: %v %+v", err, out)
	}
	// 摘要请求的 max_completion_tokens 不得超过探测到的输出上限。
	var summaryBody []byte
	for i := 0; i < 4; i++ {
		req := env.fake.nextRequest(t, 5*time.Second)
		if strings.Contains(string(req.Body), "上下文压缩器") {
			summaryBody = req.Body
			break
		}
	}
	if summaryBody == nil {
		t.Fatalf("未捕获摘要请求")
	}
	var payload struct {
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(summaryBody, &payload); err != nil {
		t.Fatalf("解析摘要请求: %v", err)
	}
	if payload.MaxCompletionTokens == 0 {
		t.Fatalf("摘要请求应带输出上限字段: %s", summaryBody)
	}
	if payload.MaxCompletionTokens > 8192 {
		t.Fatalf("max_completion_tokens 应封顶 8192，实得 %d", payload.MaxCompletionTokens)
	}
	outBudget := env.bots.summaryOutputBudget(botRow, planOfCycle(t, env, botRow, run))
	if outBudget <= 8192 {
		t.Fatalf("本场景前提：未封顶预算应超上限 outBudget=%d", outBudget)
	}
}

// planOfCycle 供断言复算输出预算（与 requestSummary 同一 plan 口径）。
func planOfCycle(t *testing.T, env *botTestEnv, botRow *model.Bot, run *model.BotRun) *botCompactionPlan {
	t.Helper()
	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionThreshold)
	if err != nil {
		t.Fatalf("plan for assert: %v", err)
	}
	return plan
}

// ---------- F5 / F11：指标口径 ----------

func TestBotMetricsUseRunSnapshotAddress(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "winmetrics-addr")
	// bot 当前地址是 B；run 快照地址是 A。同名模型、两个地址缓存不同窗口。
	const addrA = "https://svc-a.example.com/v1"
	const addrB = "https://svc-b.example.com/v1"
	bot := createBotRowAt(t, env, userID, addrB, "m1")
	if err := env.bots.persistModelWindow(addrA, "m1", &botModelCapability{
		WindowTokens: 1048576, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := env.bots.persistModelWindow(addrB, "m1", &botModelCapability{
		WindowTokens: 4096, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "metrics-addr",
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: fmt.Sprintf(`{"model":"m1","base_url":%q}`, addrA),
		CreatedAt:          now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := env.bots.requests.CreateTx(env.db, &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, InputTokensEstimate: 3000,
		InputEstimator: "utf8_bytes/3.5", StartedAt: now,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
	}); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Metrics == nil {
		t.Fatalf("应有 metrics")
	}
	// 切换服务地址后，旧请求的窗口必须按它发起时的地址解析。
	if snap.Metrics.ContextWindow != 1048576 || snap.Metrics.ContextSource != "detected" {
		t.Fatalf("指标应用 run 快照地址的窗口: %+v", snap.Metrics)
	}
}

func TestBotChatSnapshotMetricsWithoutRequest(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"fake-model","context_window":4096}]}`)
	userID := createBotTestUser(t, env.db, "winmetrics3")
	bot := createBotRowAt(t, env, userID, ps.URL(), "fake-model")
	if err := env.bots.persistModelWindow(ps.URL(), "fake-model", &botModelCapability{
		WindowTokens: 4096, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// 全新 bot（没有任何主线请求）：metrics 仍产出，Request 为 nil，
	// 已探测到的窗口可见。
	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Metrics == nil {
		t.Fatalf("无请求时也应产出 metrics")
	}
	if snap.Metrics.Request != nil {
		t.Fatalf("无请求时 Request 应为 nil: %+v", snap.Metrics.Request)
	}
	if snap.Metrics.ContextWindow != 4096 || snap.Metrics.ContextSource != "detected" {
		t.Fatalf("窗口能力应来自已探测缓存: %+v", snap.Metrics)
	}
}

// ---------- F1：auxModelRequest 发送关口 ----------

// seedMaintRun 直接落一行维护运行（无触发键语义），供 auxModelRequest 记账。
func seedMaintRun(t *testing.T, env *botTestEnv, botID string) *model.BotMaintenanceRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID, Kind: model.BotMaintenanceNightly,
		TriggerKey: "aux-gate-" + uuid.NewString()[:8], Status: model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := repository.NewBotMaintenanceRepository(env.db).CreateTx(env.db, run); err != nil {
		t.Fatalf("seed maint run: %v", err)
	}
	return run
}

// TestAuxModelRequestCapsOutputToDetectedLimit（F1）：即使调用方给超大 maxOut，
// 实际发出的 max_completion_tokens 也被探测上限封顶。
func TestAuxModelRequestCapsOutputToDetectedLimit(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok"})
	userID := createBotTestUser(t, env.db, "auxcap")
	bot := createBotRowAt(t, env, userID, env.fake.URL(), "fake-model")
	if err := env.bots.persistModelWindow(env.fake.URL(), "fake-model", &botModelCapability{
		WindowTokens: 65536, MaxOutputTokens: 512, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	maintRun := seedMaintRun(t, env, bot.ID)

	if _, err := env.bots.auxModelRequest(context.Background(), bot, maintRun,
		model.BotPurposeSummary, "fake-model",
		[]openaiMessage{{Role: "user", Content: "hi"}}, 100000); err != nil {
		t.Fatalf("封顶后应能发出请求: %v", err)
	}
	req := env.fake.nextRequest(t, 5*time.Second)
	var payload struct {
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("解析请求: %v", err)
	}
	if payload.MaxCompletionTokens != 512 {
		t.Fatalf("max_completion_tokens 应封顶到探测上限 512，实得 %d", payload.MaxCompletionTokens)
	}
}

// TestAuxModelRequestRejectsOverWindowInput（F1）：输入+输出超窗时干净失败——
// 不建维护账本行、更没有请求出网。
func TestAuxModelRequestRejectsOverWindowInput(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "auxover")
	bot := createBotRowAt(t, env, userID, env.fake.URL(), "fake-model")
	if err := env.bots.persistModelWindow(env.fake.URL(), "fake-model", &botModelCapability{
		WindowTokens: 512, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	maintRun := seedMaintRun(t, env, bot.ID)

	// 超窗输入 + 超大 maxOut 同时给：关口都必须拦下。
	big := strings.Repeat("很长的输入。", 4000)
	_, err := env.bots.auxModelRequest(context.Background(), bot, maintRun,
		model.BotPurposeSummary, "fake-model",
		[]openaiMessage{{Role: "user", Content: big}}, 100000)
	if err == nil || !errors.Is(err, errAuxInputOverWindow) {
		t.Fatalf("超窗输入应被发送关口拒绝: %v", err)
	}
	var n int64
	env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maintRun.ID).Count(&n)
	if n != 0 {
		t.Fatalf("被拒请求不得进维护账本: %d", n)
	}
	select {
	case r := <-env.fake.requests:
		t.Fatalf("超窗请求不得出网: %s", truncateRunes(string(r.Body), 200))
	default:
	}
}

// ---------- F2：静默归档纳入能力预算 ----------

// TestSilentArchiveUsesCapabilityBudgets（F2）：归档请求按归档模型能力定价，
// 输出向探测上限封顶（旧实现硬编码 1024 会直接越过 512 上限）。
func TestSilentArchiveUsesCapabilityBudgets(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: `{"items":[]}`},
		fakeModelTurn{Text: "滚动摘要：用户在做机器人主线。"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "archcap")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 30000
	env.cfg.Bots.Context.ArchiveModel = "arch-model"
	if err := env.bots.persistModelWindow(env.fake.URL(), "arch-model", &botModelCapability{
		WindowTokens: 65536, MaxOutputTokens: 512, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed arch cache: %v", err)
	}
	seedCompactableLedger(t, env, bot.ID, strings.Repeat("很长的旧历史。", 1200))
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	out, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold)
	if err != nil || !out.Published {
		t.Fatalf("归档预算不应破坏压缩: %v %+v", err, out)
	}
	req := env.fake.nextRequest(t, 5*time.Second)
	if !strings.Contains(string(req.Body), "记忆归档器") {
		t.Fatalf("第一个请求应为归档: %s", truncateRunes(string(req.Body), 200))
	}
	var payload struct {
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("解析归档请求: %v", err)
	}
	if payload.MaxCompletionTokens != 512 {
		t.Fatalf("归档输出应封顶到探测上限 512，实得 %d", payload.MaxCompletionTokens)
	}
	arch, _ := countMaintByPurpose(env.db, out.Run.ID)
	if arch != 1 {
		t.Fatalf("应恰好一次归档请求: %d", arch)
	}
}

// TestSilentArchiveOverWindowSkipsRequest（F2）：归档窗口装不下时记
// over_window 正常返回——归档尽力而为，不发注定超窗的请求，也不阻塞压缩。
func TestSilentArchiveOverWindowSkipsRequest(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "滚动摘要：用户在做机器人主线。"},
	)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "archover")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 30000
	env.cfg.Bots.Context.ArchiveModel = "arch-model"
	// 归档模型窗口很小：装不下「输入+输出」。
	if err := env.bots.persistModelWindow(env.fake.URL(), "arch-model", &botModelCapability{
		WindowTokens: 8192, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed arch cache: %v", err)
	}
	seedCompactableLedger(t, env, bot.ID, strings.Repeat("很长的旧历史。", 1200))
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	if _, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionThreshold); err != nil {
		t.Fatalf("归档超窗不得阻塞压缩主线: %v", err)
	}
	var maint model.BotMaintenanceRun
	env.db.Where("bot_id = ? AND kind = ?", bot.ID, model.BotMaintenanceCompaction).
		Order("created_at DESC").First(&maint)
	if !strings.Contains(maint.ResultJSON, "over_window") {
		t.Fatalf("归档超窗应记 over_window: %s", maint.ResultJSON)
	}
	arch, sum := countMaintByPurpose(env.db, maint.ID)
	if arch != 0 {
		t.Fatalf("超窗归档不得发出请求: arch=%d", arch)
	}
	if sum != 1 {
		t.Fatalf("摘要应照常发出: sum=%d", sum)
	}
	// 出网的第一条请求应是摘要，不是归档。
	req := env.fake.nextRequest(t, 5*time.Second)
	if strings.Contains(string(req.Body), "记忆归档器") {
		t.Fatalf("超窗归档不得出网: %s", truncateRunes(string(req.Body), 200))
	}
}

// ---------- F3：夜间整理输出封顶 ----------

// TestBotNightlyOutputCappedByDetectedLimit（F3）：夜间输出意向 4096 向探测
// 上限封顶——MaxOutputTokens < 4096 的上游不再每晚带着超限 max_completion_tokens。
func TestBotNightlyOutputCappedByDetectedLimit(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: `{"user_md":"用户叫 Bobo","memory_md":"项目天狼星","sources":[{"path":"daily/2026-10-15.md","version":1}],"note":"ok"}`})
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "nightcap")
	bot := env.createBot(t, userID)
	writeBotMemory(t, env, bot.ID, "daily/2026-10-15.md", "- 用户叫 Bobo\n- 项目天狼星", "n-cap")
	env.cfg.Bots.Memory.NightlyModel = "cheap-nightly"
	if err := env.bots.persistModelWindow(env.fake.URL(), "cheap-nightly", &botModelCapability{
		WindowTokens: 65536, MaxOutputTokens: 1024, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed nightly cache: %v", err)
	}
	env.bots.nowFn = func() time.Time {
		return time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	env.bots.runNightlyForBot(botRow.ID, "nightly:2026-10-16")

	var run model.BotMaintenanceRun
	env.db.Where("bot_id = ? AND trigger_key = ?", bot.ID, "nightly:2026-10-16").First(&run)
	if run.Status != model.BotMaintenanceSucceeded {
		t.Fatalf("夜间运行应成功: %s %v", run.Status, run.ErrorCode)
	}
	req := env.fake.nextRequest(t, 5*time.Second)
	var payload struct {
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("解析夜间请求: %v", err)
	}
	if payload.MaxCompletionTokens != 1024 {
		t.Fatalf("夜间输出应封顶到探测上限 1024，实得 %d", payload.MaxCompletionTokens)
	}
}

// ---------- F4：manual 覆盖保留 detected 输出上限 ----------

// TestBotWindowManualKeepsDetectedOutputLimit（F4）：manual 是字段级覆盖——
// 窗口取部署值，已探测到的输出上限仍生效（输入预算预留输出空间、辅助封顶）。
func TestBotWindowManualKeepsDetectedOutputLimit(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"m1","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winmanual2")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	env.cfg.Bots.Context.ModelWindows = map[string]int64{"m1": 2000000}
	cap := env.bots.modelCapability(bot, "m1")
	if cap.Source != "manual" || cap.WindowTokens != 2000000 || cap.MaxOutputTokens != 262144 || cap.Detected {
		t.Fatalf("manual 窗口应保留 detected 输出上限: %+v", cap)
	}
	if got := env.bots.inputBudgetFor(bot, "m1"); got != 2000000-262144 {
		t.Fatalf("inputBudgetFor 应为 manual 窗口−输出上限，实得 %d", got)
	}
	// 辅助封顶生效：以同一份能力封顶输出。
	reqOut, _, ok := botAuxBudgets(cap.WindowTokens, cap.MaxOutputTokens, 500000, 64)
	if !ok || reqOut != 262144 {
		t.Fatalf("辅助输出应向上限封顶: reqOut=%d ok=%v", reqOut, ok)
	}
}

// TestAuxGateUsesManualWindowAndDetectedCap（F4+F1 组合）：manual 窗口与
// detected 输出上限共存时，发送关口按同一份能力封顶实际请求。
func TestAuxGateUsesManualWindowAndDetectedCap(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok"})
	userID := createBotTestUser(t, env.db, "auxmanual")
	bot := createBotRowAt(t, env, userID, env.fake.URL(), "fake-model")
	if err := env.bots.persistModelWindow(env.fake.URL(), "fake-model", &botModelCapability{
		WindowTokens: 1048576, MaxOutputTokens: 262144, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	env.cfg.Bots.Context.ModelWindows = map[string]int64{"fake-model": 2000000}
	maintRun := seedMaintRun(t, env, bot.ID)

	if _, err := env.bots.auxModelRequest(context.Background(), bot, maintRun,
		model.BotPurposeSummary, "fake-model",
		[]openaiMessage{{Role: "user", Content: "hi"}}, 500000); err != nil {
		t.Fatalf("manual 窗口+detected 上限应共存: %v", err)
	}
	req := env.fake.nextRequest(t, 5*time.Second)
	var payload struct {
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("解析请求: %v", err)
	}
	if payload.MaxCompletionTokens != 262144 {
		t.Fatalf("manual 窗口下输出仍应封顶到 detected 上限 262144，实得 %d", payload.MaxCompletionTokens)
	}
}

// ---------- F6：/models 列表内全部模型落缓存 ----------

// TestBotWindowDetectCachesAllModels（F6）：一次探测把列表内每个模型
// （ContextWindow>0）都落缓存——配置的辅助模型也能拿到输出上限，不再永远
// 走 fallback；缺失窗口字段的条目不得落成 0 行。
func TestBotWindowDetectCachesAllModels(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200,
		`{"data":[{"id":"m1","context_window":1048576,"max_output_tokens":262144},`+
			`{"id":"aux-1","context_window":131072,"max_output_tokens":8192},`+
			`{"id":"no-window","owned_by":"x"}]}`)
	userID := createBotTestUser(t, env.db, "winall")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	row := findWindowRow(t, env, ps.URL(), "m1")
	if row == nil || row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 {
		t.Fatalf("主模型缓存行不符: %+v", row)
	}
	aux := findWindowRow(t, env, ps.URL(), "aux-1")
	if aux == nil || aux.ContextWindow != 131072 || aux.MaxOutputTokens != 8192 || aux.Source != "detected" {
		t.Fatalf("辅助模型也应落缓存: %+v", aux)
	}
	if noWin := findWindowRow(t, env, ps.URL(), "no-window"); noWin != nil {
		t.Fatalf("缺失窗口字段的条目不得落成 0 行: %+v", noWin)
	}
	capability := env.bots.modelCapability(bot, "aux-1")
	if capability.Source != "detected" || capability.WindowTokens != 131072 || capability.MaxOutputTokens != 8192 {
		t.Fatalf("辅助模型能力解析应命中缓存: %+v", capability)
	}
}

// ---------- 第六轮复审 H1：大小写折叠的统一查找口径 ----------

// TestBotWindowAuxModelCaseVariantHitsCache（H1）：缓存里只有列表原样名 M1
// （主模型精确同名，别名只为主请求名而落）时，配置成大小写变体 m1 的辅助
// 模型此前按精确名查不到、永远走 fallback。统一折叠查找后 capabilityFor 命中
// detected（含 manual 覆盖窗口时沿用变体行的输出上限），ensureModelWindow 也
// 视其为新鲜不再探测。
func TestBotWindowAuxModelCaseVariantHitsCache(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200,
		`{"data":[{"id":"M1","context_window":65536,"max_output_tokens":16384}]}`)
	userID := createBotTestUser(t, env.db, "winauxfold")
	bot := createBotRowAt(t, env, userID, ps.URL(), "M1")

	// 主模型精确同名：只落一行 M1——后续变体查找只能靠折叠口径命中。
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "M1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var count int64
	env.db.Model(&model.BotModelWindow{}).
		Where("base_url = ?", botNormalizeBaseURL(ps.URL())).Count(&count)
	if count != 1 {
		t.Fatalf("精确同名探测应只有一行缓存，实得 %d", count)
	}

	// 辅助模型配置成大小写变体 m1：命中 detected，窗口/上限正确。
	cap := env.bots.capabilityFor(ps.URL(), "m1")
	if cap.Source != "detected" || cap.WindowTokens != 65536 || cap.MaxOutputTokens != 16384 {
		t.Fatalf("大小写变体应命中 detected 缓存: %+v", cap)
	}
	// manual 覆盖窗口时，变体行里的输出上限仍生效。
	env.cfg.Bots.Context.ModelWindows = map[string]int64{"m1": 100000}
	if cap := env.bots.capabilityFor(ps.URL(), "m1"); cap.Source != "manual" ||
		cap.WindowTokens != 100000 || cap.MaxOutputTokens != 16384 {
		t.Fatalf("manual 覆盖下变体行输出上限应生效: %+v", cap)
	}
	delete(env.cfg.Bots.Context.ModelWindows, "m1")

	// ensureModelWindow 对变体也视为新鲜：不再探测。
	run := botFixtureRun(t, env, bot)
	fallbackTrigger := botFloorRatio(env.bots.inputBudgetFor(bot, "m1"), env.cfg.Bots.Context.CompressionRatioV())
	env.bots.ensureModelWindow(bot, run, "sk-x", fallbackTrigger)
	if h := ps.hitCount(); h != 1 {
		t.Fatalf("新鲜缓存的大小写变体不应重探，实得 %d 次探测", h)
	}
}

// TestBotWindowFailureMarkerDoesNotShadowCaseVariantSuccess（H1）：同地址既有
// m1 的失败标记、又有 M1 的成功缓存时，按 m1 解析能力必须优先成功行而非退回
// fallback；M1 成功行数据过期后，新鲜度也按成功行判断（允许重探），而不是被
// m1 失败标记的负缓存拦住。
func TestBotWindowFailureMarkerDoesNotShadowCaseVariantSuccess(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200,
		`{"data":[{"id":"M1","context_window":65536,"max_output_tokens":16384}]}`)
	userID := createBotTestUser(t, env.db, "winshadow")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, bot)

	// M1 曾被精确探测成功；随后 m1 探测失败只落 m1 失败标记。
	if err := env.bots.persistModelWindow(ps.URL(), "M1", &botModelCapability{
		WindowTokens: 65536, MaxOutputTokens: 16384, Source: "detected", Detail: "GET /models",
	}); err != nil {
		t.Fatalf("seed M1: %v", err)
	}
	ps.setModels(500, `{"error":"boom"}`)
	if err := env.bots.refreshModelWindow(context.Background(), ps.URL(), "sk-x", "m1"); err == nil {
		t.Fatalf("第二次 refresh 应失败")
	}
	if row := findWindowRow(t, env, ps.URL(), "m1"); row == nil || row.ContextWindow != 0 {
		t.Fatalf("m1 失败标记应落库: %+v", row)
	}

	// 失败标记不得遮蔽变体成功行。
	if cap := env.bots.capabilityFor(ps.URL(), "m1"); cap.Source != "detected" ||
		cap.WindowTokens != 65536 || cap.MaxOutputTokens != 16384 {
		t.Fatalf("失败行不得遮蔽变体成功行: %+v", cap)
	}

	// M1 成功行数据过期（fetched_at 25h 前）后：新鲜度按成功行判断，估算达到
	// 触发阈值 → 允许重探；若被 m1 失败标记的负缓存拦住则不会发探测。
	stale := time.Now().UTC().Add(-25 * time.Hour)
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "M1",
		ContextWindow: 65536, MaxOutputTokens: 16384,
		Source: "detected", Detail: "GET /models",
		FetchedAt: stale, UpdatedAt: stale,
	}); err != nil {
		t.Fatalf("stale M1: %v", err)
	}
	fallbackTrigger := botFloorRatio(env.bots.inputBudgetFor(bot, "m1"), env.cfg.Bots.Context.CompressionRatioV())
	hits := ps.hitCount()
	env.bots.ensureModelWindow(bot, run, "sk-x", fallbackTrigger)
	if h := ps.hitCount(); h != hits+1 {
		t.Fatalf("变体成功行过期后应允许重探: %d → %d", hits, h)
	}
}

// TestBotEnsureModelWindowCaseFoldedBackoff 覆盖 case-folded 成功行的失败退避：
// 配置 m1、缓存成功行只以列表原名 M1 存在时，探测失败必须给 M1 行记上退避，
// 否则折叠查找继续选中无退避的成功行，每个高上下文工具轮重复同步探测。
func TestBotEnsureModelWindowCaseFoldedBackoff(t *testing.T) {
	env := setupBotTestEnv(t)
	ps := newWindowProbeServer(t, 200, `{"data":[{"id":"M1","context_window":1048576,"max_output_tokens":262144}]}`)
	userID := createBotTestUser(t, env.db, "winfoldbackoff")
	bot := createBotRowAt(t, env, userID, ps.URL(), "m1")
	run := botFixtureRun(t, env, bot)

	stale := time.Now().UTC().Add(-25 * time.Hour)
	if err := env.bots.windows.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: botNormalizeBaseURL(ps.URL()), Model: "M1",
		ContextWindow: 1048576, MaxOutputTokens: 262144,
		Source: "detected", Detail: "GET /models",
		FetchedAt: stale, UpdatedAt: stale,
	}); err != nil {
		t.Fatalf("seed stale M1 row: %v", err)
	}

	// 探测失败 + 输入达到回退触发阈值 → 走有界同步探测分支。
	ps.setModels(500, `{"error":"boom"}`)
	estimate := env.bots.inputBudgetFor(bot, "m1") + 1
	env.bots.ensureModelWindow(bot, run, "sk-x", estimate)

	folded, err := env.bots.windows.FindByAddrModel(botNormalizeBaseURL(ps.URL()), "M1")
	if err != nil || folded.FailedAt == nil {
		t.Fatalf("折叠成功行应记录失败退避: %+v err=%v", folded, err)
	}
	// 退避期内不再同步探测。
	hits := ps.hitCount()
	env.bots.ensureModelWindow(bot, run, "sk-x", estimate)
	if ps.hitCount() != hits {
		t.Fatalf("退避期内不应再同步探测: %d → %d", hits, ps.hitCount())
	}
}
