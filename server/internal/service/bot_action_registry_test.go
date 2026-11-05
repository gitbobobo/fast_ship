package service

// INT-195 动作注册表测试：四道护栏——
//  1. 不变式：v1–v14 每个工具集动作都能在注册表查到，Mode 一致，静态确认
//     标记与该版本字面量一致（confirmDynamic 的动作按参数动态判定，不在此
//     约束内，但字面量必须保持免确认静态值）；
//  2. 构建校验：写动作必须显式声明确认分类（旧 botScheduleConfirmation 的
//     fail-closed 护栏的结构化等价），dynamic 与 Confirm 闭包一一对应；
//  3. 派生集合金色断言：由注册表推导的「只读动作集合」「围栏豁免集合」与
//     手写期望清单逐项相等，防转录笔误；
//  4. 验收：临时动作同时注入声明组与运行时表，走真实生成路径
//     （botToolRegistryV14FromGroups → botBuildToolset）与真实受理路径
//     （botPrepareToolCall）再到执行分派——新增一个动作只需注册表一条，
//     校验、执行、确认、策略、围栏、会话全部正确识别。

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// botWithTestSpecs 把临时 spec 写进运行时注册表，测试结束恢复原值（原来是
// nil 的键删除）。注册表是进程级单例：覆盖已有键必须可回滚，否则后续测试
// 与同进程请求会拿到被污染的分派（面板审查 R1：注销不能是裸 delete）。
func botWithTestSpecs(t *testing.T, specs ...*botActionSpec) {
	t.Helper()
	m := botActionSpecsAll()
	saved := make(map[string]*botActionSpec, len(specs))
	for _, spec := range specs {
		key := spec.Tool + "/" + spec.Action
		saved[key] = m[key]
		m[key] = spec
	}
	t.Cleanup(func() {
		for key, old := range saved {
			if old == nil {
				delete(m, key)
			} else {
				m[key] = old
			}
		}
	})
}

// TestBotActionRegistryCoversEveryToolsetVersion 是注册表的完备性不变式：
// 各版本（含冻结副本）byAction 里的每个键都必须在注册表查到 spec，否则该
// 版本的动作在执行/确认/围栏/会话等分派点会落到 fail-closed 默认值——行为
// 与字面量分家。Mode 全量比对；静态确认标记按 botRequiresConfirmationOf
// 推导比对，confirmDynamic 的动作字面量必须是免确认静态值（运行时由
// spec.Confirm 在受理端动态改判）。
func TestBotActionRegistryCoversEveryToolsetVersion(t *testing.T) {
	for version := int64(1); version <= botToolsetVersion; version++ {
		ts := botToolsetVersionOf(version)
		for _, tool := range ts.tools {
			for _, action := range tool.Actions {
				key := tool.Name + "/" + action.Name
				spec := botActionSpecOf(tool.Name, action.Name)
				if spec == nil {
					t.Fatalf("v%d 动作 %s 在注册表中查不到", version, key)
				}
				if spec.Mode != action.Mode {
					t.Fatalf("v%d 动作 %s Mode 不一致: 注册表 %s vs 字面量 %s", version, key, spec.Mode, action.Mode)
				}
				if spec.Confirmation == confirmDynamic {
					if spec.Confirm == nil {
						t.Fatalf("v%d 动作 %s confirmDynamic 缺 Confirm 闭包", version, key)
					}
					if action.RequiresConfirmation {
						t.Fatalf("v%d 动作 %s 字面量应为免确认静态值（运行时由 Confirm 动态改判）", version, key)
					}
					continue
				}
				if botRequiresConfirmationOf(spec) != action.RequiresConfirmation {
					t.Fatalf("v%d 动作 %s RequiresConfirmation 不一致: 注册表推导 %v vs 字面量 %v",
						version, key, botRequiresConfirmationOf(spec), action.RequiresConfirmation)
				}
			}
		}
	}
}

