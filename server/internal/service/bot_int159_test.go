package service

// INT-159（机器人面）：fs_thread launch 可选执行配置（model_selection/
// runtime_mode/workspace）、composer_options 返回 providers 模型目录与
// not_settable、线程摘要回显 provider/model/branch、非 launch 动作携带
// 配置字段被拒、v12 工具集基线钉死。v1–v10 的冻结基线断言在
// bot_int103_test.go / bot_int136_test.go，v11（INT-156）基线在
// bot_int156_test.go。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// botToolsetV12FrozenHash 是 INT-159 发布的 v12 wire 字节基线：launch 执行
// 配置、composer_options/providers 描述与摘要回显只进 v12；之后任何改动都
// 会让存量会话升级后的 tools_hash 漂移，必须先递增版本再改字节。
const botToolsetV12FrozenHash = "d7a64886f0ea926574caa97a0e14e757c75d931750852e8422a41e5a77fdb1a2"

func TestBotInt159ToolsetV12(t *testing.T) {
	v11 := botToolsetVersionOf(11)
	v12 := botToolsetVersionOf(12)
	if v12.hash != botToolsetV12FrozenHash {
		t.Fatalf("v12 tools_hash 偏离基线: got %s want %s", v12.hash, botToolsetV12FrozenHash)
	}
	if v11.hash == v12.hash {
		t.Fatalf("v11/v12 相邻版本应字节不同")
	}
	if botToolsetOf().hash != v12.hash {
		t.Fatalf("botToolsetOf 应指向 v12")
	}
	thread := v12.byName["fs_thread"]
	if thread == nil || len(thread.Actions) != 11 {
		t.Fatalf("v12 fs_thread 应仍是 11 个动作: %+v", thread)
	}
	if thread.ArgsType.Name() != "botThreadArgs" {
		t.Fatalf("v12 fs_thread 应使用活跃参数结构体: %s", thread.ArgsType.Name())
	}
	// v12 的 fs_thread schema 带 launch 配置字段；v9–v11 冻结版没有。
	raw12, _ := json.Marshal(thread.Parameters)
	for _, field := range []string{`"model_selection"`, `"runtime_mode"`, `"workspace"`} {
		if !strings.Contains(string(raw12), field) {
			t.Fatalf("v12 fs_thread schema 缺 %s: %s", field, raw12)
		}
	}
	for _, ver := range []int64{9, 10, 11} {
		raw, _ := json.Marshal(botToolsetVersionOf(ver).byName["fs_thread"].Parameters)
		for _, field := range []string{"model_selection", "runtime_mode", `"workspace"`} {
			if strings.Contains(string(raw), field) {
				t.Fatalf("v%d fs_thread schema 不得带 %s（冻结字节外泄）: %s", ver, field, raw)
			}
		}
	}
	// v12 描述应点明配置入口与回显，并保留 INT-156 的 list 富化说明。
	for _, kw := range []string{"model_selection", "composer_options", "providers", "activity", "last_reply"} {
		if !strings.Contains(thread.Description, kw) {
			t.Fatalf("v12 fs_thread 描述缺 %q: %s", kw, thread.Description)
		}
	}
}

// TestBotInt159SchemaValueTyped 钉住 options[].value 的类型声明：Go any 默认
// 生成无 type 的 schema，严格校验 parameters 的 OpenAI 兼容网关会拒掉整个
// fs_thread。oneof_type 显式产 oneOf[{type:string},{type:boolean}]。
func TestBotInt159SchemaValueTyped(t *testing.T) {
	thread := botToolsetVersionOf(12).byName["fs_thread"]
	b, _ := json.Marshal(thread.Parameters)
	var p map[string]interface{}
	json.Unmarshal(b, &p)
	node := p["properties"].(map[string]interface{})["model_selection"].(map[string]interface{})["properties"].(map[string]interface{})["options"].(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})["value"].(map[string]interface{})
	if _, hasType := node["type"]; hasType {
		t.Fatalf("value 应走 oneOf 而非单 type: %v", node)
	}
	oneOf, _ := node["oneOf"].([]interface{})
	got := map[string]bool{}
	for _, o := range oneOf {
		got[o.(map[string]interface{})["type"].(string)] = true
	}
	if len(oneOf) != 2 || !got["string"] || !got["boolean"] {
		t.Fatalf("options[].value 应声明 oneOf string|boolean: %v", node)
	}
	// v11 及以下为冻结参数结构体，本断言只针对 v12 活跃 schema。
}

