package service

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"github.com/openai/openai-go/v2/packages/respjson"
)

// INT-164 请求级字段：推理 token（completion_tokens_details.reasoning_tokens）
// 与供应商实际金额（provider_cost + 币别置信）。适配层解析、两表落库、
// 端到端请求行各一组；投影口径在 bot_int164_projection_test.go。

func int164Usage(extra map[string]interface{}) map[string]interface{} {
	usage := map[string]interface{}{
		"prompt_tokens":     int64(1000),
		"completion_tokens": int64(200),
		"total_tokens":      int64(1200),
	}
	for k, v := range extra {
		usage[k] = v
	}
	return usage
}

func TestBotStreamChatReasoningTokens(t *testing.T) {
	t.Run("返回时有值", func(t *testing.T) {
		f := newFakeModelServer(t, fakeModelTurn{Text: "ok", RawUsage: int164Usage(map[string]interface{}{
			"completion_tokens_details": map[string]interface{}{"reasoning_tokens": 42},
		})})
		client := botNewClient(f.URL(), "sk-test-abcdef123456")
		reply, err := botStreamChat(context.Background(), &client, "https://api.example.com/v1",
			int137DirectRequest(), botStreamTimeouts{}, nil)
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if !reply.UsageComplete || reply.ReasoningTokens == nil || *reply.ReasoningTokens != 42 {
			t.Fatalf("reasoning_tokens 应解析为 42: %+v", reply)
		}
	})
	t.Run("未返回为 nil 缺失不等于零", func(t *testing.T) {
		f := newFakeModelServer(t, fakeModelTurn{Text: "ok",
			Usage: &fakeModelUsage{Prompt: 10, Completion: 2, Total: 12}})
		client := botNewClient(f.URL(), "sk-test-abcdef123456")
		reply, err := botStreamChat(context.Background(), &client, "https://api.example.com/v1",
			int137DirectRequest(), botStreamTimeouts{}, nil)
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if reply.ReasoningTokens != nil {
			t.Fatalf("未返回时 reasoning_tokens 应为 nil: %v", *reply.ReasoningTokens)
		}
		if reply.ProviderCost != nil || reply.ProviderCostRaw != nil {
			t.Fatalf("未返回时 provider_cost/raw 应为 nil: %+v", reply)
		}
	})
}

func TestBotStreamChatProviderCost(t *testing.T) {
	t.Run("非 OpenRouter 地址的裸 cost 只记录不置信", func(t *testing.T) {
		f := newFakeModelServer(t, fakeModelTurn{Text: "ok", RawUsage: int164Usage(map[string]interface{}{
			"cost": 0.0134,
		})})
		client := botNewClient(f.URL(), "sk-test-abcdef123456")
		reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1",
			int137DirectRequest(), botStreamTimeouts{}, nil)
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if reply.ProviderCost != nil {
			t.Fatalf("币别不明的 cost 不得进 provider_cost: %+v", reply.ProviderCost)
		}
		if reply.ProviderCostRaw == nil {
			t.Fatalf("币别不明的 cost 应记录进 provider_cost_raw")
		}
	})
	t.Run("非 OpenRouter 的 total_cost 币别不明只记录", func(t *testing.T) {
		f := newFakeModelServer(t, fakeModelTurn{Text: "ok", RawUsage: int164Usage(map[string]interface{}{
			"total_cost": "0.25",
		})})
		client := botNewClient(f.URL(), "sk-test-abcdef123456")
		reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1",
			int137DirectRequest(), botStreamTimeouts{}, nil)
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if reply.ProviderCost != nil {
			t.Fatalf("币别不明的 total_cost 不得进 provider_cost: %+v", reply.ProviderCost)
		}
		if reply.ProviderCostRaw == nil || !strings.Contains(*reply.ProviderCostRaw, "total_cost") {
			t.Fatalf("币别不明的 total_cost 应记录进 provider_cost_raw")
		}
	})
	t.Run("OpenRouter 地址的 cost 置信 USD", func(t *testing.T) {
		f := newFakeModelServer(t, fakeModelTurn{Text: "ok", RawUsage: int164Usage(map[string]interface{}{
			"cost":         "0.0134",
			"cost_details": map[string]interface{}{"upstream_inference_cost": 0.01},
		})})
		client := botNewClient(f.URL(), "sk-test-abcdef123456")
		// client 指向假服务，baseURL 身份串给 OpenRouter：与真实调用同一传参
		// 位置（bot.BaseURL / freeze.BaseURL），此处解耦以便本机模拟。
		reply, err := botStreamChat(context.Background(), &client, "https://openrouter.ai/api/v1",
			int137DirectRequest(), botStreamTimeouts{}, nil)
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if reply.ProviderCost == nil || math.Abs(*reply.ProviderCost-0.0134) > 1e-12 || reply.ProviderCostRaw != nil {
			t.Fatalf("OpenRouter usage.cost 应按 USD 解析: %+v", reply)
		}
	})
}

