package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// INT-110 机器人工具改进：列表筛选/分页/覆盖范围、详情可读、结果续读无损、
// 新输入只暂停写操作、Issue 写入保护。

func TestBotInt110ToolsetActionEnumsComplete(t *testing.T) {
	v4 := botToolsetVersionOf(4)
	for tool, want := range map[string][]string{
		"fs_project": {"list", "get", "create", "update"},
		"fs_issue":   {"list", "get", "comments", "timeline", "create", "update", "set_workflow", "replace_checklist", "comment"},
		"fs_thread":  {"list", "get", "item", "composer_options", "workspace_options", "launch", "send", "stop"},
	} {
		raw, _ := json.Marshal(v4.byName[tool].Parameters)
		var schema map[string]interface{}
		_ = json.Unmarshal(raw, &schema)
		props, _ := schema["properties"].(map[string]interface{})
		action, _ := props["action"].(map[string]interface{})
		enum, _ := action["enum"].([]interface{})
		got := map[string]bool{}
		for _, v := range enum {
			got[fmt.Sprint(v)] = true
		}
		for _, wantAction := range want {
			if !got[wantAction] {
				t.Fatalf("%s schema enum 缺 action %s: %v", tool, wantAction, enum)
			}
		}
		if desc, _ := action["description"].(string); !strings.Contains(desc, "必填字段") {
			t.Fatalf("%s schema 应把各动作必填字段写入 action 描述: %q", tool, desc)
		}
	}
	// v1/v2/v3 冻结：枚举仍是历史生成结果（只列第一个取值），不得被修正污染。
	for _, v := range []int64{1, 2, 3} {
		raw, _ := json.Marshal(botToolsetVersionOf(v).byName["fs_issue"].Parameters)
		if strings.Count(string(raw), `"enum":["list"`) == 0 {
			t.Fatalf("v%d fs_issue 枚举应保持冻结的旧字节: %s", v, raw)
		}
	}
	if botToolsetVersionOf(1).hash != botToolsetV1FrozenHash ||
		botToolsetVersionOf(2).hash != botToolsetV2FrozenHash ||
		botToolsetVersionOf(3).hash != botToolsetV3FrozenHash {
		t.Fatalf("v1/v2/v3 tools_hash 偏离冻结基线")
	}
}

func TestBotInt110ActionFieldValidation(t *testing.T) {
	ts := botToolsetOf()
	ok := []struct{ tool, args string }{
		// 非字符串必填字段只要求出现：items 是数组、expected_version 是数字。
		{"fs_issue", `{"action":"replace_checklist","issue_id":"i1","items":[]}`},
		{"fs_memory", `{"action":"replace","path":"MEMORY.md","content":"x","expected_version":1}`},
		{"fs_memory", `{"action":"delete","path":"MEMORY.md","expected_version":2}`},
		{"fs_memory", `{"action":"create","path":"USER.md","content":""}`},
		{"fs_thread", `{"action":"list","project_id":"p1","page":2,"page_size":10}`},
		{"fs_issue", `{"action":"update","issue_id":"i1","title":"t","expected_updated_at":"2026-01-01T00:00:00Z"}`},
	}
	for _, tc := range ok {
		if _, err := botPrepareToolCall(ts, tc.tool, tc.args); err != nil {
			t.Fatalf("%s %s 应通过校验: %v", tc.tool, tc.args, err)
		}
	}
	bad := []struct{ tool, args string }{
		{"fs_issue", `{"action":"replace_checklist","issue_id":"i1"}`},
		{"fs_memory", `{"action":"replace","path":"MEMORY.md","content":"x"}`},
		{"fs_memory", `{"action":"create","path":"","content":"x"}`},
		{"fs_project", `{"action":"create","id":"p1","name":"x"}`},
		{"fs_schedule", `{"action":"create","name":"n","cron":"* * * * *","instruction":"i","task_id":"t"}`},
		{"fs_issue", `{"action":"nope","issue_id":"i1"}`},
	}
	for _, tc := range bad {
		if _, err := botPrepareToolCall(ts, tc.tool, tc.args); err == nil {
			t.Fatalf("%s %s 应被拒绝", tc.tool, tc.args)
		}
	}
}