// TestBotActionRegistryValidatesSpecs 构建期校验：写动作零值确认分类不再
// 等价于免确认（旧 botScheduleConfirmation 的 fail-closed 护栏收进构建），
// dynamic 与 Confirm 闭包一一对应，只读动作不得恒需确认。
func TestBotActionRegistryValidatesSpecs(t *testing.T) {
	base := func(mutate func(*botActionSpec)) *botActionSpec {
		spec := &botActionSpec{
			Tool: "fs_issue", Action: "boom_test",
			Mode: model.BotExecutionSync, Confirmation: confirmNever,
		}
		mutate(spec)
		return spec
	}
	mustPanic := func(name string, spec *botActionSpec) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: 应当 panic 拒绝", name)
			}
		}()
		botValidateActionSpec(spec)
	}
	mustOK := func(name string, spec *botActionSpec) {
		t.Helper()
		botValidateActionSpec(spec)
	}
	// 写动作未声明确认分类：拒绝（零值不再静默免确认）。
	mustPanic("写动作缺确认分类", base(func(s *botActionSpec) { s.Confirmation = "" }))
	// 只读动作零值分类：合法（读不需要确认属性）。
	mustOK("只读动作缺确认分类", base(func(s *botActionSpec) { s.Confirmation = ""; s.ReadOnly = true }))
	// dynamic 必须带 Confirm 闭包。
	mustPanic("dynamic 缺 Confirm", base(func(s *botActionSpec) { s.Confirmation = confirmDynamic }))
	// 静态分类不得携带 Confirm 闭包。
	mustPanic("never 带 Confirm", base(func(s *botActionSpec) {
		s.Confirm = func(map[string]json.RawMessage) (bool, error) { return true, nil }
	}))
	// 只读动作不得恒需确认（矛盾语义）。
	mustPanic("只读恒需确认", base(func(s *botActionSpec) { s.Confirmation = confirmAlways; s.ReadOnly = true }))
	// 恒需确认的动作不得带同步执行入口（绕过确认的第二把钥匙，面板 R2）。
	mustPanic("恒需确认带同步入口", base(func(s *botActionSpec) {
		s.Confirmation = confirmAlways
		s.SyncExec = func(*BotService, *model.Bot, context.Context, *model.BotToolCall) (string, error) {
			return "{}", nil
		}
	}))
	// SyncExecForbidden 显式声明同步入口非法：带 SyncExec 即 panic（覆盖
	// confirmDynamic+恒真闭包的 create/enable/delete），不带则合法。
	mustPanic("禁同步入口却带 SyncExec", base(func(s *botActionSpec) {
		s.SyncExecForbidden = true
		s.SyncExec = func(*BotService, *model.Bot, context.Context, *model.BotToolCall) (string, error) {
			return "{}", nil
		}
	}))
	mustOK("禁同步入口且无 SyncExec", base(func(s *botActionSpec) { s.SyncExecForbidden = true }))
	// Mode 与执行入口/确认分类交叉校验（快船 bot R5）：async 无 AsyncExec、
	// confirmAlways 配 sync，都在构建期拒绝而不是等到批准后的 worker 才炸。
	mustPanic("async 缺 AsyncExec", base(func(s *botActionSpec) {
		s.Confirmation = confirmNever
		s.Mode = model.BotExecutionAsync
	}))
	mustPanic("恒确认配 sync 形态", base(func(s *botActionSpec) {
		s.Confirmation = confirmAlways
		s.Mode = model.BotExecutionSync
	}))
	// 未知分类拒绝。
	mustPanic("未知分类", base(func(s *botActionSpec) { s.Confirmation = botConfirmKind("sometimes") }))
	// 合法形态：never / always / dynamic。
	mustOK("never", base(func(*botActionSpec) {}))
	mustOK("always", base(func(s *botActionSpec) {
		s.Confirmation = confirmAlways
		s.Mode = model.BotExecutionAsync
		s.PolicyTier = "tier2"
		s.AsyncExec = func(*BotService, context.Context, *model.BotToolCall) (interface{}, error) {
			return nil, nil
		}
	}))
	mustOK("dynamic", base(func(s *botActionSpec) {
		s.Confirmation = confirmDynamic
		s.Confirm = func(map[string]json.RawMessage) (bool, error) { return true, nil }
	}))
}