// 非 launch 动作携带执行配置字段必须被拒（不静默忽略）；launch 合法通过。
func TestBotInt159ActionFieldRules(t *testing.T) {
	ts := botToolsetOf()
	for _, field := range botThreadLaunchConfigFields {
		for _, action := range []string{"get", "send", "stop", "settle", "list", "composer_options"} {
			args := `{"action":"` + action + `","env_id":"e1","thread_id":"t1","text":"x","` + field + `":`
			if field == "runtime_mode" {
				args += `"auto"}`
			} else {
				args += `{}}`
			}
			if _, err := botPrepareToolCall(ts, "fs_thread", args); err == nil {
				t.Fatalf("fs_thread/%s 携带 %s 应被拒绝", action, field)
			}
		}
	}
	// launch 带完整配置通过受理校验。
	ok, err := botPrepareToolCall(ts, "fs_thread", `{"action":"launch","env_id":"e1","project_id":"p1","text":"hi",`+
		`"model_selection":{"instance_id":"prov","model":"m1","options":[{"id":"effort","value":"high"}]},`+
		`"runtime_mode":"full-access","workspace":{"type":"worktree","branch":"feat-x"}}`)
	if err != nil {
		t.Fatalf("launch 带执行配置应通过: %v", err)
	}
	if ok == nil || ok.ToolName != "fs_thread" {
		t.Fatalf("prepared call = %+v", ok)
	}
}

// botThreadLaunchConfig：省略不落字段、完整映射、缺必填字段在受理侧拒绝。
func TestBotInt159LaunchConfigMapping(t *testing.T) {
	// 全省略：不落任何字段，沿用项目默认。
	req := &T3CreateThreadRequest{}
	if err := botThreadLaunchConfig(&botThreadArgs{}, req); err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if req.ModelSelection != nil || req.RuntimeMode != nil || req.Workspace != nil {
		t.Fatalf("省略配置不应落字段: %+v", req)
	}

	startFromOrigin := true
	args := &botThreadArgs{
		ModelSelection: &botThreadModelSelection{
			InstanceID: "prov-a", Model: "swe-2",
			Options: []botThreadModelOption{{ID: "effort", Value: "high"}},
		},
		RuntimeMode: "auto",
		Workspace: &botThreadWorkspace{
			Type: "worktree", BaseRef: "origin/main", Branch: "feat-159",
			StartFromOrigin: &startFromOrigin,
		},
	}
	if err := botThreadLaunchConfig(args, req); err != nil {
		t.Fatalf("full config: %v", err)
	}
	if req.ModelSelection == nil || req.ModelSelection.InstanceId != "prov-a" ||
		req.ModelSelection.Model != "swe-2" {
		t.Fatalf("model selection = %+v", req.ModelSelection)
	}
	if len(req.ModelSelection.Options) != 1 || req.ModelSelection.Options[0].Id != "effort" ||
		req.ModelSelection.Options[0].Value != "high" {
		t.Fatalf("options = %+v", req.ModelSelection.Options)
	}
	if req.RuntimeMode == nil || string(*req.RuntimeMode) != "auto" {
		t.Fatalf("runtime mode = %+v", req.RuntimeMode)
	}
	ws := req.Workspace
	if ws == nil || string(ws.Type) != "worktree" || ws.BaseRef == nil || *ws.BaseRef != "origin/main" ||
		ws.Branch == nil || *ws.Branch != "feat-159" || ws.StartFromOrigin == nil || !*ws.StartFromOrigin {
		t.Fatalf("workspace = %+v", ws)
	}

	// model_selection 缺 instance_id/model：受理侧即拒，不透给上游。
	for _, ms := range []*botThreadModelSelection{
		{InstanceID: "", Model: "m1"},
		{InstanceID: "prov", Model: ""},
		{InstanceID: "prov", Model: "m1", Options: []botThreadModelOption{{ID: "", Value: "x"}}},
	} {
		if err := botThreadLaunchConfig(&botThreadArgs{ModelSelection: ms}, &T3CreateThreadRequest{}); err == nil {
			t.Fatalf("非法 model_selection 应被拒绝: %+v", ms)
		}
	}

	// options[].value 字符串同口径 TrimSpace：执行取值须与授权摘要
	// （统一 trim 叶子值）一致（代码审查 r22）。
	padded := &T3CreateThreadRequest{}
	paddedArgs := &botThreadArgs{ModelSelection: &botThreadModelSelection{
		InstanceID: "prov", Model: "m",
		Options: []botThreadModelOption{{ID: " effort ", Value: " high "}},
	}}
	if err := botThreadLaunchConfig(paddedArgs, padded); err != nil {
		t.Fatalf("padded options: %v", err)
	}
	opt := padded.ModelSelection.Options[0]
	if opt.Id != "effort" || opt.Value != "high" {
		t.Fatalf("options id/value 应 TrimSpace: %+v", opt)
	}

	// options[].id 重复受理即拒：摘要排序归一与执行保序透传会分叉
	// （代码审查 r24）。
	dupArgs := &botThreadArgs{ModelSelection: &botThreadModelSelection{
		InstanceID: "prov", Model: "m",
		Options: []botThreadModelOption{{ID: "effort", Value: "low"}, {ID: "effort", Value: "high"}},
	}}
	if err := botThreadLaunchConfig(dupArgs, &T3CreateThreadRequest{}); err == nil {
		t.Fatalf("options 重复 id 应被拒绝")
	}
}