func TestBotInt110ReadOnlyClassification(t *testing.T) {
	syncCall := func(tool, action string) *model.BotToolCall {
		return &model.BotToolCall{ToolName: tool, Action: action, ExecutionMode: string(model.BotExecutionSync)}
	}
	for _, call := range []*model.BotToolCall{
		syncCall("fs_issue", "get"), syncCall("fs_issue", "list"),
		syncCall("fs_thread", "list"), syncCall("fs_thread", "get"), syncCall("fs_thread", "item"),
		syncCall("fs_schedule", "list"), syncCall("fs_schedule", "runs"),
		syncCall("fs_memory", "read"), syncCall("fs_result", "read"),
	} {
		if !botCallIsReadOnly(call) {
			t.Fatalf("%s/%s 应判为读类", call.ToolName, call.Action)
		}
	}
	for _, call := range []*model.BotToolCall{
		syncCall("fs_issue", "update"), syncCall("fs_issue", "replace_checklist"),
		syncCall("fs_memory", "create"), syncCall("fs_schedule", "create"),
		syncCall("fs_schedule", "delete"),
		{ToolName: "fs_issue", Action: "update", ExecutionMode: string(model.BotExecutionAsync)},
	} {
		if botCallIsReadOnly(call) {
			t.Fatalf("%s/%s 不应判为读类", call.ToolName, call.Action)
		}
	}
	// not_executed 回复必须与参数校验失败区分开。
	reply := botNotExecutedReplyText("new_input_before_execution")
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(reply), &payload); err != nil {
		t.Fatalf("not_executed 回复应为 JSON: %v", err)
	}
	if payload["error_code"] != "not_executed" || payload["reason"] != "new_input_before_execution" {
		t.Fatalf("not_executed 回复缺原因: %s", reply)
	}
	if strings.Contains(reply, "40001") {
		t.Fatalf("not_executed 不得复用参数校验错误码: %s", reply)
	}
}

