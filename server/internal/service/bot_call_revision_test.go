package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// INT-217 端到端：调用行 revision 单调代次。状态链严格递增；每个发调用事件
// 的路径负载携带提交时点的完整投影与 revision（异步终态嵌 call 字段），浏览器
// 只按 revision 覆盖。

// readBotCallEvents 读某 bot 的全部调用事件（按 seq 序），解析成 envelope。
func readBotCallEvents(t *testing.T, queryEnv *botTestEnv, botID string) []botEventEnvelope {
	t.Helper()
	var rows []model.BotEvent
	if err := queryEnv.db.Where("bot_id = ? AND event_type IN ?", botID,
		[]string{model.BotEventToolCall, model.BotEventToolResult}).
		Order("seq ASC").Find(&rows).Error; err != nil {
		t.Fatalf("read call events: %v", err)
	}
	envs := make([]botEventEnvelope, 0, len(rows))
	for _, r := range rows {
		var env botEventEnvelope
		if err := json.Unmarshal([]byte(r.PayloadJSON), &env); err != nil {
			t.Fatalf("unmarshal event payload: %v", err)
		}
		envs = append(envs, env)
	}
	return envs
}

// eventCallProjection 取事件负载里的完整调用投影（异步后台结果嵌 call 字段，
// 其余事件顶层即投影）。返回 (status, revision, payload)。
func eventCallProjection(t *testing.T, env botEventEnvelope) (string, int64, map[string]interface{}) {
	t.Helper()
	payload, ok := env.Payload.(map[string]interface{})
	if !ok {
		t.Fatalf("事件负载不是对象: %#v", env.Payload)
	}
	if call, ok := payload["call"].(map[string]interface{}); ok {
		payload = call
	}
	status, _ := payload["status"].(string)
	rev, _ := payload["revision"].(float64)
	return status, int64(rev), payload
}

func TestBotCallRevisionLifecycle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrev")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusPendingConfirmation, string(model.BotExecutionAsync))

	row, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.Revision != 0 {
		t.Fatalf("新建行 revision 应为 0，实际 %d", row.Revision)
	}

	// 决定 approve → queued（rev 1）；决定事件带提交时点完整投影与 revision。
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	row, _ = env.bots.calls.FindByID(callID)
	if row.Status != model.BotCallStatusQueued || row.Revision != 1 {
		t.Fatalf("决定后应为 queued/1，实际 %s/%d", row.Status, row.Revision)
	}
	events := readBotCallEvents(t, env, bot.ID)
	if len(events) != 1 {
		t.Fatalf("应恰好 1 条决定事件，实际 %d", len(events))
	}
	if st, rev, payload := eventCallProjection(t, events[0]); st != "queued" || rev != 1 ||
		payload["decision"] != "approve" {
		t.Fatalf("决定事件负载应为 queued/rev1/approve，实际 %s/%d/%v", st, rev, payload["decision"])
	}

	// 认领 running（rev 2）→ 异步执行落 failed 终态（rev 3，fs_project 无异步实现）。
	env.bots.executeQueuedCall(callID)
	waitForBot(t, "调用终态", 15*time.Second, func() bool {
		row, err := env.bots.calls.FindByID(callID)
		return err == nil && row.Status.IsTerminal()
	})
	row, _ = env.bots.calls.FindByID(callID)
	if row.Revision != 3 {
		t.Fatalf("终态后 revision 应为 3（认领+终态各 +1），实际 %d", row.Revision)
	}

	events = readBotCallEvents(t, env, bot.ID)
	if len(events) != 3 {
		t.Fatalf("应有决定/running/终态 3 条事件，实际 %d: %+v", len(events), events)
	}
	if events[1].Kind != "tool_call" || events[1].Phase != "running" {
		t.Fatalf("第二条事件应为 running 的 tool_call: %s/%s", events[1].Kind, events[1].Phase)
	}
	if st, rev, _ := eventCallProjection(t, events[1]); st != "running" || rev != 2 {
		t.Fatalf("running 事件负载应为 running/rev2，实际 %s/%d", st, rev)
	}
	if events[2].Kind != "tool_result" || events[2].Phase != "terminal" {
		t.Fatalf("第三条事件应为终态 tool_result: %s/%s", events[2].Kind, events[2].Phase)
	}
	st, rev, _ := eventCallProjection(t, events[2])
	if st != string(row.Status) || rev != 3 {
		t.Fatalf("终态事件嵌套投影应为 %s/rev3，实际 %s/%d", row.Status, st, rev)
	}
	// 异步终态保留 web 已消费的顶层字段：status/message_id/message；完整
	// 调用投影嵌在 call 字段。
	top, ok := events[2].Payload.(map[string]interface{})
	if !ok {
		t.Fatalf("终态事件负载不是对象")
	}
	for _, key := range []string{"status", "message_id", "message", "call"} {
		if _, ok := top[key]; !ok {
			t.Fatalf("异步终态负载缺顶层字段 %s: %v", key, top)
		}
	}
}

