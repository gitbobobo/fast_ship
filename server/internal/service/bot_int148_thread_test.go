package service

// INT-148 第 1、2、4 部分（机器人面）：工具集 v9 的 fs_thread 新动作与描述、
// 读取形状补 run_status/pull_requests/child_thread_id、结算与队列恢复的授权
// 范围与真实派发、帮助文本语义。v1–v8 的冻结基线断言在 bot_int103_test.go。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

func TestBotInt148ThreadToolsetV9(t *testing.T) {
	v8 := botToolsetVersionOf(8)
	v9 := botToolsetVersionOf(9)
	if len(v9.schemas) != 11 {
		t.Fatalf("v9 工具数应为 11，实得 %d", len(v9.schemas))
	}
	if v9.hash == v8.hash || v9.hash == botToolsetV8FrozenHash {
		t.Fatalf("v9 与 v8 的 tools_hash 必须不同")
	}
	// 当前发布版本路由到 v9。
	if botToolsetOf().hash != v9.hash {
		t.Fatalf("botToolsetOf 应指向 v9")
	}

	thread := v9.byName["fs_thread"]
	if thread == nil {
		t.Fatalf("v9 应含 fs_thread")
	}
	if len(thread.Actions) != 11 {
		t.Fatalf("v9 fs_thread 应有 11 个动作，实得 %d", len(thread.Actions))
	}
	// 三个新写动作：异步 + 需确认，与 send/stop 一致；不计入只读。
	for _, action := range []string{"settle", "unsettle", "resume_queue"} {
		def, ok := v9.byAction["fs_thread/"+action]
		if !ok {
			t.Fatalf("v9 fs_thread 缺动作 %s", action)
		}
		if def.Mode != model.BotExecutionAsync || !def.RequiresConfirmation {
			t.Fatalf("%s 应为异步需确认动作: %+v", action, def)
		}
		if botReadOnlyActions["fs_thread/"+action] {
			t.Fatalf("%s 是写动作，不得列入只读", action)
		}
		rule, ok := botActionRules["fs_thread/"+action]
		if !ok || len(rule.Required) != 2 || rule.Required[0].Name != "env_id" || rule.Required[1].Name != "thread_id" {
			t.Fatalf("%s 的字段规则应为 env_id+thread_id: %+v", action, rule)
		}
	}
	// v9 的 fs_thread 参数用活跃结构体；v8 仍冻结在共享副本。
	if thread.ArgsType.Name() != "botThreadArgs" {
		t.Fatalf("v9 fs_thread 应使用活跃参数结构体，实得 %s", thread.ArgsType.Name())
	}
	if v8.byName["fs_thread"].ArgsType.Name() != "botThreadArgsV5Frozen" {
		t.Fatalf("v8 fs_thread 应冻结在 botThreadArgsV5Frozen")
	}

	// schema：action 枚举含新动作，必填字段描述由 botActionRules 注出。
	raw, _ := json.Marshal(thread.Parameters)
	var schema struct {
		Properties struct {
			Action struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	for _, want := range []string{"settle", "unsettle", "resume_queue"} {
		found := false
		for _, e := range schema.Properties.Action.Enum {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("v9 action 枚举应含 %s: %v", want, schema.Properties.Action.Enum)
		}
	}
	for _, want := range []string{"settle=env_id、thread_id", "unsettle=env_id、thread_id", "resume_queue=env_id、thread_id"} {
		if !strings.Contains(schema.Properties.Action.Description, want) {
			t.Fatalf("action 描述应注出 %q: %s", want, schema.Properties.Action.Description)
		}
	}

	// 受理路径：缺 thread_id 拒绝；参数齐备受理且带确认标记。
	v9Args := `{"action":"settle","env_id":"e1"}`
	if _, err := botPrepareToolCall(v9, "fs_thread", v9Args); err == nil {
		t.Fatalf("settle 缺 thread_id 应被拒绝")
	}
	prepared, err := botPrepareToolCall(v9, "fs_thread", `{"action":"settle","env_id":"e1","thread_id":"t1"}`)
	if err != nil {
		t.Fatalf("settle 应被受理: %v", err)
	}
	if prepared.Mode != model.BotExecutionAsync || !prepared.RequiresConfirmation {
		t.Fatalf("settle 受理应为异步需确认: %+v", prepared)
	}
	for _, action := range []string{"unsettle", "resume_queue"} {
		if _, err := botPrepareToolCall(v9, "fs_thread", `{"action":"`+action+`","env_id":"e1"}`); err == nil {
			t.Fatalf("%s 缺 thread_id 应被拒绝", action)
		}
	}
	// 工具级描述写明新动作（写操作、需确认）与读取面新字段。
	for _, want := range []string{"settle/unsettle/resume_queue", "需确认", "run_status", "pull_requests"} {
		if !strings.Contains(thread.Description, want) {
			t.Fatalf("v9 fs_thread 描述应含 %q: %s", want, thread.Description)
		}
	}
	// run_status 语义写全：status 非 idle 时线程还没结束，它只是最近一次根
	// run 的状态；status=idle 才代表最终结局——不能被读成「当前结局」。
	for _, want := range []string{"还没结束", "最近一次根 run 的状态", "status=idle 时它才是最终结局"} {
		if !strings.Contains(thread.Description, want) {
			t.Fatalf("v9 fs_thread 描述应写明 run_status 语义 %q: %s", want, thread.Description)
		}
	}
	// stop/send 描述写清真实效果（挂起队列、PR 监听、子任务、activity.stopping）。
	stopDesc := v9.byAction["fs_thread/stop"].Description
	for _, want := range []string{"挂起", "PR 监听", "空操作", "stopped=false", "activity.stopping"} {
		if !strings.Contains(stopDesc, want) {
			t.Fatalf("stop 描述应含 %q: %s", want, stopDesc)
		}
	}
	sendDesc := v9.byAction["fs_thread/send"].Description
	if !strings.Contains(sendDesc, "resume_queue") {
		t.Fatalf("send 描述应指向 resume_queue: %s", sendDesc)
	}
	// send 的「继承挂起」限定：只有队列里已有被挂起的排队消息时才继承。
	if !strings.Contains(sendDesc, "已有被挂起的排队消息") || !strings.Contains(sendDesc, "继承挂起") {
		t.Fatalf("send 描述应把继承挂起限定在队列已有被挂起的排队消息时: %s", sendDesc)
	}
	// settle 描述写明真实副作用与拒绝条件（清置顶/PR 监听、忙时拒绝）。
	settleDesc := v9.byAction["fs_thread/settle"].Description
	for _, want := range []string{"清掉置顶", "PR 监听", "会被拒绝"} {
		if !strings.Contains(settleDesc, want) {
			t.Fatalf("settle 描述应含 %q: %s", want, settleDesc)
		}
	}
}

// 读取形状：摘要带 run_status 与 pull_requests；subagent 条目带 child_thread_id。
func TestBotInt148ThreadReadShape(t *testing.T) {
	summary := normalizeT3ThreadShell(int148ThreadEnv(), &t3ThreadShell{
		ID: "th-1", Status: "completed",
		PullRequests: []json.RawMessage{
			json.RawMessage(`{"host":"github.com","repository":"o/r","number":1,"url":"u",
				"snapshot":{"state":"open","isDraft":true},"watch":{}}`),
			json.RawMessage(`{"host":"github.com","repository":"o/x","number":2,"url":"u","snapshot":null}`),
		},
	}, nil)
	slim := botSlimThreadSummary(&summary)
	if slim["run_status"] != "completed" {
		t.Fatalf("slim 摘要应带 run_status: %+v", slim)
	}
	prs, ok := slim["pull_requests"].([]api.T3ThreadPullRequest)
	if !ok || len(prs) != 2 || !prs[0].Watching || prs[1].Watching {
		t.Fatalf("slim 摘要应带 pull_requests（watching 原样）: %+v", slim["pull_requests"])
	}
	if prs[0].IsDraft == nil || !*prs[0].IsDraft {
		t.Fatalf("is_draft 应原样带出: %+v", prs[0])
	}

	child := "th-child"
	item := &T3ThreadItem{Id: "i1", Type: T3ThreadItemTypeSubagent, ChildThreadId: &child}
	slimItem := botSlimThreadItemSummary(item)
	if slimItem["child_thread_id"] != child {
		t.Fatalf("subagent 条目应带 child_thread_id: %+v", slimItem)
	}
	// 补读条目（botSlimThreadItem）与精简条目同判定：模型压缩后重新进入
	// 子线程的唯一入口，丢了就找不回来。
	fullItem := botSlimThreadItem(item)
	if fullItem["child_thread_id"] != child {
		t.Fatalf("补读条目应带 child_thread_id: %+v", fullItem)
	}
	plain := botSlimThreadItemSummary(&T3ThreadItem{Id: "i2", Type: T3ThreadItemTypeUserMessage})
	if _, has := plain["child_thread_id"]; has {
		t.Fatalf("非 subagent 条目不得带 child_thread_id: %+v", plain)
	}
	plainFull := botSlimThreadItem(&T3ThreadItem{Id: "i4", Type: T3ThreadItemTypeUserMessage})
	if _, has := plainFull["child_thread_id"]; has {
		t.Fatalf("补读条目同样不得凭空带 child_thread_id: %+v", plainFull)
	}
	orphan := botSlimThreadItemSummary(&T3ThreadItem{Id: "i3", Type: T3ThreadItemTypeSubagent, ChildThreadId: api.Ptr("")})
	if _, has := orphan["child_thread_id"]; has {
		t.Fatalf("空 child_thread_id 不得带出: %+v", orphan)
	}
	orphanFull := botSlimThreadItem(&T3ThreadItem{Id: "i5", Type: T3ThreadItemTypeSubagent, ChildThreadId: api.Ptr("")})
	if _, has := orphanFull["child_thread_id"]; has {
		t.Fatalf("补读条目空 child_thread_id 同样不得带出: %+v", orphanFull)
	}
}

func TestBotInt148HelpTextThreadSemantics(t *testing.T) {
	for _, want := range []string{
		"fs_thread launch/send/stop/settle/unsettle/resume_queue",
		"run.interrupt + holdQueue",
		"activity.stopping",
		"要先 resume_queue",
		"settled=true 才看得到",
		"默认不含已结算",
		// list 结果自证实际生效的筛选：帮助文本写明 settled_filter 的含义。
		"settled_filter",
		"未结算的线程",
		"空操作",
		"PR 监听继续",
		"run_status",
		"pull_requests",
		"watching",
		"child_thread_id",
		// run_status 语义限定与 settle 的真实副作用/拒绝条件。
		"最近一次根 run 的状态",
		"status=idle 时它才是最终结局",
		"清掉置顶",
		"会被拒绝",
		"继承挂起",
	} {
		if !strings.Contains(botHelpText, want) {
			t.Fatalf("帮助文本应含 %q", want)
		}
	}
}

// fs_thread list 的 settled 默认语义：默认不含已结算线程（settle 之后离开
// 默认列表，settled=true 仍可见）；settled=false 与默认一致；settled=true
// 只看已结算。实际生效的筛选以 settled_filter 随结果回带（附 note 说明），
// 旧版冻结 schema 的会话也能自证口径。纯函数直测 botSlimThreadList，不起
// T3。同时钉住 v9 描述与帮助文本口径一致（list 默认口径、stop 的空操作边界）。
func TestBotInt148ThreadListSettledFilter(t *testing.T) {
	thread := func(id string, settled bool) T3ThreadSummary {
		return T3ThreadSummary{
			Id: id, Title: id, Status: api.T3ThreadSummaryStatusIdle,
			Settled: settled, UpdatedAt: "2026-01-01T00:00:00Z",
		}
	}
	list := &T3ThreadListResponse{
		Items:        []T3ThreadSummary{thread("th-open", false), thread("th-settled", true)},
		Environments: []api.T3Environment{{Id: "e1", Name: "健康", Status: api.T3EnvironmentStatusOk}},
	}
	idsOf := func(t *testing.T, out map[string]interface{}) map[string]bool {
		t.Helper()
		items, ok := out["items"].([]map[string]interface{})
		if !ok {
			t.Fatalf("items 形状不符: %+v", out["items"])
		}
		ids := map[string]bool{}
		for _, item := range items {
			ids[item["id"].(string)] = true
		}
		return ids
	}

	defOut, err := botSlimThreadList(list, &botThreadArgs{})
	if err != nil {
		t.Fatalf("默认 list: %v", err)
	}
	defIDs := idsOf(t, defOut)
	if !defIDs["th-open"] || defIDs["th-settled"] || len(defIDs) != 1 {
		t.Fatalf("默认 list 应只含未结算线程: %v", defIDs)
	}
	if defOut["total_items"] != 1 {
		t.Fatalf("默认 total_items 应为 1: %+v", defOut)
	}
	// settled_filter 回带实际生效的筛选：默认（省略 settled）为 false，且附
	// 说明让冻结在旧 schema 的会话也能自证口径；total_items 只覆盖该侧。
	if defOut["settled_filter"] != false {
		t.Fatalf("默认 list 的 settled_filter 应为 false: %+v", defOut["settled_filter"])
	}
	if note, _ := defOut["settled_filter_note"].(string); !strings.Contains(note, "settled=true") {
		t.Fatalf("settled_filter_note 应说明筛选口径: %+v", defOut["settled_filter_note"])
	}
	falseOut, err := botSlimThreadList(list, &botThreadArgs{Settled: api.Ptr(false)})
	if err != nil {
		t.Fatalf("settled=false list: %v", err)
	}
	if idsOf(t, falseOut)["th-settled"] {
		t.Fatalf("settled=false 应与默认一致: %v", falseOut["items"])
	}
	if falseOut["settled_filter"] != false {
		t.Fatalf("settled=false 的 settled_filter 应为 false: %+v", falseOut["settled_filter"])
	}
	trueOut, err := botSlimThreadList(list, &botThreadArgs{Settled: api.Ptr(true)})
	if err != nil {
		t.Fatalf("settled=true list: %v", err)
	}
	trueIDs := idsOf(t, trueOut)
	if !trueIDs["th-settled"] || trueIDs["th-open"] || len(trueIDs) != 1 {
		t.Fatalf("settled=true 应只含已结算线程: %v", trueIDs)
	}
	if trueOut["settled_filter"] != true {
		t.Fatalf("settled=true 的 settled_filter 应为 true: %+v", trueOut["settled_filter"])
	}

	// 行为闭环：settle 之后该线程离开默认列表，settled=true 能看到。
	list.Items[0].Settled = true
	afterDefault, err := botSlimThreadList(list, &botThreadArgs{})
	if err != nil {
		t.Fatalf("settle 后默认 list: %v", err)
	}
	if idsOf(t, afterDefault)["th-open"] {
		t.Fatalf("settle 后默认 list 不应返回该线程: %v", afterDefault["items"])
	}
	afterTrue, err := botSlimThreadList(list, &botThreadArgs{Settled: api.Ptr(true)})
	if err != nil {
		t.Fatalf("settle 后 settled=true list: %v", err)
	}
	if !idsOf(t, afterTrue)["th-open"] {
		t.Fatalf("settle 后 settled=true 应能看到该线程: %v", afterTrue["items"])
	}

	// 描述与行为一致：v9 的 list/settle 写明默认口径，stop 写明无可中断
	// run 时的空操作边界。
	v9 := botToolsetVersionOf(9)
	listDesc := v9.byAction["fs_thread/list"].Description
	for _, want := range []string{"已结算", "settled=true"} {
		if !strings.Contains(listDesc, want) {
			t.Fatalf("v9 list 描述应含 %q: %s", want, listDesc)
		}
	}
	settleDesc := v9.byAction["fs_thread/settle"].Description
	if !strings.Contains(settleDesc, "settled=true") {
		t.Fatalf("v9 settle 描述应指向 settled=true: %s", settleDesc)
	}
	stopDesc := v9.byAction["fs_thread/stop"].Description
	for _, want := range []string{"空操作", "stopped=false", "监听继续"} {
		if !strings.Contains(stopDesc, want) {
			t.Fatalf("v9 stop 描述应含 %q: %s", want, stopDesc)
		}
	}
}

// 8→9 升级通知只讲 v9 内容；7→9 级联补 v8 的时区与指令写法。
func TestBotInt148ToolsetChangeNote(t *testing.T) {
	note89 := botToolsetChangeNote(8, 9)
	for _, want := range []string{"resume_queue", "run_status", "pull_requests"} {
		if !strings.Contains(note89, want) {
			t.Fatalf("8→9 升级通知应说明 v9 变更 %q: %s", want, note89)
		}
	}
	// 默认不含已结算是升级后立刻生效的行为变更，存量会话必须被告知
	// （冻结 schema 的旧口径仍写着「true 只看已收进历史区的」）。
	for _, want := range []string{"默认不含已结算线程", "settled=true", "settled_filter"} {
		if !strings.Contains(note89, want) {
			t.Fatalf("8→9 升级通知应写明 list 默认口径变更 %q: %s", want, note89)
		}
	}
	if strings.Contains(note89, "用户时区") {
		t.Fatalf("8→9 升级通知不应级联 v8 内容: %s", note89)
	}
	note79 := botToolsetChangeNote(7, 9)
	if !strings.Contains(note79, "用户时区") || !strings.Contains(note79, "resume_queue") {
		t.Fatalf("7→9 升级通知应级联 v8 内容并说明 v9: %s", note79)
	}
}

// resolveThreadScope：结算按线程（thread_settle），恢复队列单独一种
// （thread_queue），都绑定 thread_id 与环境。
func TestBotInt148ThreadWriteScope(t *testing.T) {
	cases := []struct {
		action   string
		args     string
		wantKind string
	}{
		{"settle", `{"action":"settle","env_id":"e1","thread_id":"t1"}`, "thread_settle"},
		{"unsettle", `{"action":"unsettle","env_id":"e1","thread_id":"t1"}`, "thread_settle"},
		{"resume_queue", `{"action":"resume_queue","env_id":"e1","thread_id":"t1"}`, "thread_queue"},
	}
	ts := botToolsetVersionOf(botToolsetVersion)
	for _, tc := range cases {
		prepared, err := botPrepareToolCall(ts, "fs_thread", tc.args)
		if err != nil {
			t.Fatalf("%s prepare: %v", tc.action, err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.args), &raw); err != nil {
			t.Fatalf("unmarshal args: %v", err)
		}
		svc := &BotService{}
		svc.resolveThreadScope(prepared, raw)
		if prepared.ScopeKind != tc.wantKind {
			t.Fatalf("%s scope kind 应为 %s，实得 %s", tc.action, tc.wantKind, prepared.ScopeKind)
		}
		if prepared.ScopeID != "t1" {
			t.Fatalf("%s scope id 应为线程 ID，实得 %q", tc.action, prepared.ScopeID)
		}
		if prepared.EnvironmentID == nil || *prepared.EnvironmentID != "e1" {
			t.Fatalf("%s 应绑定环境: %+v", tc.action, prepared.EnvironmentID)
		}
	}
}

// pairBotT3 在 bot 测试库配对一个指向 fake T3 的环境，并把 bot 服务指向同一
// fake 的新 T3ThreadsService（setupBotTestEnv 不迁移 T3 表，这里补齐）。
func pairBotT3(t *testing.T, env *botTestEnv, userID, name string) (*fakeT3Environment, string) {
	t.Helper()
	if err := env.db.AutoMigrate(&model.T3Environment{}); err != nil {
		t.Fatalf("migrate t3 env: %v", err)
	}
	fake := newFakeT3Environment(t)
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	fake.baseURL = server.URL

	hub := NewT3ShellHub(zap.NewNop())
	t.Cleanup(hub.Close)
	repo := repository.NewT3EnvironmentRepository(env.db)
	svc := NewT3ThreadsService(repo, env.cfg, zap.NewNop(), hub)
	svc.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 2 * time.Second, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second, ShellFirstSync: 2 * time.Second}
	env.bots.threads = svc
	envSvc := NewT3EnvironmentService(repo, env.cfg, zap.NewNop(), hub)
	created, err := envSvc.Create(context.Background(), userID, &CreateT3EnvironmentRequest{Name: name, PairingUrl: fake.pairingURL(fake.baseURL)})
	if err != nil {
		t.Fatalf("pair t3 env: %v", err)
	}
	if created.Access != T3EnvironmentAccessOperate {
		t.Fatalf("access = %v", created.Access)
	}
	return fake, created.Id
}

// runThreadWrite 端到端：三个动作经服务层派发到 fake T3，payload 齐备；
// 能力缺失时错误原样上抛（不吞成 invalid params）。
func TestBotInt148ThreadWriteDispatch(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "t3settle")
	bot := env.createBot(t, userID)
	fake, envID := pairBotT3(t, env, userID, "机器人结算机")

	fake.mu.Lock()
	fake.configValue = int148SettlementConfig(true)
	fake.mu.Unlock()

	dispatch := func(action string) (interface{}, error) {
		call := &model.BotToolCall{
			BotID:                bot.ID,
			ToolName:             "fs_thread",
			Action:               action,
			ArgumentsJSON:        `{"action":"` + action + `","env_id":"` + envID + `","thread_id":"th-1"}`,
			ExecutionContextJSON: `{}`,
		}
		return env.bots.runThreadWrite(context.Background(), call)
	}

	for _, action := range []string{"settle", "unsettle", "resume_queue"} {
		if _, err := dispatch(action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	seen := map[string]map[string]any{}
	for _, rpc := range fake.recordedRPC() {
		if rpc.method != "orchestration.dispatchCommand" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(rpc.payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		seen[payload["type"].(string)] = payload
	}
	for _, commandType := range []string{"thread.settle", "thread.unsettle", "queue.resume"} {
		payload, ok := seen[commandType]
		if !ok {
			t.Fatalf("缺 %s 命令: %v", commandType, seen)
		}
		if payload["threadId"] != "th-1" {
			t.Fatalf("%s threadId 不符: %+v", commandType, payload)
		}
		if id, _ := payload["commandId"].(string); id == "" {
			t.Fatalf("%s 缺 commandId: %+v", commandType, payload)
		}
	}

	// 能力缺失：错误码与提示原样透出给调用方。
	fake.mu.Lock()
	fake.configValue = mustJSON(map[string]any{"providers": map[string]any{}})
	fake.mu.Unlock()
	_, err := dispatch("settle")
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrT3Upstream.Code {
		t.Fatalf("能力缺失应为 50202，实得 %v", err)
	}
	if !strings.Contains(err.Error(), t3SettlementUnsupportedMessage) {
		t.Fatalf("能力缺失应带明确提示，实得 %v", err)
	}
}