// TestBotActionRegistryDerivedSets 由注册表推导「只读动作集合」与「围栏豁免
// 集合」，与手写期望清单逐项相等：既证明注册表转录无误，也证明没有旧平行表
// 残留。期望清单按今日 botReadOnlyActions / botResultFenceExemptTools 的原值
// 手写；新增动作在这两个集合上的归属要随 spec 一并更新并补进清单。
func TestBotActionRegistryDerivedSets(t *testing.T) {
	wantReadOnly := []string{
		"fs_environment/list", "fs_environment/projects",
		"fs_help/read",
		"fs_issue/comments", "fs_issue/get", "fs_issue/list", "fs_issue/timeline",
		"fs_memory/history", "fs_memory/list", "fs_memory/read", "fs_memory/status",
		"fs_memory_search/search",
		"fs_project/get", "fs_project/list",
		"fs_result/read",
		"fs_schedule/list", "fs_schedule/runs", "fs_schedule/state_get", "fs_schedule/status",
		"fs_thread/composer_options", "fs_thread/get", "fs_thread/item",
		"fs_thread/list", "fs_thread/workspace_options",
	}
	wantFenceExempt := []string{
		"fs_cancel/cancel",
		"fs_help/read",
		"fs_memory/append", "fs_memory/create", "fs_memory/delete", "fs_memory/history",
		"fs_memory/list", "fs_memory/replace", "fs_memory/restore", "fs_memory/status",
		"fs_schedule/state_set",
		"fs_stop/stop",
	}
	gotReadOnly := make([]string, 0, len(wantReadOnly))
	gotFenceExempt := make([]string, 0, len(wantFenceExempt))
	for key, spec := range botActionSpecsAll() {
		if spec.ReadOnly {
			gotReadOnly = append(gotReadOnly, key)
		}
		if spec.FenceExempt {
			gotFenceExempt = append(gotFenceExempt, key)
		}
	}
	sort.Strings(gotReadOnly)
	sort.Strings(gotFenceExempt)
	if strings.Join(gotReadOnly, ",") != strings.Join(wantReadOnly, ",") {
		t.Fatalf("只读动作集合偏离期望:\n got %v\nwant %v", gotReadOnly, wantReadOnly)
	}
	if strings.Join(gotFenceExempt, ",") != strings.Join(wantFenceExempt, ",") {
		t.Fatalf("围栏豁免集合偏离期望:\n got %v\nwant %v", gotFenceExempt, wantFenceExempt)
	}
}

// TestBotSyncDispatchKeepsLegacyRejections 是同步分派的白名单语义回归：
// 恒需确认的 fs_schedule 写动作（create/enable/delete）不声明 SyncExec，
// 同步分派保持拆分前的拒绝（「同步工具不执行该动作」），不给绕过受理直接
// 写库留第二把钥匙（面板审查 R1）。update/run 保留同步入口（免确认参数的
// 真实形态），这里不执行它们——零值 BotService 没有 DB，走到执行体只会
// 混入无关失败。
func TestBotSyncDispatchKeepsLegacyRejections(t *testing.T) {
	for _, action := range []string{"create", "enable", "delete"} {
		call := &model.BotToolCall{
			ToolName: "fs_schedule", Action: action,
			ArgumentsJSON: `{"action":"` + action + `","task_id":"t1"}`,
		}
		_, err := (&BotService{}).botExecuteSyncTool(context.Background(), nil, call)
		if err == nil || !strings.Contains(err.Error(), "同步工具不执行该动作") {
			t.Fatalf("fs_schedule/%s 的同步分派应保持旧拒绝语义: %v", action, err)
		}
	}
	// run 的同步入口保留 dry_run 门禁：非 dry_run 到达同步路径即拒（与拆分前
	// botExecuteSyncTool 的专用拒绝一致），零值 BotService 不触库。
	runCall := &model.BotToolCall{ToolName: "fs_schedule", Action: "run", ArgumentsJSON: `{"action":"run","task_id":"t1"}`}
	if _, err := (&BotService{}).execScheduleRunAction(nil, context.Background(), runCall); err == nil || !strings.Contains(err.Error(), "非 dry_run 的 run 需要用户确认") {
		t.Fatalf("非 dry_run 的 run 同步直达应被门禁拒绝: %v", err)
	}
}

// TestBotCapabilityStatusNilBotRejected 防御回归：能力查询的 SyncExec 消费
// bot.ToolsetVersion，bot 为 nil 是编程错误——必须拒绝而不是解引用 panic
// （面板审查 R1：旧聚合兼容入口曾把 nil bot 送进来）。
func TestBotCapabilityStatusNilBotRejected(t *testing.T) {
	call := &model.BotToolCall{ToolName: "fs_memory", Action: "status", ArgumentsJSON: `{"action":"status"}`}
	_, err := (&BotService{}).botExecuteSyncTool(context.Background(), nil, call)
	if err == nil {
		t.Fatal("nil bot 的能力查询应被拒绝")
	}
}