func TestBotCallRevisionCancelRequestedBump(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrevcancel")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionAsync))

	row, _ := env.bots.calls.FindByID(callID)
	if row.Revision != 0 {
		t.Fatalf("夹具行 revision 应为 0，实际 %d", row.Revision)
	}
	if _, err := env.bots.CancelCall(userID, bot.ID, callID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// running 分支只记取消标记（cancel_requested_at 进投影），同样 bump。
	row, _ = env.bots.calls.FindByID(callID)
	if row.CancelRequestedAt == nil || row.Revision != 1 {
		t.Fatalf("取消标记应落上且 revision 为 1，实际 %v/%d", row.CancelRequestedAt, row.Revision)
	}
}

func TestBotCallRevisionProposalEventRevisionZero(t *testing.T) {
	// 真实受理路径：CreateTx 后内存对象即行状态，提案事件投影 revision == 行 revision（0）。
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{
			{ID: "call-rev", Name: "fs_project", Args: `{"action":"create","name":"rev项目","description":"d"}`},
		}},
	)
	userID := createBotTestUser(t, env.db, "botrevprop")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "建个项目", ClientMessageID: "s-rev"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "受理完成", 15*time.Second, func() bool {
		var row model.BotToolCall
		if err := env.db.Where("provider_tool_call_id = ?", "call-rev").First(&row).Error; err != nil {
			return false
		}
		return row.Status == model.BotCallStatusPendingConfirmation
	})
	var row model.BotToolCall
	if err := env.db.Where("provider_tool_call_id = ?", "call-rev").First(&row).Error; err != nil {
		t.Fatalf("find call: %v", err)
	}
	if row.Revision != 0 {
		t.Fatalf("受理行 revision 应为 0，实际 %d", row.Revision)
	}
	for _, env0 := range readBotCallEvents(t, env, bot.ID) {
		if env0.Kind != "tool_call" {
			continue
		}
		st, rev, _ := eventCallProjection(t, env0)
		if st != "pending_confirmation" || rev != 0 {
			t.Fatalf("提案事件负载应为 pending_confirmation/rev0，实际 %s/%d", st, rev)
		}
		return
	}
	t.Fatalf("未找到提案事件")
}

func TestBotCallRevisionRevertQueued(t *testing.T) {
	// 授权失效回退：UPDATE→重读→事件同一事务，回退事件带提交时点 revision，
	// 浏览器据此让确认卡重新出现（合法回退，queued→pending_confirmation）。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrevrevert")
	bot := env.createBot(t, userID)
	now := time.Now().UTC()
	authz := &model.BotAuthorization{
		ID: "authz-rev-" + uuid.NewString()[:8], BotID: bot.ID,
		ToolName: "fs_project", Action: "create",
		ScopeKind: "global", ScopeID: "global",
		ConstraintsJSON: "{}", RuleHash: "hash-rev-" + uuid.NewString()[:8],
		CreatedAt: now,
	}
	if err := env.bots.db.Create(authz).Error; err != nil {
		t.Fatalf("seed authz: %v", err)
	}
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("authorization_id", authz.ID).Error; err != nil {
		t.Fatalf("attach authz: %v", err)
	}
	// 撤销授权后 worker 认领前的授权复核走 revertQueuedCall。
	if _, err := env.bots.authz.Revoke(authz.ID, now); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	env.bots.executeQueuedCall(callID)

	row, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.Status != model.BotCallStatusPendingConfirmation || row.Revision != 1 {
		t.Fatalf("回退后应为 pending_confirmation/1，实际 %s/%d", row.Status, row.Revision)
	}
	if row.AuthorizationID != nil {
		t.Fatalf("回退应清掉授权关联: %v", *row.AuthorizationID)
	}
	found := false
	for _, ev := range readBotCallEvents(t, env, bot.ID) {
		if ev.Kind != "tool_call" || ev.Phase != "awaiting_confirmation" {
			continue
		}
		st, rev, payload := eventCallProjection(t, ev)
		if st != "pending_confirmation" || rev != 1 {
			t.Fatalf("回退事件负载应为 pending_confirmation/rev1，实际 %s/%d", st, rev)
		}
		if auth, ok := payload["authorization_id"]; ok && auth != nil {
			t.Fatalf("回退事件投影的授权应为空: %v", auth)
		}
		found = true
	}
	if !found {
		t.Fatalf("未找到回退事件")
	}
}

// 脏 result_preview_json（非法 JSON、历史竞速路径的空串）按无结果处理：
// RawMessage 携带不合法字节会让事件 payload 的 json.Marshal 报错，把终态
// 事务整个回滚（INT-217 云审 P2）。
func TestBotCallResponseToleratesDirtyResultPreview(t *testing.T) {
	cases := []string{"not-json{{", "", "  "}
	for _, dirty := range cases {
		call := &model.BotToolCall{
			ID: "call-dirty", BotID: "bot-1", Status: model.BotCallStatusSucceeded,
			ResultPreviewJSON: &dirty,
		}
		resp := botToolCallResponseOf(call)
		if resp.ResultPreview != nil {
			t.Fatalf("脏值 %q 应按无结果处理，得到 %s", dirty, string(resp.ResultPreview))
		}
		if _, err := json.Marshal(resp); err != nil {
			t.Fatalf("脏值 %q 的投影应可序列化: %v", dirty, err)
		}
	}
}