// 字段并存按固定优先级：cost > total_cost > cost_details.upstream_inference_cost
// > *_usd；解析失败的字段跳过，不吞掉后面可解析的。置信 USD 的值进
// provider_cost；币别不明或非用户实收的金额进 provider_cost_raw。
// cost / total_cost 裸名不带币别声明，仅 OpenRouter 置信；*_usd 名即声明。
func TestBotParseProviderCostPrecedence(t *testing.T) {
	f := func(raw string) respjson.Field { return respjson.NewField(raw) }
	none := map[string]respjson.Field{}
	cases := []struct {
		name       string
		extra      map[string]respjson.Field
		openrouter bool
		wantCost   *float64
		wantRaw    bool
	}{
		{name: "空字段", extra: none},
		{name: "null 与坏值都跳过", extra: map[string]respjson.Field{
			"cost": f("null"), "total_cost": f(`"abc"`), "prompt_usd": f("x"),
		}},
		{name: "NaN/Inf/负数不算金额", extra: map[string]respjson.Field{
			"cost": f("null"), "total_cost": f("NaN"),
			"prompt_usd": f("-0.5"), "upstream_usd": f("Inf"),
		}},
		{name: "非 OR 的 cost 与 total_cost 都币别不明", extra: map[string]respjson.Field{
			"cost": f("0.5"), "total_cost": f("0.9"),
		}, wantRaw: true},
		{name: "非 OR 的 cost 记 raw 且不挡 _usd 字段", extra: map[string]respjson.Field{
			"cost": f("0.5"), "cost_usd": f("0.07"),
		}, wantCost: ptrCost(0.07), wantRaw: true},
		{name: "upstream 记 raw 但不挡 _usd 字段", extra: map[string]respjson.Field{
			"cost_details": f(`{"upstream_inference_cost":0.01}`),
			"cost_usd":     f("0.07"),
		}, wantCost: ptrCost(0.07), wantRaw: true},
		{name: "OR 的 cost 置信 USD", extra: map[string]respjson.Field{
			"cost": f("0.5"), "total_cost": f("0.9"),
		}, openrouter: true, wantCost: ptrCost(0.5)},
		{name: "cost_details 是上游成本非用户实收只进 raw", extra: map[string]respjson.Field{
			"cost_details": f(`{"upstream_inference_cost":0.01}`),
		}, openrouter: true, wantRaw: true},
		{name: "字段名含 _usd 视为 USD", extra: map[string]respjson.Field{
			"total_tokens_billed_usd": f("0.07"),
		}, wantCost: ptrCost(0.07), wantRaw: true},
		{name: "分项 _usd 字段不当总额只进 raw", extra: map[string]respjson.Field{
			"input_cost_usd": f("0.03"), "output_cost_usd": f("0.04"),
		}, wantRaw: true},
		{name: "单价 _usd 字段不当总额只进 raw", extra: map[string]respjson.Field{
			"price_per_token_usd": f("0.0001"),
		}, wantRaw: true},
		{name: "总额 _usd 优先于分项 _usd", extra: map[string]respjson.Field{
			"input_cost_usd": f("0.03"), "cost_usd": f("0.07"),
		}, wantCost: ptrCost(0.07), wantRaw: true},
		{name: "非 _usd 后缀字段不作 USD 候选", extra: map[string]respjson.Field{
			"cost_usd_v2": f("0.5"), "upstream_usd_cost": f("0.5"),
		}},
		{name: "total_ 前缀的分项 _usd 不当总额", extra: map[string]respjson.Field{
			"total_input_cost_usd": f("0.03"), "total_output_cost_usd": f("0.04"),
		}, wantRaw: true},
		{name: "total_ 前缀的上游 _usd 不当总额", extra: map[string]respjson.Field{
			"total_upstream_cost_usd": f("0.03"),
		}, wantRaw: true},
		{name: "OR 的 total_cost 置信 USD 并记来源字段", extra: map[string]respjson.Field{
			"total_cost": f("0.9"),
		}, openrouter: true, wantCost: ptrCost(0.9), wantRaw: true},
		{name: "非 OR 的裸 cost 币别不明", extra: map[string]respjson.Field{
			"cost": f("0.3"),
		}, wantRaw: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, raw := botParseProviderCost(tc.extra, tc.openrouter)
			if (got == nil) != (tc.wantCost == nil) ||
				(got != nil && math.Abs(*got-*tc.wantCost) > 1e-12) {
				t.Fatalf("cost = %v, want %v", got, tc.wantCost)
			}
			if (raw != nil) != tc.wantRaw {
				t.Fatalf("raw = %v, wantRaw %v", raw, tc.wantRaw)
			}
		})
	}
}

