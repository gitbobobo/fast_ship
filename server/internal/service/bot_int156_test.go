package service

// INT-156 定时任务巡检降本：① 工具集 v11（v1–v10 冻结基线不动，v11 只演进
// fs_thread/fs_schedule 的工具与动作描述）；② list 每项自带 activity 与末条
// 回复摘要 last_reply（单条失败置 detail_unavailable，整页 15 线程的字符增量
// 有纯函数层估算）；③ schedule run 运行时上下文直接带任务小状态 task_state，
// state_set 写入与当前完全相同的内容短路返回 unchanged。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// botToolsetV11FrozenHash 是 INT-156 发布的 v11 wire 字节基线。v11 只改
// fs_thread/fs_schedule 的工具与动作描述；之后任何改动都会让存量会话升级后
// 的 tools_hash 漂移，必须先递增版本再改字节。
const botToolsetV11FrozenHash = "7ef3e64ba0ba992e6cf0955b992e8454a658d8bb1dd73903de03d1adbe551188"

// ---------- 1. 工具集 v11：描述演进、字节钉死、冻结基线不动 ----------

func TestBotInt156ToolsetV11(t *testing.T) {
	// v11 自 INT-159 起冻结为基线；当前发布版本在其之上演进，但不得低于
	// v11，否则下方 v11 基线断言失去路由前提。
	if botToolsetVersion < 11 {
		t.Fatalf("v11 基线断言要求当前版本至少为 11，实得 %d", botToolsetVersion)
	}
	v10 := botToolsetVersionOf(10)
	v11 := botToolsetVersionOf(11)
	// 不新增工具、不改参数面：工具数与 v10 一致，只有两份工具描述的字节不同。
	if len(v11.schemas) != len(v10.schemas) {
		t.Fatalf("v11 工具数应与 v10 一致: %d vs %d", len(v11.schemas), len(v10.schemas))
	}
	if v11.hash == v10.hash {
		t.Fatalf("v11 与 v10 的 tools_hash 不应相同")
	}
	if v11.hash != botToolsetV11FrozenHash {
		t.Fatalf("v11 tools_hash 偏离基线: got %s want %s", v11.hash, botToolsetV11FrozenHash)
	}
	// v1–v10 各冻结基线原样通过：v11 的演进不得回写任何已发布版本。
	for _, tc := range []struct {
		version int64
		want    string
	}{
		{1, botToolsetV1FrozenHash},
		{2, botToolsetV2FrozenHash},
		{3, botToolsetV3FrozenHash},
		{4, botToolsetV4FrozenHash},
		{5, botToolsetV5FrozenHash},
		{6, botToolsetV6FrozenHash},
		{7, botToolsetV7FrozenHash},
		{8, botToolsetV8FrozenHash},
		{9, botToolsetV9FrozenHash},
		{10, botToolsetV10FrozenHash},
	} {
		if got := botToolsetVersionOf(tc.version).hash; got != tc.want {
			t.Fatalf("v%d tools_hash 偏离冻结基线: got %s want %s", tc.version, got, tc.want)
		}
	}

	// v11 只改 fs_thread/fs_schedule 两份工具描述，其余工具的 wire 字节与 v10 逐字节一致。
	for i, tool := range v10.tools {
		switch tool.Name {
		case "fs_thread", "fs_schedule":
			continue
		}
		before, _ := json.Marshal(v10.schemas[i])
		after, _ := json.Marshal(v11.schemas[i])
		if string(before) != string(after) {
			t.Fatalf("v11 不得改写 v10 的 %s wire 字节", tool.Name)
		}
	}

	// fs_thread：工具级描述带 enrichment 说明，list 动作描述点名 last_reply/activity。
	thread := v11.byName["fs_thread"]
	for _, want := range []string{"last_reply", "activity", "detail_unavailable", "逐线程 get"} {
		if !strings.Contains(thread.Description, want) {
			t.Fatalf("v11 fs_thread 描述应含 %q: %s", want, thread.Description)
		}
	}
	if !strings.Contains(thread.Description, "run_status") || !strings.Contains(thread.Description, "pull_requests") {
		t.Fatalf("v11 fs_thread 描述应保留 v9 的 run_status/pull_requests 语义: %s", thread.Description)
	}
	listDesc := v11.byAction["fs_thread/list"].Description
	for _, want := range []string{"last_reply", "activity", "detail_unavailable"} {
		if !strings.Contains(listDesc, want) {
			t.Fatalf("v11 list 动作描述应含 %q: %s", want, listDesc)
		}
	}

	// fs_schedule：工具描述带 task_state 与 unchanged 语义，动作清单不变。
	sched := v11.byName["fs_schedule"]
	if len(sched.Actions) != 10 {
		t.Fatalf("v11 fs_schedule 应仍是 10 个动作: %+v", sched)
	}
	for _, want := range []string{"task_state", "unchanged", "开局不必再调 state_get", "需要用户确认", "省略即用户时区", "[[SILENT]]"} {
		if !strings.Contains(sched.Description, want) {
			t.Fatalf("v11 fs_schedule 描述应含 %q: %s", want, sched.Description)
		}
	}
	if !strings.Contains(v11.byAction["fs_schedule/state_get"].Description, "开局") {
		t.Fatalf("v11 state_get 动作描述应说明开局不必调: %s", v11.byAction["fs_schedule/state_get"].Description)
	}
	if !strings.Contains(v11.byAction["fs_schedule/state_set"].Description, "unchanged") {
		t.Fatalf("v11 state_set 动作描述应说明 unchanged 语义: %s", v11.byAction["fs_schedule/state_set"].Description)
	}
}