// botSlimComposerProviders：归一化上游 camelCase 目录，剔除不可用项。
func TestBotInt159ComposerProviders(t *testing.T) {
	config := map[string]interface{}{
		"providers": []interface{}{
			map[string]interface{}{
				"instanceId": "codex", "displayName": "Codex",
				"models": []interface{}{
					map[string]interface{}{
						"slug": "gpt-5.3-codex", "name": "GPT 5.3 Codex", "isDefault": true,
						"capabilities": map[string]interface{}{
							"optionDescriptors": []interface{}{
								map[string]interface{}{
									"id": "effort", "label": "推理强度", "type": "select",
									"currentValue": "medium",
									"options": []interface{}{
										map[string]interface{}{"id": "low", "label": "低"},
										map[string]interface{}{"id": "high", "label": "高", "isDefault": true},
									},
								},
							},
						},
					},
					map[string]interface{}{"name": "缺 slug 的坏条目"},
				},
			},
			map[string]interface{}{"instanceId": "disabled", "enabled": false,
				"models": []interface{}{map[string]interface{}{"slug": "m"}}},
			map[string]interface{}{"instanceId": "unavail", "availability": "unavailable",
				"models": []interface{}{map[string]interface{}{"slug": "m"}}},
			map[string]interface{}{"instanceId": "nomodels", "models": []interface{}{}},
			map[string]interface{}{"displayName": "缺 instanceId"},
		},
	}
	providers, drifted := botSlimComposerProviders(config)
	if drifted {
		t.Fatalf("正常目录不得报漂移（缺 instanceId 的坏行单独跳过即可）")
	}
	if len(providers) != 1 || providers[0]["instance_id"] != "codex" || providers[0]["name"] != "Codex" {
		t.Fatalf("providers = %+v", providers)
	}
	models, _ := providers[0]["models"].([]map[string]interface{})
	if len(models) != 1 || models[0]["slug"] != "gpt-5.3-codex" || models[0]["is_default"] != true {
		t.Fatalf("models = %+v", models)
	}
	options, _ := models[0]["options"].([]map[string]interface{})
	if len(options) != 1 || options[0]["id"] != "effort" || options[0]["current_value"] != "medium" {
		t.Fatalf("options = %+v", options)
	}
	choices, _ := options[0]["choices"].([]map[string]interface{})
	if len(choices) != 2 || choices[1]["is_default"] != true {
		t.Fatalf("choices = %+v", choices)
	}
	// 空/非数组 config 安全返回空目录，不算漂移（上游合法地没有目录）。
	if got, d := botSlimComposerProviders(map[string]interface{}{}); len(got) != 0 || d {
		t.Fatalf("empty config 应返回空目录且无漂移: %+v drift=%v", got, d)
	}
	if got, d := botSlimComposerProviders(map[string]interface{}{"providers": map[string]interface{}{}}); len(got) != 0 || d {
		t.Fatalf("非数组 providers 应返回空目录且无漂移: %+v drift=%v", got, d)
	}
	// 上游改了键名：条目存在但一个 instanceId 都读不到 → 报漂移，回执
	// 里必须显式标注而不是让目录静默消失（代码审查 Note）。
	_, drifted = botSlimComposerProviders(map[string]interface{}{
		"providers": []interface{}{
			map[string]interface{}{"instance_id": "snake_case 漂移"},
			map[string]interface{}{"name": "只有名字"},
		},
	})
	if !drifted {
		t.Fatalf("providers 有条目但无 instanceId 应报字段漂移")
	}
}

// botSlimThreadSummary：provider/model/branch 回显实际生效配置，空值省略。
func TestBotInt159ThreadSummaryEcho(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		out := botSlimThreadSummary(&T3ThreadSummary{
			Id: "th-1", Title: "t", Provider: "codex", Model: "gpt-5.3-codex", Branch: "feat-x",
		})
		if out["provider"] != "codex" || out["model"] != "gpt-5.3-codex" || out["branch"] != "feat-x" {
			t.Fatalf("摘要应回显执行配置: %+v", out)
		}
	})
	t.Run("empty", func(t *testing.T) {
		out := botSlimThreadSummary(&T3ThreadSummary{Id: "th-2", Title: "t"})
		for _, k := range []string{"provider", "model", "branch"} {
			if _, ok := out[k]; ok {
				t.Fatalf("空 %s 不应出现在摘要: %+v", k, out)
			}
		}
	})
}