// TestBotActionRegistryDrivesEverySystem 是验收测试：临时动作注入声明组与
// 运行时表（用完恢复），走真实生成路径与真实受理路径，证明新增一个动作
// 只需注册表一条，任何分派点都不用改。
func TestBotActionRegistryDrivesEverySystem(t *testing.T) {
	fixedResult := `{"ok":true,"from":"registry-spec"}`
	// boom_test：同步免确认动作，用 fs_issue 真实存在的参数字段（query/
	// state），保证受理端的键白名单放行——字段约束本身用自定义字段名在
	// botValidateActionFields 层另行证明（见下）。
	boomSync := &botActionSpec{
		Tool: "fs_issue", Action: "boom_test",
		Description: "测试动作（验收后恢复）",
		Mode:        model.BotExecutionSync, Confirmation: confirmNever,
		PolicyTier: "tier3", PolicyRule: "boom_test_rule",
		FenceExempt: true,
		Required:    req("query"), Forbidden: []string{"state"},
		SyncExec: func(_ *BotService, _ *model.Bot, _ context.Context, _ *model.BotToolCall) (string, error) {
			// 假执行器忽略参数：只证明分派到达了 spec.SyncExec。
			return fixedResult, nil
		},
	}
	// boom_confirm_test：动态确认动作（Confirm 命中 state 字段即需确认），
	// 证明受理端经真实 botPrepareToolCall 把同步调用改判异步待确认。
	boomConfirm := &botActionSpec{
		Tool: "fs_issue", Action: "boom_confirm_test",
		Description: "测试动作（验收后恢复）",
		Mode:        model.BotExecutionSync, Confirmation: confirmDynamic,
		Required: req("query"),
		SyncExec: func(*BotService, *model.Bot, context.Context, *model.BotToolCall) (string, error) {
			return "{}", nil
		},
		Confirm: func(raw map[string]json.RawMessage) (bool, error) {
			_, sensitive := raw["state"]
			return sensitive, nil
		},
	}
	// 1) 运行时表：字段校验、执行分派、策略、围栏、会话拦截逐项按 spec 生效。
	botWithTestSpecs(t, boomSync, boomConfirm)
	t.Run("字段校验", func(t *testing.T) {
		if err := botValidateActionFields("fs_issue", "boom_test", map[string]json.RawMessage{
			"action": []byte(`"boom_test"`), "query": []byte(`"q"`),
		}); err != nil {
			t.Fatalf("新动作无需预注册规则表即可校验: %v", err)
		}
		if err := botValidateActionFields("fs_issue", "boom_test", map[string]json.RawMessage{
			"action": []byte(`"boom_test"`),
		}); err == nil || !strings.Contains(err.Error(), "需要 query") {
			t.Fatalf("缺必填字段应拒绝: %v", err)
		}
		if err := botValidateActionFields("fs_issue", "boom_test", map[string]json.RawMessage{
			"action": []byte(`"boom_test"`), "query": []byte(`"q"`), "state": []byte(`"open"`),
		}); err == nil || !strings.Contains(err.Error(), "不接受 state") {
			t.Fatalf("携带禁止字段应拒绝: %v", err)
		}
	})
	t.Run("执行分派", func(t *testing.T) {
		call := &model.BotToolCall{ToolName: "fs_issue", Action: "boom_test", ArgumentsJSON: `{"action":"boom_test","query":"q"}`}
		out, err := (&BotService{}).botExecuteSyncTool(context.Background(), nil, call)
		if err != nil || out != fixedResult {
			t.Fatalf("botExecuteSyncTool 应派发到 spec.SyncExec: out=%q err=%v", out, err)
		}
	})
	t.Run("策略档位", func(t *testing.T) {
		prep := &botPreparedCall{ToolName: "fs_issue", Action: "boom_test"}
		if got := botPolicyBaseTier(prep); got != "tier3" {
			t.Fatalf("botPolicyBaseTier 应读 spec.PolicyTier: %s", got)
		}
		if got := botPolicyRuleForAction("fs_issue", "boom_test"); got != "boom_test_rule" {
			t.Fatalf("botPolicyRuleForAction 应读 spec.PolicyRule: %q", got)
		}
		if got := botPolicyCandidateRule(prep); got != "boom_test_rule" {
			t.Fatalf("botPolicyCandidateRule 无动态候选时应回 PolicyRule: %q", got)
		}
	})
	t.Run("围栏", func(t *testing.T) {
		payload := `{"name":"x"}`
		if got := botFenceToolResult("nonce123", "fs_issue", "boom_test", payload); got != payload {
			t.Fatalf("FenceExempt=true 应豁免围栏: %s", got)
		}
		boomSync.FenceExempt = false
		if got := botFenceToolResult("nonce123", "fs_issue", "boom_test", payload); !strings.HasPrefix(got, `<untrusted_data nonce="nonce123"`) {
			t.Fatalf("FenceExempt=false 应进围栏: %s", got)
		}
		boomSync.FenceExempt = true
	})
	t.Run("定时任务会话拦截", func(t *testing.T) {
		schedOK := &botActionSpec{
			Tool: "fs_schedule", Action: "boom_test",
			Description: "测试动作（验收后恢复）",
			Mode:        model.BotExecutionSync, Confirmation: confirmNever, ScheduleOK: true,
			Forbidden: []string{"dry_run"},
			SyncExec:  (*BotService).execCapabilityStatusAction,
		}
		botWithTestSpecs(t, schedOK)
		if err := botScheduleManageDenied(&botPreparedCall{ToolName: "fs_schedule", Action: "boom_test"}); err != nil {
			t.Fatalf("ScheduleOK=true 应在定时任务会话放行: %v", err)
		}
		schedOK.ScheduleOK = false
		if err := botScheduleManageDenied(&botPreparedCall{ToolName: "fs_schedule", Action: "boom_test"}); err == nil {
			t.Fatal("ScheduleOK=false 应在定时任务会话被拒绝")
		}
		// 未注册动作保持拒绝（fail closed）。
		if err := botScheduleManageDenied(&botPreparedCall{ToolName: "fs_schedule", Action: "gone_test"}); err == nil {
			t.Fatal("未注册动作应在定时任务会话被拒绝")
		}
	})

	// 2) 真实生成路径：临时动作进声明组副本 → botToolRegistryV14FromGroups
	//    → botBuildToolset，动作出现在工具面且 Mode/静态确认随 spec。
	groups := botActionSpecGroupsAll()
	groups["fs_issue"] = append(append([]*botActionSpec{}, groups["fs_issue"]...), boomSync, boomConfirm)
	holder := botBuildToolset(botToolsetVersion, botToolRegistryV14FromGroups(groups))
	t.Run("工具面生成", func(t *testing.T) {
		action, ok := holder.byAction["fs_issue/boom_test"]
		if !ok {
			t.Fatal("临时动作应随声明组进入 v14 工具面")
		}
		if action.Mode != boomSync.Mode || action.RequiresConfirmation != botRequiresConfirmationOf(boomSync) {
			t.Fatalf("工具面动作元数据应来自 spec: %+v", action)
		}
		if _, ok := holder.byAction["fs_issue/boom_confirm_test"]; !ok {
			t.Fatal("动态确认测试动作应随声明组进入 v14 工具面")
		}
	})

	// 3) 真实受理路径：botPrepareToolCall 全链路（键白名单 → 字段校验 →
	//    确认判定）。动态确认命中时同步调用改判异步待确认。
	t.Run("正式受理", func(t *testing.T) {
		if _, err := botPrepareToolCall(holder, "fs_issue", `{"action":"boom_test"}`); err == nil || !strings.Contains(err.Error(), "需要 query") {
			t.Fatalf("受理端应按 spec 校验必填字段: %v", err)
		}
		if _, err := botPrepareToolCall(holder, "fs_issue", `{"action":"boom_test","query":"q","state":"open"}`); err == nil || !strings.Contains(err.Error(), "不接受 state") {
			t.Fatalf("受理端应按 spec 拒绝禁止字段: %v", err)
		}
		prepared, err := botPrepareToolCall(holder, "fs_issue", `{"action":"boom_test","query":"q"}`)
		if err != nil {
			t.Fatalf("合法调用应受理成功: %v", err)
		}
		if prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionSync {
			t.Fatalf("confirmNever 动作应同步免确认: %+v", prepared)
		}
		confirmed, err := botPrepareToolCall(holder, "fs_issue", `{"action":"boom_confirm_test","query":"q","state":"open"}`)
		if err != nil {
			t.Fatalf("动态确认动作受理应成功: %v", err)
		}
		if !confirmed.RequiresConfirmation || confirmed.Mode != model.BotExecutionAsync {
			t.Fatalf("Confirm 命中应改判异步待确认: %+v", confirmed)
		}
		quiet, err := botPrepareToolCall(holder, "fs_issue", `{"action":"boom_confirm_test","query":"q"}`)
		if err != nil {
			t.Fatalf("动态确认未命中受理应成功: %v", err)
		}
		if quiet.RequiresConfirmation || quiet.Mode != model.BotExecutionSync {
			t.Fatalf("Confirm 未命中应保持同步免确认: %+v", quiet)
		}
	})
}