// 升级通知：10→11 只讲 v11 内容；9→11 级联补 v10 的确认语义。
func TestBotInt156ToolsetChangeNote(t *testing.T) {
	note1011 := botToolsetChangeNote(10, 11)
	for _, want := range []string{"last_reply", "activity", "detail_unavailable", "task_state", "unchanged", "不必调 state_get"} {
		if !strings.Contains(note1011, want) {
			t.Fatalf("10→11 升级通知应含 %q: %s", want, note1011)
		}
	}
	// 10→11 不应重告 v10 已告知过的确认语义。
	if strings.Contains(note1011, "需要用户确认") {
		t.Fatalf("10→11 不应重告 v10 确认语义: %s", note1011)
	}
	note911 := botToolsetChangeNote(9, 11)
	for _, want := range []string{"last_reply", "需要用户确认"} {
		if !strings.Contains(note911, want) {
			t.Fatalf("9→11 应级联 v10 确认语义 %q: %s", want, note911)
		}
	}
	if strings.Contains(note911, "resume_queue") || strings.Contains(note911, "用户时区") {
		t.Fatalf("9→11 不应重告 v9 及更早的变化: %s", note911)
	}
	// 用户可见行：白话概括，不堆工具名。
	if user := botToolsetUserNote(10, 11); !strings.Contains(user, "最近回复") {
		t.Fatalf("10→11 用户说明应提到列表自带最近回复: %s", user)
	}
}

// ---------- 2. state_set 短路：相同内容回 unchanged，不推进写入时间 ----------