// composer_options 执行路径：projects/providers/not_settable 三段齐备，
// providers 来自上游 config 归一化，可回填 launch 的 model_selection。
func TestBotInt159ComposerOptionsExec(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "t3compose")
	bot := env.createBot(t, userID)
	fake, envID := pairBotT3(t, env, userID, "选项机")

	fake.mu.Lock()
	fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "P", "workspaceRoot": "/repo",
			"defaultModelSelection": map[string]any{"instanceId": "codex", "model": "gpt-5.3-codex"}},
	}})
	fake.configValue = mustJSON(map[string]any{
		"providers": []any{
			map[string]any{"instanceId": "codex", "displayName": "Codex",
				"models": []any{map[string]any{"slug": "gpt-5.3-codex", "name": "GPT 5.3 Codex", "isDefault": true}}},
		},
		"shellResumeCompletionMarker": true,
	})
	fake.mu.Unlock()

	call := &model.BotToolCall{
		BotID: bot.ID, ToolName: "fs_thread", Action: "composer_options",
		ArgumentsJSON: `{"action":"composer_options","env_id":"` + envID + `"}`,
	}
	raw, err := env.bots.execThreadRead(context.Background(), call)
	if err != nil {
		t.Fatalf("composer_options: %v", err)
	}
	var out struct {
		Projects    []map[string]any    `json:"projects"`
		Providers   []map[string]any    `json:"providers"`
		NotSettable []map[string]string `json:"not_settable"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if len(out.Projects) != 1 || out.Projects[0]["id"] != "p-1" {
		t.Fatalf("projects = %+v", out.Projects)
	}
	def, _ := out.Projects[0]["default_model_selection"].(map[string]any)
	if def["instance_id"] != "codex" || def["model"] != "gpt-5.3-codex" {
		t.Fatalf("default_model_selection = %+v", def)
	}
	if len(out.Providers) != 1 || out.Providers[0]["instance_id"] != "codex" {
		t.Fatalf("providers = %+v", out.Providers)
	}
	models, _ := out.Providers[0]["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	m0, _ := models[0].(map[string]any)
	if m0["slug"] != "gpt-5.3-codex" || m0["is_default"] != true {
		t.Fatalf("model entry = %+v", m0)
	}
	if len(out.NotSettable) == 0 {
		t.Fatalf("not_settable 应如实列出不可设置项")
	}
	for _, item := range out.NotSettable {
		if item["field"] == "" || item["reason"] == "" {
			t.Fatalf("not_settable 条目缺 field/reason: %+v", item)
		}
	}
}

// composer_options 的 not_settable 版本门按「发起调用的请求所冻结的
// 工具集版本」判定（call.RequestID → bot_model_requests），不读 bots
// 表活值——另一条对话线先激活 v12 会把行版本推进，本会话仍冻结 v11
// 时必须照常标注不可用（代码审查 r30）。
func TestBotInt159ComposerOptionsFrozenVersionGate(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "t3compgate")
	bot := env.createBot(t, userID)
	fake, envID := pairBotT3(t, env, userID, "版本门")
	fake.mu.Lock()
	fake.projects = mustJSON(map[string]any{"projects": []any{}})
	fake.configValue = mustJSON(map[string]any{"providers": []any{}})
	fake.mu.Unlock()

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "gate:" + uuid.NewString(),
		TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("run: %v", err)
	}
	var ordinal int64
	newReq := func(ver int64) *model.BotModelRequest {
		ordinal++
		req := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: ordinal,
			Status:         model.BotModelRequestRunning,
			PromptRevision: bot.PromptRevision, ToolsetVersion: ver,
			PrefixHash: "x", ToolsHash: "x", RequestHash: "x",
			TokenEstimator: "chars/4", StartedAt: now,
		}
		if err := env.bots.requests.CreateTx(env.db, req); err != nil {
			t.Fatalf("req: %v", err)
		}
		return req
	}
	hasGateNote := func(reqID string) bool {
		call := &model.BotToolCall{
			BotID: bot.ID, RequestID: reqID,
			ToolName: "fs_thread", Action: "composer_options",
			ArgumentsJSON: `{"action":"composer_options","env_id":"` + envID + `"}`,
		}
		raw, err := env.bots.execThreadRead(context.Background(), call)
		if err != nil {
			t.Fatalf("composer_options: %v", err)
		}
		var out struct {
			NotSettable []map[string]string `json:"not_settable"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, item := range out.NotSettable {
			if strings.Contains(item["reason"], "本会话工具集") {
				return true
			}
		}
		return false
	}
	if !hasGateNote(newReq(11).ID) {
		t.Fatalf("v11 冻结会话的 not_settable 应标注 launch 配置字段不可用")
	}
	if hasGateNote(newReq(botToolsetVersion).ID) {
		t.Fatalf("v%d 冻结会话不应出现版本门标注", botToolsetVersion)
	}
	// bots 表活值已被另一条线推进到 v12：本会话冻结 v11 仍应标注。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("toolset_version", botToolsetVersion).Error; err != nil {
		t.Fatalf("bump bot version: %v", err)
	}
	if !hasGateNote(newReq(11).ID) {
		t.Fatalf("活值推进后 v11 冻结会话仍应标注（按请求版本而非 bots 表活值）")
	}
}