// TestBotActionRegistryDynamicConfirmError 是动态确认的 error 语义回归：
// Confirm 返回的受理期参数校验错误原样上抛（不建卡），与拆分前
// botScheduleConfirmation 一致。
func TestBotActionRegistryDynamicConfirmError(t *testing.T) {
	spec := &botActionSpec{
		Tool: "fs_issue", Action: "confirm_test",
		Description: "测试动作（验收后恢复）",
		Mode:        model.BotExecutionSync, Confirmation: confirmDynamic,
		SyncExec: func(*BotService, *model.Bot, context.Context, *model.BotToolCall) (string, error) {
			return "{}", nil
		},
		Confirm: func(map[string]json.RawMessage) (bool, error) {
			return false, errs.New(errs.ErrInvalidParams.Code, "受理期校验失败")
		},
	}
	botWithTestSpecs(t, spec)
	prepared := &botPreparedCall{ToolName: "fs_issue", Action: "confirm_test", Mode: model.BotExecutionSync}
	if err := botApplyActionConfirmation(prepared, spec, map[string]json.RawMessage{}); err == nil {
		t.Fatal("Confirm 的受理期校验错误应上抛")
	}
}

// ---------- 其它测试文件的迁移 helper（原聚合兼容入口的测试语义） ----------

// botMemoryTestExec 是原 execMemoryTool 聚合入口的测试替代：先解析参数取
// action（与拆分前同源），再经注册表同步分派（memory 动作全是 SyncExec，
// bot 传 nil 即可——只有能力查询消费 ToolsetVersion，那些测试自行传真实 bot）。
func botMemoryTestExec(s *BotService, call *model.BotToolCall) (string, error) {
	var args botMemoryArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	return s.botExecSyncAction(context.Background(), nil, call, args.Action,
		"不支持的 fs_memory 动作: "+args.Action)
}