func TestBotIsOpenRouterBase(t *testing.T) {
	cases := map[string]bool{
		"https://openrouter.ai/api/v1":       true,
		"https://OPENROUTER.AI/api/v1":       true,
		"https://api.openrouter.ai/v1":       true,
		"https://relay.example.com/v1":       false,
		"https://openrouter.ai.evil.test/v1": false,
		"https://notopenrouter.ai/v1":        false,
		"://bad url":                         false,
	}
	for raw, want := range cases {
		if got := botIsOpenRouterBase(raw); got != want {
			t.Fatalf("botIsOpenRouterBase(%q) = %v, want %v", raw, got, want)
		}
	}
}

// usage 送达后断流的失败请求同样落推理 token 与供应商金额（与 INT-139 的
// 「计费事实与成败无关」同口径）；币别不置信的金额进 provider_cost_raw
// 只记录；字段缺失时各列保持 NULL。
func TestFinishModelRequestUsageFieldsPersisted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int164fin")
	created := env.createBot(t, userID)
	bot, err := env.bots.bots.Get(created.ID)
	if err != nil {
		t.Fatalf("读 bot: %v", err)
	}
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: created.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "int164-" + uuid.NewString(),
		Status: model.BotRunStatusRunning, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	newRow := func(ordinal int64) *model.BotModelRequest {
		row := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: ordinal,
			Status: model.BotModelRequestRunning, StartedAt: now,
			PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
		}
		if err := env.db.Create(row).Error; err != nil {
			t.Fatalf("seed request: %v", err)
		}
		return row
	}

	usd := &botModelReply{UsageComplete: true, PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		ReasoningTokens: ptrInt64(13), ProviderCost: ptrCost(0.5)}
	raw := `{"field":"cost","value":"0.25"}`
	unknown := &botModelReply{UsageComplete: true, PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		ProviderCostRaw: &raw}
	bare := &botModelReply{UsageComplete: true, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}

	cases := []struct {
		name  string
		rowID string
		reply *botModelReply
	}{
		{"USD 置信", newRow(1).ID, usd},
		{"币别不明", newRow(2).ID, unknown},
		{"字段缺失", newRow(3).ID, bare},
	}
	for _, tc := range cases {
		env.bots.finishModelRequest(&model.BotModelRequest{ID: tc.rowID}, tc.reply, nil, run, bot)
	}

	var gotUSD, gotUnknown, gotBare model.BotModelRequest
	if err := env.db.First(&gotUSD, "id = ?", cases[0].rowID).Error; err != nil {
		t.Fatalf("读请求行: %v", err)
	}
	if err := env.db.First(&gotUnknown, "id = ?", cases[1].rowID).Error; err != nil {
		t.Fatalf("读请求行: %v", err)
	}
	if err := env.db.First(&gotBare, "id = ?", cases[2].rowID).Error; err != nil {
		t.Fatalf("读请求行: %v", err)
	}
	if gotUSD.ReasoningTokens == nil || *gotUSD.ReasoningTokens != 13 ||
		gotUSD.ProviderCost == nil || math.Abs(*gotUSD.ProviderCost-0.5) > 1e-12 ||
		gotUSD.ProviderCostRaw != nil {
		t.Fatalf("USD 置信行应落 provider_cost 且 raw 为空: %+v", gotUSD)
	}
	if gotUnknown.ProviderCost != nil || gotUnknown.ProviderCostRaw == nil || *gotUnknown.ProviderCostRaw != raw {
		t.Fatalf("币别不明应只记 provider_cost_raw: %+v", gotUnknown)
	}
	if gotUnknown.ReasoningTokens != nil {
		t.Fatalf("未返回的 reasoning_tokens 应保持 NULL")
	}
	if gotBare.ReasoningTokens != nil || gotBare.ProviderCost != nil || gotBare.ProviderCostRaw != nil {
		t.Fatalf("字段缺失应全空: %+v", gotBare)
	}
}