// launch 端到端：bot 参数经服务层落进 launchThread RPC 的
// modelSelection/runtimeMode/workspaceStrategy；省略时只走项目默认。
func TestBotInt159LaunchDispatch(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "t3launch")
	bot := env.createBot(t, userID)
	fake, envID := pairBotT3(t, env, userID, "启动机")

	fake.mu.Lock()
	fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "P", "workspaceRoot": "/repo",
			"defaultModelSelection": map[string]any{"instanceId": "default-prov", "model": "default-m"}},
	}})
	fake.mu.Unlock()

	dispatch := func(args string) (interface{}, error) {
		call := &model.BotToolCall{
			BotID: bot.ID, ToolName: "fs_thread", Action: "launch",
			ArgumentsJSON:        args,
			ExecutionContextJSON: `{}`,
		}
		return env.bots.runThreadWrite(context.Background(), call)
	}
	launches := func() []map[string]any {
		var out []map[string]any
		for _, rpc := range fake.recordedRPC() {
			if rpc.method != "orchestration.launchThread" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(rpc.payload, &payload); err == nil {
				out = append(out, payload)
			}
		}
		return out
	}

	// 带完整执行配置的 launch。
	if _, err := dispatch(`{"action":"launch","env_id":"` + envID + `","project_id":"p-1","text":"诊断 INT-158",` +
		`"model_selection":{"instance_id":"acpRegistry_devin","model":"swe-2","options":[{"id":"effort","value":"high"},{"id":"fast","value":true}]},` +
		`"runtime_mode":"auto","workspace":{"type":"worktree","branch":"int159"}}`); err != nil {
		t.Fatalf("launch with config: %v", err)
	}
	got := launches()
	if len(got) != 1 {
		t.Fatalf("应有 1 个 launchThread: %d", len(got))
	}
	ms, _ := got[0]["modelSelection"].(map[string]any)
	if ms["instanceId"] != "acpRegistry_devin" || ms["model"] != "swe-2" {
		t.Fatalf("modelSelection = %+v", ms)
	}
	options, _ := ms["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("options = %+v", ms["options"])
	}
	opt0, _ := options[0].(map[string]any)
	if opt0["id"] != "effort" || opt0["value"] != "high" {
		t.Fatalf("option = %+v", opt0)
	}
	// boolean 选项必须以 JSON boolean 落到 wire（T3 ProviderOptionSelectionValue），
	// 不被收窄成字符串。
	opt1, _ := options[1].(map[string]any)
	if opt1["id"] != "fast" || opt1["value"] != true {
		t.Fatalf("boolean option 应原样穿透: %+v", opt1)
	}
	if got[0]["runtimeMode"] != "auto" {
		t.Fatalf("runtimeMode = %+v", got[0]["runtimeMode"])
	}
	ws, _ := got[0]["workspaceStrategy"].(map[string]any)
	if ws["type"] != "worktree" || ws["branch"] != "int159" {
		t.Fatalf("workspaceStrategy = %+v", ws)
	}

	// 省略配置的 launch：模型回落项目默认，runtimeMode/workspace 走服务端缺省。
	if _, err := dispatch(`{"action":"launch","env_id":"` + envID + `","project_id":"p-1","text":"默认配置"}`); err != nil {
		t.Fatalf("launch default: %v", err)
	}
	got = launches()
	if len(got) != 2 {
		t.Fatalf("应有 2 个 launchThread: %d", len(got))
	}
	ms, _ = got[1]["modelSelection"].(map[string]any)
	if ms["instanceId"] != "default-prov" || ms["model"] != "default-m" {
		t.Fatalf("省略时应回落项目默认: %+v", ms)
	}
	if got[1]["runtimeMode"] != "full-access" {
		t.Fatalf("默认 runtimeMode = %+v", got[1]["runtimeMode"])
	}
	ws, _ = got[1]["workspaceStrategy"].(map[string]any)
	if ws["type"] != "root" {
		t.Fatalf("默认 workspace = %+v", ws)
	}

	// model_selection 缺必填字段在受理侧拒绝，不发 RPC。
	_, err := dispatch(`{"action":"launch","env_id":"` + envID + `","project_id":"p-1","text":"x","model_selection":{"instance_id":"p"}}`)
	if err == nil {
		t.Fatalf("缺 model 的 model_selection 应被拒绝")
	}
	if len(launches()) != 2 {
		t.Fatalf("非法配置不应发出 launchThread")
	}
}