func TestBotInt110ThreadListFiltersPagingCoverage(t *testing.T) {
	if args := (&botThreadArgs{}); args.PageSize != 0 {
		t.Fatal("args 默认应为零值")
	}
	list := &T3ThreadListResponse{
		Items: []T3ThreadSummary{
			{Id: "t-old", Title: "旧", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-01T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1"},
			{Id: "t-new", Title: "新", Status: api.T3ThreadSummaryStatusRunning, UpdatedAt: "2026-10-10T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1"},
			{Id: "t-arch", Title: "归档", Status: api.T3ThreadSummaryStatusArchived, UpdatedAt: "2026-10-11T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1"},
			{Id: "t-other", Title: "别的项目", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-12T00:00:00Z", EnvironmentId: "e1", ProjectId: "p2"},
		},
		Environments: []api.T3Environment{
			{Id: "e1", Name: "健康", Status: api.T3EnvironmentStatusOk},
			{Id: "e2", Name: "坏环境", Status: api.T3EnvironmentStatusUnreachable, Error: api.Ptr("网络不可达")},
		},
	}

	out, err := botSlimThreadList(list, &botThreadArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out["total_items"] != 3 || out["returned_items"] != 3 {
		t.Fatalf("默认应排除归档：%+v", out)
	}
	if out["include_archived"] != false {
		t.Fatalf("默认 include_archived=false")
	}
	items := out["items"].([]map[string]interface{})
	if items[0]["id"] != "t-other" || items[1]["id"] != "t-new" || items[2]["id"] != "t-old" {
		t.Fatalf("应按 updated_at 降序: %+v", items)
	}
	// 覆盖范围：坏环境如实报告，不宣称全部正常。
	incomplete, _ := out["incomplete_environments"].([]string)
	if len(incomplete) != 1 || incomplete[0] != "坏环境" {
		t.Fatalf("应报告未完整读取的环境: %+v", out)
	}
	if _, ok := out["note"]; !ok {
		t.Fatalf("应带覆盖范围说明")
	}

	// 项目过滤 + 分页。
	page, err := botSlimThreadList(list, &botThreadArgs{ProjectID: "p1", Page: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	pageItems := page["items"].([]map[string]interface{})
	if page["total_items"] != 2 || len(pageItems) != 1 || pageItems[0]["id"] != "t-old" {
		t.Fatalf("项目过滤分页不符: %+v", page)
	}
	if page["has_more"] != false {
		t.Fatalf("第二页后应无更多")
	}

	// 归档可选纳入 + settled 过滤。
	withArchived, err := botSlimThreadList(list, &botThreadArgs{IncludeArchived: api.Ptr(true), Settled: api.Ptr(false)})
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if withArchived["total_items"] != 4 {
		t.Fatalf("include_archived=true 应含归档: %+v", withArchived)
	}

	// updated_after 过滤。
	recent, err := botSlimThreadList(list, &botThreadArgs{UpdatedAfter: "2026-10-09T00:00:00Z"})
	if err != nil {
		t.Fatalf("list updated_after: %v", err)
	}
	if recent["total_items"] != 2 {
		t.Fatalf("updated_after 应只剩两条: %+v", recent)
	}

	// 非法时间与超大 page_size 都要明确报错。
	if _, err := botSlimThreadList(list, &botThreadArgs{UpdatedAfter: "not-a-time"}); err == nil {
		t.Fatalf("非法 updated_after 应报错")
	}
	if _, err := botSlimThreadList(list, &botThreadArgs{PageSize: 1000}); err == nil {
		t.Fatalf("page_size 超上限应报错")
	}
}

func TestBotInt110ThreadDetailItemsWindow(t *testing.T) {
	items := make([]T3ThreadItem, 0, 6)
	for i := 0; i < 6; i++ {
		status := "completed"
		detail := true
		items = append(items, T3ThreadItem{
			Id:              fmt.Sprintf("item-%d", i),
			Type:            api.AssistantMessage,
			Status:          &status,
			UpdatedAt:       api.Ptr(fmt.Sprintf("2026-10-0%dT00:00:00Z", i+1)),
			Text:            strings.Repeat("正文", 400),
			Summary:         fmt.Sprintf("摘要%d", i),
			DetailAvailable: &detail,
		})
	}
	detail := &T3ThreadDetail{
		Thread: T3ThreadSummary{Id: "thr", Title: "线程", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-06T00:00:00Z"},
		Items:  items,
		Access: api.Operate,
	}
	out, err := botSlimThreadDetail(detail, &botThreadArgs{ItemLimit: 2})
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if out["total_items"] != 6 || out["item_limit"] != 2 {
		t.Fatalf("详情条目元信息不符: %+v", out)
	}
	got := out["items"].([]map[string]interface{})
	if len(got) != 2 || got[0]["id"] != "item-4" || got[1]["id"] != "item-5" {
		t.Fatalf("默认应给最新两条: %+v", got)
	}
	if out["next_item_offset"] != 2 || out["has_more_items"] != true {
		t.Fatalf("应可继续向前翻: %+v", out)
	}
	// 不再给全量正文，只给短预览。
	preview, _ := got[1]["text_preview"].(string)
	if len([]rune(preview)) > botThreadTextPreview+1 {
		t.Fatalf("text_preview 过长: %d", len([]rune(preview)))
	}
	if _, hasFullText := got[1]["text"]; hasFullText {
		t.Fatalf("详情列表不应带全量正文")
	}

	// 向前翻页拿到更早的条目。
	older, err := botSlimThreadDetail(detail, &botThreadArgs{ItemLimit: 2, ItemOffset: 2})
	if err != nil {
		t.Fatalf("detail older: %v", err)
	}
	olderItems := older["items"].([]map[string]interface{})
	if olderItems[0]["id"] != "item-2" || olderItems[1]["id"] != "item-3" {
		t.Fatalf("向前翻页不符: %+v", olderItems)
	}
}

func TestBotInt110IssueUpdateWriteProtection(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int110issue")
	bot := env.createBot(t, userID)

	project, err := env.bots.projects.Create(userID, &CreateProjectRequest{Name: "INT110 写保护"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	original := "原始正文"
	issue, err := env.bots.issues.CreateInternalIssue(project.Id, userID,
		CreateInternalIssueRequest{Title: "原标题", Body: &original})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	current, err := env.bots.issues.Get(issue.Id, userID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// 过期的 expected_updated_at：拒绝执行并回最新内容。
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: "req-int110",
		ToolName: "fs_issue", Action: "update",
		ArgumentsJSON: fmt.Sprintf(`{"action":"update","issue_id":%q,"title":"偷偷改","expected_updated_at":"2000-01-01T00:00:00Z"}`, issue.Id),
		ExecutionMode: string(model.BotExecutionAsync), Status: model.BotCallStatusRunning,
	}
	result, err := env.bots.runIssueWrite(context.Background(), call)
	if err != nil {
		t.Fatalf("conflict run: %v", err)
	}
	conflict, ok := result.(map[string]interface{})
	if !ok || conflict["conflict"] != true {
		t.Fatalf("过期版本应回 conflict: %+v", result)
	}
	after, err := env.bots.issues.Get(issue.Id, userID)
	if err != nil {
		t.Fatalf("get after conflict: %v", err)
	}
	if after.Title != "原标题" {
		t.Fatalf("冲突时不得写真: %q", after.Title)
	}

	// 正确版本：正常更新。
	call2 := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: "req-int110",
		ToolName: "fs_issue", Action: "update",
		ArgumentsJSON: fmt.Sprintf(`{"action":"update","issue_id":%q,"title":"新标题","expected_updated_at":%q}`, issue.Id, current.UpdatedAt),
		ExecutionMode: string(model.BotExecutionAsync), Status: model.BotCallStatusRunning,
	}
	if _, err := env.bots.runIssueWrite(context.Background(), call2); err != nil {
		t.Fatalf("update: %v", err)
	}
	fresh, err := env.bots.issues.Get(issue.Id, userID)
	if err != nil {
		t.Fatalf("get final: %v", err)
	}
	if fresh.Title != "新标题" {
		t.Fatalf("版本一致时应更新: %q", fresh.Title)
	}

	// 详情投影必须带写入保护基准与决策信息。
	slim := botSlimIssueDetail(fresh)
	if slim["updated_at"] == "" || slim["body"] != original {
		t.Fatalf("详情应含 updated_at 与 body: %+v", slim)
	}
}

func TestBotInt110ThreadPageOverflowNoPanic(t *testing.T) {
	list := &T3ThreadListResponse{
		Items: []T3ThreadSummary{
			{Id: "t1", Title: "一", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-01T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1"},
			{Id: "t2", Title: "二", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-02T00:00:00Z", EnvironmentId: "e1", ProjectId: "p1"},
		},
	}
	// 极大 page 不得让 (page-1)*pageSize 溢出成负下标。
	out, err := botSlimThreadList(list, &botThreadArgs{Page: math.MaxInt, PageSize: 100})
	if err != nil {
		t.Fatalf("huge page: %v", err)
	}
	if items := out["items"].([]map[string]interface{}); len(items) != 0 {
		t.Fatalf("越界页应返回空: %+v", items)
	}
	if out["total_items"] != 2 {
		t.Fatalf("总数应保留: %+v", out)
	}

	detail := &T3ThreadDetail{
		Thread: T3ThreadSummary{Id: "thr", Status: api.T3ThreadSummaryStatusIdle, UpdatedAt: "2026-10-02T00:00:00Z"},
		Items:  []T3ThreadItem{{Id: "i1", Type: api.UserMessage, Text: "hi"}},
	}
	dOut, err := botSlimThreadDetail(detail, &botThreadArgs{ItemOffset: math.MaxInt, ItemLimit: 5})
	if err != nil {
		t.Fatalf("huge item_offset: %v", err)
	}
	if items := dOut["items"].([]map[string]interface{}); len(items) != 0 {
		t.Fatalf("越界条目偏移应返回空: %+v", items)
	}
}

func TestBotInt110RequestStopRejectsFinishedRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int110stop")
	bot := env.createBot(t, userID)

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "int110-stop-finished",
		TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := env.bots.RequestStop(userID, bot.ID, run.ID); !errors.Is(err, errs.ErrBotRunStateConflict) {
		t.Fatalf("已结束 run 的停止应 409，实得 %v", err)
	}
	var n int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindUser).Count(&n)
	if n != 0 {
		t.Fatalf("被拒的停止不得落消息，实得 %d 条", n)
	}
}

func TestBotInt110CloseUnstartedAsyncWrite(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int110async")
	bot := env.createBot(t, userID)
	project, err := env.bots.projects.Create(userID, &CreateProjectRequest{Name: "INT110 异步暂停"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	original := "原始正文"
	issue, err := env.bots.issues.CreateInternalIssue(project.Id, userID,
		CreateInternalIssueRequest{Title: "原标题", Body: &original})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).Updates(map[string]interface{}{
		"tool_name":      "fs_issue",
		"action":         "update",
		"arguments_json": fmt.Sprintf(`{"action":"update","issue_id":%q,"title":"不应落地"}`, issue.Id),
	})
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	env.bots.closeUnstartedAsyncCall(call, "new_input_before_execution")

	fresh, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if fresh.Status != model.BotCallStatusNotExecuted || fresh.ErrorCode == nil || *fresh.ErrorCode != "not_executed" {
		t.Fatalf("排队中的异步写应置 not_executed: %+v", fresh)
	}
	after, err := env.bots.issues.Get(issue.Id, userID)
	if err != nil {
		t.Fatalf("issue after: %v", err)
	}
	if after.Title != "原标题" {
		t.Fatalf("未开始的异步写不得落地: %q", after.Title)
	}
	var results int64
	env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND kind = ? AND call_id = ?", bot.ID, model.BotMessageKindBackgroundResult, callID).
		Count(&results)
	if results != 1 {
		t.Fatalf("应补一条后台结果消息，实得 %d", results)
	}

	// 已被 worker 认领（running）的写不在这里逆转：副作用可能已发生。
	_, runningID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionAsync))
	runningCall, err := env.bots.calls.FindByID(runningID)
	if err != nil {
		t.Fatalf("running call: %v", err)
	}
	env.bots.closeUnstartedAsyncCall(runningCall, "new_input_before_execution")
	freshRunning, err := env.bots.calls.FindByID(runningID)
	if err != nil {
		t.Fatalf("fresh running: %v", err)
	}
	if freshRunning.Status != model.BotCallStatusRunning {
		t.Fatalf("已认领的写不应被逆转: %+v", freshRunning)
	}
}

func TestBotInt110ResultReadAdvancesWithEscaping(t *testing.T) {
	// 切片里全是反斜杠：嵌进 JSON 后每个字符转义成两个，若收缩后 keep 落到 0
	// 会返回空 content 且 next_offset 不变，续读原地死循环。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int110esc")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusSucceeded, string(model.BotExecutionSync))
	source := strings.Repeat("\\", botResultBudgetChars+2000)
	env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).Updates(map[string]interface{}{"result_json": source})

	reader := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: "req-esc",
		ProviderToolCallID: "prov-esc", ToolName: "fs_result", Action: "read",
		ArgumentsJSON: fmt.Sprintf(`{"action":"read","call_id":%q,"offset":0,"limit":16000}`, callID),
		ArgumentsHash: "x", ExecutionMode: "sync", Status: model.BotCallStatusQueued,
		ExecutionContextJSON: "{}", CreatedAt: time.Now().UTC(),
	}
	content, err := env.bots.botExecuteSyncTool(context.Background(), reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var page struct {
		Content    string `json:"content"`
		NextOffset int64  `json:"next_offset"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(content), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Content == "" || page.NextOffset <= 0 {
		t.Fatalf("转义膨胀时也必须前进: %+v", page)
	}
	if !page.HasMore {
		t.Fatalf("源结果远大于一页，应 has_more")
	}
	if out := botBuildOutcomeRaw(reader.ID, content, nil); out.Truncated {
		t.Fatalf("读取体不得被二次截断")
	}
}

func TestBotInt110RequestStopRejectsScheduleRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int110schedstop")
	bot := env.createBot(t, userID)

	now := time.Now().UTC()
	taskID := uuid.NewString()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerSchedule, TriggerKey: "int110-schedule-stop",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		SessionKind: model.BotSessionKindSchedule, ScheduleTaskID: &taskID,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}
	if _, err := env.bots.RequestStop(userID, bot.ID, run.ID); err == nil {
		t.Fatalf("schedule run 的停止应被拒绝")
	}
	var n int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindUser).Count(&n)
	if n != 0 {
		t.Fatalf("被拒的 schedule 停止不得落主线消息，实得 %d 条", n)
	}
}