// 维护路径同口径：推理 token 与 USD 置信金额落 bot_maintenance_requests。
func TestAuxModelRequestUsageFieldsPersisted(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", RawUsage: int164Usage(map[string]interface{}{
		"completion_tokens_details": map[string]interface{}{"reasoning_tokens": 7},
		"cost_usd":                  0.125,
	})})
	userID := createBotTestUser(t, env.db, "int164aux")
	created := env.createBot(t, userID)
	bot, err := env.bots.bots.Get(created.ID)
	if err != nil {
		t.Fatalf("读 bot: %v", err)
	}
	now := time.Now().UTC()
	maint := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: bot.ID,
		Kind: model.BotMaintenanceCompaction, TriggerKey: "int164-" + uuid.NewString(),
		Status:             model.BotMaintenanceRunning,
		ConfigSnapshotJSON: `{"catalog_id":"custom"}`,
		InputSnapshotJSON:  "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(maint).Error; err != nil {
		t.Fatalf("seed maint run: %v", err)
	}
	if _, err := env.bots.auxModelRequest(context.Background(), bot, maint,
		model.BotPurposeSummary, "fake-model",
		[]openaiMessage{{Role: "user", Content: "hi"}}, 256); err != nil {
		t.Fatalf("aux: %v", err)
	}
	var req model.BotMaintenanceRequest
	if err := env.db.Where("maintenance_run_id = ?", maint.ID).First(&req).Error; err != nil {
		t.Fatalf("读维护请求: %v", err)
	}
	if req.ReasoningTokens == nil || *req.ReasoningTokens != 7 {
		t.Fatalf("维护请求应落 reasoning_tokens = 7: %+v", req)
	}
	if req.ProviderCost == nil || math.Abs(*req.ProviderCost-0.125) > 1e-12 ||
		req.ProviderCostRaw == nil || !strings.Contains(*req.ProviderCostRaw, "cost_usd") {
		t.Fatalf("cost_usd 应按 USD 落库并记来源字段: %+v", req)
	}
}

// 端到端：假服务 usage 带推理 token 与供应商金额时请求行有值；不带时为
// NULL（缺失 ≠ 0）。
func TestBotRunUsageFieldsEndToEnd(t *testing.T) {
	withFields := int164Usage(map[string]interface{}{
		"completion_tokens_details": map[string]interface{}{"reasoning_tokens": 33},
		"cost_usd":                  "0.75",
	})
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "第一轮", RawUsage: withFields},
		fakeModelTurn{Text: "第二轮", Usage: &fakeModelUsage{Prompt: 10, Completion: 2, Total: 12}},
	)
	userID := createBotTestUser(t, env.db, "int164e2e")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "int164-1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// 等 run 行出现且收尾：botSettled 在新 run 落库前会误报空闲。
	int139WaitNewRunSettled(t, env, bot.ID, 1)

	reqs := runModelRequests(t, env, lastRunOf(t, env, bot.ID).ID)
	if len(reqs) != 1 {
		t.Fatalf("应只有一条请求行: %d", len(reqs))
	}
	if reqs[0].ReasoningTokens == nil || *reqs[0].ReasoningTokens != 33 {
		t.Fatalf("请求行 reasoning_tokens 应为 33: %+v", reqs[0])
	}
	if reqs[0].ProviderCost == nil || math.Abs(*reqs[0].ProviderCost-0.75) > 1e-12 ||
		reqs[0].ProviderCostRaw == nil || !strings.Contains(*reqs[0].ProviderCostRaw, "cost_usd") {
		t.Fatalf("请求行 provider_cost 应为 USD 0.75 并记来源字段: %+v", reqs[0])
	}

	// 第二轮不带这两个字段：请求行保持 NULL。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "again", ClientMessageID: "int164-2"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 2)
	reqs = runModelRequests(t, env, lastRunOf(t, env, bot.ID).ID)
	if len(reqs) != 1 {
		t.Fatalf("第二轮应只有一条请求行: %d", len(reqs))
	}
	if reqs[0].ReasoningTokens != nil || reqs[0].ProviderCost != nil || reqs[0].ProviderCostRaw != nil {
		t.Fatalf("不带字段的请求行应保持 NULL: %+v", reqs[0])
	}
}