// INT-159 受理侧校验：形状错误的执行配置在 botPrepareToolCall 就拒掉，
// 不产生待确认卡片（审查发现：原来只在 worker 执行时失败）。
func TestBotInt159LaunchConfigAccepted(t *testing.T) {
	v12 := botToolsetVersionOf(12)
	base := `"action":"launch","env_id":"e","project_id":"p","text":"x"`

	reject := []struct {
		name string
		args string
	}{
		{"model_selection 缺 model", `"model_selection":{"instance_id":"i1"}`},
		{"model_selection 缺 instance_id", `"model_selection":{"model":"m"}`},
		{"model_selection option 缺 id", `"model_selection":{"instance_id":"i","model":"m","options":[{"value":"high"}]}`},
		{"option 缺 value", `"model_selection":{"instance_id":"i","model":"m","options":[{"id":"effort"}]}`},
		{"option value=null", `"model_selection":{"instance_id":"i","model":"m","options":[{"id":"effort","value":null}]}`},
		{"option value=数字", `"model_selection":{"instance_id":"i","model":"m","options":[{"id":"effort","value":1}]}`},
		{"option value=对象", `"model_selection":{"instance_id":"i","model":"m","options":[{"id":"effort","value":{"x":1}}]}`},
		{"runtime_mode 非枚举", `"runtime_mode":"turbo"`},
		{"workspace 缺 type", `"workspace":{"branch":"feat"}`},
		{"workspace.type 非枚举", `"workspace":{"type":"new_worktree"}`},
		{"existing_worktree 缺路径", `"workspace":{"type":"existing_worktree","branch":"b"}`},
		{"root 携带 branch", `"workspace":{"type":"root","branch":"feat"}`},
		{"root 携带 start_from_origin", `"workspace":{"type":"root","start_from_origin":true}`},
		{"worktree 携带 worktree_path", `"workspace":{"type":"worktree","worktree_path":"/tmp/w"}`},
		{"existing_worktree 携带 base_ref", `"workspace":{"type":"existing_worktree","worktree_path":"/tmp/w","base_ref":"main"}`},
		{"model_selection 未知嵌套键", `"model_selection":{"instance_id":"i","model":"m","temperature":0.5}`},
		{"workspace 未知嵌套键", `"workspace":{"type":"root","path":"/tmp/w"}`},
		{"option 未知嵌套键", `"model_selection":{"instance_id":"i","model":"m","options":[{"id":"e","value":"h","label":"x"}]}`},
	}
	for _, c := range reject {
		if _, err := botPrepareToolCall(v12, "fs_thread", "{"+base+","+c.args+"}"); err == nil {
			t.Fatalf("%s 应在受理侧被拒绝", c.name)
		}
	}

	// 合法配置照常受理（进入待确认）。
	prepared, err := botPrepareToolCall(v12, "fs_thread", "{"+base+
		`,"model_selection":{"instance_id":"i","model":"m","options":[{"id":"effort","value":"high"}]}`+
		`,"runtime_mode":"full-access","workspace":{"type":"worktree","branch":"feat"}}`)
	if err != nil {
		t.Fatalf("合法配置应受理: %v", err)
	}
	if !prepared.RequiresConfirmation {
		t.Fatalf("launch 应需确认")
	}

	// 冻结的 v10 会话没有这些字段：传了被未知字段规则拒掉，不传正常受理。
	v10 := botToolsetVersionOf(10)
	if _, err := botPrepareToolCall(v10, "fs_thread", "{"+base+`,"runtime_mode":"auto"}`); err == nil {
		t.Fatalf("v10 应拒绝 runtime_mode 字段")
	}
	if _, err := botPrepareToolCall(v10, "fs_thread", "{"+base+"}"); err != nil {
		t.Fatalf("v10 launch 应受理: %v", err)
	}
}