// botScheduleTestExec 是原 execScheduleTool 聚合入口的测试替代：优先 AsyncExec
// 执行体直连（与拆分前测试语义一致——确认型写动作直接驱动执行体，不带确认
// 分流）；仅 SyncExec 的动作（读动作、disable、state 读写）走同步入口。
func botScheduleTestExec(s *BotService, call *model.BotToolCall) (string, error) {
	args, err := botScheduleExecArgs(call)
	if err != nil {
		return "", err
	}
	spec := botActionSpecOf("fs_schedule", args.Action)
	if spec == nil {
		return "", errs.New(errs.ErrInvalidParams.Code, "不支持的 fs_schedule 动作: "+args.Action)
	}
	if spec.AsyncExec != nil {
		out, err := spec.AsyncExec(s, context.Background(), call)
		if err != nil {
			return "", err
		}
		raw, ok := out.(json.RawMessage)
		if !ok {
			return "", errs.ErrInternal
		}
		return string(raw), nil
	}
	return spec.SyncExec(s, nil, context.Background(), call)
}

// botScheduleTestExecSync 与生产同步分派同口径（SyncExec 优先）：专门覆盖
// 免确认 update、run 的 dry_run 门禁、disable 等同步入口——这些分支若被改
// 坏，同步路径用例才会失败（fast-ship-bot R1 Note 3：原聚合入口 AsyncExec
// 优先，同步入口无直接覆盖）。
func botScheduleTestExecSync(s *BotService, call *model.BotToolCall) (string, error) {
	args, err := botScheduleExecArgs(call)
	if err != nil {
		return "", err
	}
	spec := botActionSpecOf("fs_schedule", args.Action)
	if spec == nil {
		return "", errs.New(errs.ErrInvalidParams.Code, "不支持的 fs_schedule 动作: "+args.Action)
	}
	if spec.SyncExec == nil {
		return "", errs.New(errs.ErrInvalidParams.Code, "同步工具不执行该动作: fs_schedule/"+args.Action)
	}
	return spec.SyncExec(s, nil, context.Background(), call)
}