func TestBotInt156StateSetSameContentShortCircuits(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int156state")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	// 首次写入：正常落库，不标 unchanged。
	out := mustStateCall(t, env, run, `{"action":"state_set","state":{ "seen" : ["a-1"] }}`)
	if strings.Contains(out, `"unchanged":true`) {
		t.Fatalf("首次写入不应回 unchanged: %s", out)
	}
	first := reloadScheduleTask(t, env, task.ID)
	if first.StateJSON != `{"seen":["a-1"]}` || first.StateUpdatedAt == nil {
		t.Fatalf("首次写入应落库: %+v", first)
	}

	// 相同内容（空白不同、字节比较按紧凑化后）重复写：短路 unchanged，
	// state_updated_at 保持原值。
	out = mustStateCall(t, env, run, `{"action":"state_set","state":{"seen":["a-1"]}}`)
	for _, want := range []string{`"ok":true`, `"unchanged":true`, `"bytes":16`, `"max_bytes":8192`, `"cleared":false`, `"task_id":"` + task.ID + `"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("unchanged 回执缺 %s: %s", want, out)
		}
	}
	again := reloadScheduleTask(t, env, task.ID)
	if !again.StateUpdatedAt.Equal(*first.StateUpdatedAt) {
		t.Fatalf("短路不得推进 state_updated_at: %v vs %v", again.StateUpdatedAt, first.StateUpdatedAt)
	}
	if !again.UpdatedAt.Equal(first.UpdatedAt) {
		// unchanged 走条件 UPDATE 确认提交点状态，同样不得推动配置时间。
		t.Fatalf("短路不得推进 updated_at: %v vs %v", again.UpdatedAt, first.UpdatedAt)
	}
	if got := mustStateCall(t, env, run, `{"action":"state_get"}`); !strings.Contains(got, `"state":{"seen":["a-1"]}`) {
		t.Fatalf("state_get 应读到原值: %s", got)
	}

	// 内容不同照常写入；清空到已空同样短路。
	out = mustStateCall(t, env, run, `{"action":"state_set","state":{"seen":["a-1","a-2"]}}`)
	if strings.Contains(out, `"unchanged":true`) {
		t.Fatalf("不同内容不应回 unchanged: %s", out)
	}
	mustStateCall(t, env, run, `{"action":"state_set","state":null}`)
	out = mustStateCall(t, env, run, `{"action":"state_set"}`)
	if !strings.Contains(out, `"unchanged":true`) || !strings.Contains(out, `"cleared":true`) {
		t.Fatalf("空写空应回 unchanged+cleared: %s", out)
	}
	if got := reloadScheduleTask(t, env, task.ID); got.StateJSON != "" || got.StateUpdatedAt != nil {
		t.Fatalf("短路清空不得改状态: %+v", got)
	}

	// 键序不做语义归一：同义不同序按不同内容照常写入。
	mustStateCall(t, env, run, `{"action":"state_set","state":{"a":1,"b":2}}`)
	before := reloadScheduleTask(t, env, task.ID)
	out = mustStateCall(t, env, run, `{"action":"state_set","state":{"b":2,"a":1}}`)
	if strings.Contains(out, `"unchanged":true`) {
		t.Fatalf("键序不同不得短路: %s", out)
	}
	if got := reloadScheduleTask(t, env, task.ID); got.StateJSON != `{"b":2,"a":1}` || got.StateUpdatedAt.Equal(*before.StateUpdatedAt) {
		t.Fatalf("键序不同应照常落库推进时间: %+v", got)
	}

	// 比较前先过校验：超限与非法 JSON 一律拒绝，不会短路放行。
	if _, err := botScheduleStateCall(t, env, run, `{"action":"state_set","state":"`+strings.Repeat("x", botScheduleStateMaxBytes)+`"}`); err == nil {
		t.Fatalf("超限状态应被拒绝")
	}
	if _, err := botScheduleStateCall(t, env, run, `{"action":"state_set","state":{"broken":`); err == nil {
		t.Fatalf("非法 JSON 应被拒绝")
	}
	if got := reloadScheduleTask(t, env, task.ID); got.StateJSON != `{"b":2,"a":1}` {
		t.Fatalf("拒绝路径不得改动状态: %+v", got)
	}
	_ = userID
}

// ---------- 3. 运行时上下文注入任务小状态 ----------

// decodeScheduleRT 解出 schedule run 构建产物末条消息里的运行时上下文。
func decodeScheduleRT(t *testing.T, built *botBuiltContext, botRow *model.Bot) botScheduleRuntimeContext {
	t.Helper()
	last := built.Request.Messages[len(built.Request.Messages)-1]
	raw, _ := json.Marshal(last)
	var wire struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	prefix := botPlatformPrefix(botProjectionNonce(botRow), "运行时上下文") + "\n"
	if !strings.HasPrefix(wire.Content, prefix) {
		t.Fatalf("末条消息应为运行时上下文: %q", wire.Content)
	}
	var rt botScheduleRuntimeContext
	if err := json.Unmarshal([]byte(strings.TrimPrefix(wire.Content, prefix)), &rt); err != nil {
		t.Fatalf("unmarshal rt: %v", err)
	}
	return rt
}

func TestBotInt156RuntimeContextCarriesTaskState(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int156rt")
	bot := env.createBot(t, userID)
	updated := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.StateJSON = `{"seen":["i-7"]}`
		tk.StateUpdatedAt = &updated
	})
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	sess, err := env.bots.newScheduleSession(botRow, run)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	built, err := env.bots.buildScheduleRunContext(botRow, run, sess)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rt := decodeScheduleRT(t, built, botRow)
	// 状态原文进不可信围栏（schedule_state），带写入时间。
	if want := botFenceUntrusted(botProjectionNonce(botRow), "schedule_state", task.StateJSON); rt.TaskState != want {
		t.Fatalf("task_state 应为围栏原文: got %q want %q", rt.TaskState, want)
	}
	if rt.TaskStateUpdatedAt != updated.UTC().Format(time.RFC3339) {
		t.Fatalf("task_state_updated_at 应为 RFC3339: %q", rt.TaskStateUpdatedAt)
	}
	for _, kw := range []string{"task_state", "不必调 state_get", "unchanged", "上次已报的 ID", "运行开始时的快照", "省略表示当时为空", "仍是旧快照"} {
		if !strings.Contains(rt.Note, kw) {
			t.Fatalf("note 应说明快照语义与 unchanged 语义（缺 %q）: %s", kw, rt.Note)
		}
	}

	// 无状态的另一任务：整体省略，不注空串。
	plain := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })
	run2 := seedScheduleRun(t, env, bot.ID, plain.ID)
	sess2, err := env.bots.newScheduleSession(botRow, run2)
	if err != nil {
		t.Fatalf("session2: %v", err)
	}
	built2, err := env.bots.buildScheduleRunContext(botRow, run2, sess2)
	if err != nil {
		t.Fatalf("build2: %v", err)
	}
	rt2 := decodeScheduleRT(t, built2, botRow)
	if rt2.TaskState != "" || rt2.TaskStateUpdatedAt != "" {
		t.Fatalf("无状态任务不得注入 task_state: %+v", rt2)
	}
	_ = userID
}

// ---------- 4. fs_thread list 补 activity 与 last_reply ----------

// pairBotT3WithSnapshot 与 pairBotT3 同源，但允许在建环境前放好 shell 快照：
// fs_thread list 补 activity/last_reply 依赖 Detail，而 Detail 只信 WS 快照里
// 存在的线程。投影（HTTP 面）可以后置，按需拉取。
func pairBotT3WithSnapshot(t *testing.T, env *botTestEnv, userID, name string, snapshot json.RawMessage) (*fakeT3Environment, string) {
	t.Helper()
	if err := env.db.AutoMigrate(&model.T3Environment{}); err != nil {
		t.Fatalf("migrate t3 env: %v", err)
	}
	fake := newFakeT3Environment(t)
	fake.snapshot = snapshot
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

// threadProjection 组装 wire 投影：runs 决定 activity（活动 run 状态透传），
// visibleTurnItems 决定 last_reply 的扫描原料。
func threadProjection(threadID string, runs []any, rows []map[string]any) string {
	return mustJSONString(map[string]any{
		"thread":           appThreadFixture(threadID, "巡检-"+threadID, nil),
		"runs":             runs,
		"turnItems":        []any{},
		"visibleTurnItems": rows,
	})
}

func botInt156ThreadCall(t *testing.T, env *botTestEnv, botID, args string) map[string]interface{} {
	t.Helper()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, RequestID: "req-156",
		ProviderToolCallID: "prov-156", ToolName: "fs_thread", Action: "list",
		ArgumentsJSON: args, ArgumentsHash: "x", ExecutionMode: "sync",
		Status: model.BotCallStatusQueued, ExecutionContextJSON: "{}",
		CreatedAt: time.Now().UTC(),
	}
	content, err := env.bots.botExecuteSyncTool(context.Background(), call)
	if err != nil {
		t.Fatalf("fs_thread list: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("decode list: %v %s", err, content)
	}
	return out
}

func TestBotInt156ThreadListCarriesActivityAndLastReply(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int156thread")
	bot := env.createBot(t, userID)

	longReply := strings.Repeat("结论", 200) // 400 字符 > 300 上限
	rowsA := []map[string]any{
		{"position": 0, "visibility": "local", "sourceThreadId": "th-a", "sourceItemId": "a1",
			"item": map[string]any{"id": "a1", "type": "user_message", "status": "completed", "text": "帮我盯着这个线程"}},
		{"position": 1, "visibility": "local", "sourceThreadId": "th-a", "sourceItemId": "a2",
			"item": map[string]any{"id": "a2", "type": "assistant_message", "status": "completed", "text": longReply}},
		{"position": 2, "visibility": "local", "sourceThreadId": "th-a", "sourceItemId": "a3",
			"item": map[string]any{"id": "a3", "type": "user_message", "status": "completed", "text": "更晚的用户消息不该算回复"}},
	}
	rowsB := []map[string]any{
		{"position": 0, "visibility": "local", "sourceThreadId": "th-b", "sourceItemId": "b1",
			"item": map[string]any{"id": "b1", "type": "user_message", "status": "completed", "text": "只有用户消息"}},
	}

	projects := []any{map[string]any{"id": "p-1", "title": "P"}}
	threads := []any{
		shellThread("th-a", "在跑", "running", nil),
		shellThread("th-b", "平静", "idle", nil),
		shellThread("th-ghost", "投影缺失", "idle", nil),
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "巡检机", shellSnapshotFixture(projects, threads))
	_ = envID

	fake.mu.Lock()
	// th-a 在跑（投影带活动 run）；th-b 平静无助手回复；th-ghost 只有 shell、无投影。
	fake.projections["th-a"] = threadProjection("th-a",
		[]any{map[string]any{"id": "run-1", "status": "running", "ordinal": 1}}, rowsA)
	fake.projections["th-b"] = threadProjection("th-b", []any{}, rowsB)
	fake.mu.Unlock()

	out := botInt156ThreadCall(t, env, bot.ID, `{"action":"list"}`)
	items, _ := out["items"].([]interface{})
	if len(items) != 3 {
		t.Fatalf("应返回 3 条线程: %+v", out)
	}
	byID := map[string]map[string]interface{}{}
	for _, raw := range items {
		item := raw.(map[string]interface{})
		byID[item["id"].(string)] = item
	}

	// th-a：last_reply 取末条 assistant_message（300 字符截断 + 省略号），
	// 不被更晚的 user_message 干扰；activity 带活动 run 状态。
	a := byID["th-a"]
	if got, _ := a["last_reply"].(string); got != truncateBotRunes(longReply, botThreadLastReplyChars) {
		t.Fatalf("last_reply 应为末条助手回复的 300 字符截断: %d 字符", len([]rune(got)))
	}
	if strings.Contains(a["last_reply"].(string), "不该算回复") {
		t.Fatalf("last_reply 不得取用户消息")
	}
	actA, _ := a["activity"].(map[string]interface{})
	if actA["active_run_status"] != "running" || actA["active_run_id"] != "run-1" {
		t.Fatalf("th-a activity 应带活动 run: %+v", actA)
	}

	// th-b：无助手回复省略 last_reply；平静线程 activity 是空对象（已核实平静）。
	b := byID["th-b"]
	if _, has := b["last_reply"]; has {
		t.Fatalf("无助手回复应省略 last_reply: %+v", b)
	}
	actB, _ := b["activity"].(map[string]interface{})
	if len(actB) != 0 {
		t.Fatalf("平静线程 activity 应为空对象: %+v", actB)
	}

	// th-ghost：投影缺失，单条失败置 detail_unavailable，不中断整页。
	g := byID["th-ghost"]
	if g["detail_unavailable"] != true {
		t.Fatalf("投影缺失应置 detail_unavailable: %+v", g)
	}
	if _, has := g["last_reply"]; has {
		t.Fatalf("详情不可得时不得编造 last_reply")
	}
	if _, has := g["detail_skipped"]; has {
		t.Fatalf("尝试过但失败的不得标 detail_skipped: %+v", g)
	}

	// 描述与行为一致：v11 list 动作描述提到 last_reply/activity/detail_skipped。
	listDesc := botToolsetVersionOf(botToolsetVersion).byAction["fs_thread/list"].Description
	for _, want := range []string{"last_reply", "activity", "detail_unavailable", "detail_skipped"} {
		if !strings.Contains(listDesc, want) {
			t.Fatalf("v11 list 描述应与行为一致（缺 %q）", want)
		}
	}
}

// 整页预算到期后进回调的下标不得标 detail_unavailable——「没轮到」与
// 「真读不到」要可分（云端复审 P2）。已取消 ctx 下全部条目应标
// detail_skipped 且不写 last_reply/activity。
func TestBotInt156ThreadListSkippedNotUnavailable(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int156skip")
	// threads 服务只用 env.bots.threads——本测试 ctx 已取消、Detail 必败，
	// 连 fake T3 都不需要；若误走真实 Detail，detail_unavailable 会露馅。
	env.bots.threads = NewT3ThreadsService(nil, env.cfg, zap.NewNop(), nil)

	items := []map[string]interface{}{
		{"id": "t-1", "environment_id": "e-1"},
		{"id": "t-2", "environment_id": "e-1"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.bots.botEnrichThreadListItems(ctx, userID, items)
	for i, item := range items {
		if item["detail_skipped"] != true {
			t.Fatalf("条目 %d 应标 detail_skipped: %+v", i, item)
		}
		for _, absent := range []string{"detail_unavailable", "last_reply", "activity"} {
			if _, has := item[absent]; has {
				t.Fatalf("未尝试的条目不得写 %s: %+v", absent, item)
			}
		}
	}
}

// Detail 读到一半被预算杀掉（整页总闸或单条超时在请求途中到点）同样归
// detail_skipped：fake 端点挂起投影响应，父 ctx 在读取途中取消。
func TestBotInt156ThreadListMidFlightKillMarkedSkipped(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int156mid")

	fake, envID := pairBotT3WithSnapshot(t, env, userID, "巡检机",
		shellSnapshotFixture(nil, []any{shellThread("th-slow", "慢", "idle", nil)}))
	gate := make(chan struct{})
	fake.mu.Lock()
	fake.projections["th-slow"] = threadProjection("th-slow", []any{}, nil)
	fake.blockProjection = map[string]chan struct{}{"th-slow": gate}
	fake.mu.Unlock()
	t.Cleanup(func() { close(gate) })

	items := []map[string]interface{}{{"id": "th-slow", "environment_id": envID}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	env.bots.botEnrichThreadListItems(ctx, userID, items)

	if items[0]["detail_skipped"] != true {
		t.Fatalf("途中被杀的条目应标 detail_skipped: %+v", items[0])
	}
	if _, has := items[0]["detail_unavailable"]; has {
		t.Fatalf("预算杀掉的不得冒充详情真读不到: %+v", items[0])
	}
}

// 字符增量估算（INT-156 验收口径 ~5k）：纯函数层拼 item，量
// activity + last_reply 两个键带来的 JSON 增量。15 条平静页（都有满额
// 回复）≈4.9k，全信号打满 + UUID 长度 run id ≈7.4k；默认 50 条页线性
// 放大到 ≈16.5k/24.7k——增量随条数线性增长，超过 ~16k 结果预算的回执
// 退化为 500 字符预览（truncated=true），fs_result.read 续读。
func TestBotInt156ThreadListCharBudget(t *testing.T) {
	reply := strings.Repeat("查", botThreadLastReplyChars) // 300 字符满额
	idle := botSlimThreadActivity(T3ThreadActivity{})
	// 全信号最差面：活动 run（36 字符 UUID）+ 待应答 + 后台工作 + 停止中。
	worst := botSlimThreadActivity(T3ThreadActivity{
		ActiveRunId:                api.Ptr("9f4a2c1e-7b3d-4e5f-8a6c-1d2e3f4a5b6c"),
		ActiveRunStatus:            api.Ptr("running"),
		HasPendingRequests:         true,
		PendingBackgroundTaskCount: 3,
		Stopping:                   true,
	})
	delta := func(activity map[string]interface{}, n int) int {
		total := 0
		for i := 0; i < n; i++ {
			item := map[string]interface{}{"id": "t", "title": "x", "status": "idle", "run_status": "idle", "updated_at": "2026-10-23T00:00:00Z"}
			before, _ := json.Marshal(item)
			item["activity"] = activity
			item["last_reply"] = truncateBotRunes(reply, botThreadLastReplyChars)
			after, _ := json.Marshal(item)
			total += len([]rune(string(after))) - len([]rune(string(before)))
		}
		return total
	}
	if got := delta(idle, 15); got > 5000 {
		t.Fatalf("15 条平静线程（都有满额回复）的增量应 ≤5k 字符，实得 %d", got)
	}
	if got := delta(worst, 15); got > 7500 {
		t.Fatalf("15 条全信号打满线程的绝对上限应 ≤7.5k 字符，实得 %d", got)
	}
	// 默认 page_size=50 的同口径：增量随条数线性增长，不突变。
	if got := delta(idle, 50); got > 18000 {
		t.Fatalf("50 条平静页的增量应 ≤18k 字符，实得 %d", got)
	}
	if got := delta(worst, 50); got > 26000 {
		t.Fatalf("50 条全信号打满页的绝对上限应 ≤26k 字符，实得 %d", got)
	}
}

// ---------- 5. fs_thread list 回执豁免 schedule 确定性折叠 ----------

// 富化后的 list 回执必然越过 6000 折叠线——折成 fs_result.read 引用等于把
// 省掉的读轮次加回来（该引用回执同样超线、自折叠成死路）。豁免与 state_get
// 同列，其余动作的超大回执照常折叠。
func TestBotInt156ThreadListReplyExemptFromScheduleFold(t *testing.T) {
	big := strings.Repeat("x", botScheduleFoldChars+100)
	rows := []model.BotMessage{
		{ID: "m-1", Kind: model.BotMessageKindAssistant, ContentJSON: mustJSONString(botAssistantContent{
			ToolCalls: []botAssistantToolCall{
				{CallID: "call-list", ToolName: "fs_thread", Action: "list"},
				{CallID: "call-get", ToolName: "fs_thread", Action: "get"},
				{CallID: "call-state", ToolName: "fs_schedule", Action: "state_get"},
			}})},
		{ID: "m-2", Kind: model.BotMessageKindToolReply, CallID: api.Ptr("call-list"),
			ContentJSON: mustJSONString(botToolReplyContent{CallID: "call-list", ToolName: "fs_thread", Action: "list", Status: "succeeded", Content: big})},
		{ID: "m-3", Kind: model.BotMessageKindToolReply, CallID: api.Ptr("call-get"),
			ContentJSON: mustJSONString(botToolReplyContent{CallID: "call-get", ToolName: "fs_thread", Action: "get", Status: "succeeded", Content: big})},
		{ID: "m-4", Kind: model.BotMessageKindToolReply, CallID: api.Ptr("call-state"),
			ContentJSON: mustJSONString(botToolReplyContent{CallID: "call-state", ToolName: "fs_schedule", Action: "state_get", Status: "succeeded", Content: big})},
	}
	folded := botScheduleFoldMap(rows)
	if _, ok := folded["m-2"]; ok {
		t.Fatalf("fs_thread list 回执应豁免折叠")
	}
	if _, ok := folded["m-4"]; ok {
		t.Fatalf("state_get 豁免为 INT-123 既有行为，不得被误伤")
	}
	if _, ok := folded["m-3"]; !ok {
		t.Fatalf("fs_thread get 的超大回执应照常折叠")
	}
}