// INT-159 授权绑定：launch 的「始终允许」按项目 + 执行配置取值精确授权，
// 换配置组合要重新确认，同配置不同正文复用授权（代码审查 P2）。
func TestBotInt159LaunchAuthzBindsExecConfig(t *testing.T) {
	svc := &BotService{}
	hashOf := func(argsJSON string) string {
		raw := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
			t.Fatalf("bad args: %v", err)
		}
		p := &botPreparedCall{ToolName: "fs_thread", Action: "launch"}
		svc.resolveThreadScope(p, raw)
		return botRuleHash(botRuleOf(p))
	}
	bare := `{"env_id":"e","project_id":"p","text":"任务一"}`
	fullAccess := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access"}`
	fullAccessOtherText := `{"env_id":"e","project_id":"p","text":"任务二","runtime_mode":"full-access"}`
	approval := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"approval-required"}`
	otherModel := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access","model_selection":{"instance_id":"i","model":"m2"}}`
	if hashOf(bare) == hashOf(fullAccess) {
		t.Fatalf("带与不带执行配置的 launch 不得共用授权")
	}
	if hashOf(fullAccess) != hashOf(fullAccessOtherText) {
		t.Fatalf("同配置不同正文的 launch 应复用同一授权（正文不进约束）")
	}
	if hashOf(fullAccess) == hashOf(approval) {
		t.Fatalf("同字段不同 runtime_mode 取值不得共用授权（防提权放行）")
	}
	if hashOf(fullAccess) == hashOf(otherModel) {
		t.Fatalf("换模型不得共用授权")
	}
	// 序列化差异（键序/空白/options 顺序）不得产生新授权规则（代码审查）。
	reordered := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access","model_selection":{ "model":"m2", "options":[{"value":"high","id":"effort"},{"id":"x","value":true}], "instance_id":"i" }}`
	canonical := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access","model_selection":{"instance_id":"i","model":"m2","options":[{"id":"x","value":true},{"id":"effort","value":"high"}]}}`
	if hashOf(reordered) != hashOf(canonical) {
		t.Fatalf("语义相同的执行配置（键序/空白/options 顺序不同）应共用授权")
	}
	// 执行侧 TrimSpace 的同语义取值不得产生新授权规则（代码审查 r19）。
	padded := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":" full-access "}`
	if hashOf(padded) != hashOf(fullAccess) {
		t.Fatalf("仅首尾空白不同的同语义取值应共用授权")
	}
	// null 叶子与缺省同口径：执行侧 null 按空值处理，摘要不得拆规则
	// （代码审查 r25）。
	withNull := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access","model_selection":{"instance_id":"i","model":"m2","options":null}}`
	withoutNull := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access","model_selection":{"instance_id":"i","model":"m2"}}`
	if hashOf(withNull) != hashOf(withoutNull) {
		t.Fatalf("null 占位字段与缺省应共用授权")
	}
	// 顶层配置键值为 null 与省略同口径（presentFields 排除 null 值，
	// fields 为空即不写摘要），与裸 launch 共用裸规则（代码审查 r26）。
	if hashOf(`{"env_id":"e","project_id":"p","text":"t","runtime_mode":null}`) != hashOf(bare) {
		t.Fatalf("顶层 null 应与省略同规则")
	}
	if hashOf(`{"env_id":"e","project_id":"p","text":"t","model_selection":null,"workspace":null}`) != hashOf(bare) {
		t.Fatalf("顶层 null 应与省略同规则")
	}
	// 空容器/空串与省略同口径（执行侧 options:[] 不进 wire、NonEmptyPtr
	// 丢空串），不得拆规则（代码审查 r27）。
	emptyOpts := `{"env_id":"e","project_id":"p","text":"任务一","runtime_mode":"full-access","model_selection":{"instance_id":"i","model":"m2","options":[]}}`
	if hashOf(emptyOpts) != hashOf(withoutNull) {
		t.Fatalf("options:[] 应与省略同规则")
	}
	emptyBranch := `{"env_id":"e","project_id":"p","text":"t","workspace":{"type":"worktree","branch":""}}`
	plainWorktree := `{"env_id":"e","project_id":"p","text":"t","workspace":{"type":"worktree"}}`
	if hashOf(emptyBranch) != hashOf(plainWorktree) {
		t.Fatalf("branch:\"\" 应与省略同规则")
	}
	// 纯空白串同口径：执行侧 TrimSpace+NonEmptyPtr 丢弃，与省略等价
	// （代码审查 r28）。
	blankBranch := `{"env_id":"e","project_id":"p","text":"t","workspace":{"type":"worktree","branch":" "}}`
	if hashOf(blankBranch) != hashOf(plainWorktree) {
		t.Fatalf("branch:\" \" 应与省略同规则")
	}
	// 例外：options[].value:"" 执行侧原样透传，与省略 value 语义不同，
	// 必须拆规则（豁免不能过头）。
	valEmpty := `{"env_id":"e","project_id":"p","text":"t","model_selection":{"instance_id":"i","model":"m2","options":[{"id":"x","value":""}]}}`
	valAbsent := `{"env_id":"e","project_id":"p","text":"t","model_selection":{"instance_id":"i","model":"m2","options":[{"id":"x"}]}}`
	if hashOf(valEmpty) == hashOf(valAbsent) {
		t.Fatalf("value:\"\" 与省略 value 执行不同，不得共用授权")
	}
}

// INT-159 always-allow 落库：constraints_json 必须带上规范化执行配置
// （exec_config）与参数值约束（param），授权管理面才能按值区分同
// action 的不同配置规则（代码审查 r25 P2）。
func TestBotInt159AlwaysAllowPersistsExecConfig(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botalw159")
	bot := env.createBot(t, userID)
	botRow := &model.Bot{ID: bot.ID, UserID: userID}
	args := `{"action":"launch","env_id":"e","project_id":"p","text":"t","runtime_mode":"auto","model_selection":{"instance_id":"i","model":"m2"}}`
	prep, err := botPrepareToolCall(botToolsetOf(), "fs_thread", args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var execCtx botCallExecContext
	env.bots.resolveCallScope(botRow, prep, args, &execCtx)
	call := &model.BotToolCall{
		ID: "c1", BotID: bot.ID, ToolName: "fs_thread", Action: "launch",
		ExecutionContextJSON: marshalExecContext(execCtx),
	}
	authz := env.bots.buildAlwaysAllowValue(bot.ID, call)
	var constraints map[string]interface{}
	if err := json.Unmarshal([]byte(authz.ConstraintsJSON), &constraints); err != nil {
		t.Fatalf("constraints: %v", err)
	}
	execCfg, ok := constraints["exec_config"].(map[string]interface{})
	if !ok || execCfg["runtime_mode"] != "auto" {
		t.Fatalf("exec_config 应落库规范化配置: %s", authz.ConstraintsJSON)
	}
	if ms, ok := execCfg["model_selection"].(map[string]interface{}); !ok || ms["model"] != "m2" {
		t.Fatalf("exec_config 应含规范化 model_selection: %s", authz.ConstraintsJSON)
	}
	if p, ok := constraints["param"].(string); !ok || !strings.HasPrefix(p, "exec=") {
		t.Fatalf("param 应落 exec 摘要: %s", authz.ConstraintsJSON)
	}
	f, _ := constraints["fields"].([]interface{})
	if len(f) != 2 {
		t.Fatalf("fields 应含两个配置字段: %s", authz.ConstraintsJSON)
	}
}

// INT-159 受理侧：runtime_mode 显式空串与非枚举同口径拒掉（缺失与为空
// 不等价——空串会静默落 full-access 缺省）。
func TestBotInt159RuntimeModeEmptyRejected(t *testing.T) {
	v12 := botToolsetVersionOf(12)
	base := `"action":"launch","env_id":"e","project_id":"p","text":"x"`
	if _, err := botPrepareToolCall(v12, "fs_thread", "{"+base+`,"runtime_mode":""}`); err == nil {
		t.Fatalf("runtime_mode 空串应受理即拒")
	}
	if _, err := botPrepareToolCall(v12, "fs_thread", "{"+base+`,"runtime_mode":"  "}`); err == nil {
		t.Fatalf("runtime_mode 空白串应受理即拒")
	}
}

// 嵌套执行配置对象内的重复键会让授权摘要（map 末次值）与执行取值（结构体
// 顺序赋值、null no-op）解释不一致，受理端必须逐层拒绝，不只查顶层。
func TestBotInt159NestedDuplicateKeysRejected(t *testing.T) {
	v12 := botToolsetVersionOf(12)
	base := `"action":"launch","env_id":"e","project_id":"p","text":"x"`
	for _, args := range []string{
		`{"instance_id":"a","instance_id":null,"model":"m"}`,                           // model_selection 内重复
		`{"instance_id":"a","model":"m","options":[{"id":"o","id":null,"value":"v"}]}`, // options 数组元素内重复
	} {
		if _, err := botPrepareToolCall(v12, "fs_thread",
			"{"+base+`,"model_selection":`+args+`}`); err == nil {
			t.Fatalf("嵌套重复键应受理即拒: %s", args)
		}
	}
	if _, err := botPrepareToolCall(v12, "fs_thread",
		"{"+base+`,"workspace":{"type":"root","type":"worktree"}}`); err == nil {
		t.Fatalf("workspace 内重复键应受理即拒")
	}
	// 顶层重复键同样保持拒绝（回归）。
	if _, err := botPrepareToolCall(v12, "fs_thread",
		"{"+base+`,"runtime_mode":"auto","runtime_mode":null}`); err == nil {
		t.Fatalf("顶层重复键应受理即拒")
	}
}
