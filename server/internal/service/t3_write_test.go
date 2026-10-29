package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// INT-85 写路径用例：REST 语义 → T3 一次性 WS RPC 的适配行为、
// 只读守卫、状态冲突映射、密钥安全面与图片字节转发。

// pairWritable 配对一个宽 scope（标准客户端授权）环境。
func pairWritable(t *testing.T, e *t3TestEnv, userID, name string) string {
	t.Helper()
	created, err := e.env.Create(context.Background(), userID, &CreateT3EnvironmentRequest{Name: name, PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create env: %v", err)
	}
	if created.Access != T3EnvironmentAccessOperate {
		t.Fatalf("standard grant must report access=operate, got %v", created.Access)
	}
	if len(created.Scopes) == 0 {
		t.Fatalf("expected scopes in response")
	}
	return created.Id
}

// TestT3ReadOnlyEnvironmentWriteGuard 旧只读环境：读路径照常，写入口统一
// 40300（重新配对后可操作），不发出任何写 RPC。
func TestT3ReadOnlyEnvironmentWriteGuard(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.grantScope = t3ScopeOrchestrationRead // 旧配对只授予只读
	e.fake.mu.Unlock()
	created, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "只读机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Access != T3EnvironmentAccessReadOnly {
		t.Fatalf("access = %v", created.Access)
	}
	e.fake.mu.Lock()
	e.fake.grantScope = t3ScopeOrchestrationRead + " " + t3ScopeOrchestrationOperate
	e.fake.mu.Unlock()

	if _, err := e.th.SendMessage(ctx, userID, created.Id, "th-1", &T3SendMessageRequest{Text: "你好"}); err == nil || t3ErrCode(err) != errs.ErrPermissionDenied.Code {
		t.Fatalf("expected 40300 for read-only env, got %v", err)
	}
	if _, err := e.th.CreateAttachmentUpload(ctx, userID, created.Id, "x.txt", "text/plain", 3, strings.NewReader("abc")); err == nil || t3ErrCode(err) != errs.ErrPermissionDenied.Code {
		t.Fatalf("expected 40300 for read-only upload before mime check, got %v", err)
	}
	if err := e.th.DeleteAttachment(ctx, userID, created.Id, "att-1"); err == nil || t3ErrCode(err) != errs.ErrPermissionDenied.Code {
		t.Fatalf("expected 40300 for attachment delete, got %v", err)
	}
	if _, err := e.th.CreateThread(ctx, userID, created.Id, &T3CreateThreadRequest{ProjectId: "p-1", Message: api.CreateT3ThreadRequest_Message{Text: "hi"}}); err == nil || t3ErrCode(err) != errs.ErrPermissionDenied.Code {
		t.Fatalf("expected 40300 for thread create, got %v", err)
	}
	// 只读会话不发任何写 RPC
	for _, rpc := range e.fake.recordedRPC() {
		switch rpc.method {
		case "orchestration.dispatchCommand", "orchestration.launchThread", "attachments.delete", "attachments.createUploadUrl":
			t.Fatalf("read-only env must not dispatch %s", rpc.method)
		}
	}

	// 重新配对升级为 operate
	repaired, err := e.env.Update(ctx, userID, created.Id, &UpdateT3EnvironmentRequest{PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL))})
	if err != nil {
		t.Fatalf("re-pair: %v", err)
	}
	if repaired.Access != T3EnvironmentAccessOperate {
		t.Fatalf("after re-pair access = %v", repaired.Access)
	}
}

// TestT3SendMessageDispatch 覆盖 message.dispatch 适配：queue_after_active、
// attachments 缺省发 []、禁止 deliveryIntent、受理 sequence 透出。
func TestT3SendMessageDispatch(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "发信机")
	ctx := context.Background()

	result, err := e.th.SendMessage(ctx, userID, envID, "th-1", &T3SendMessageRequest{Text: "帮我看看"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Sequence != 1 {
		t.Fatalf("sequence = %d", result.Sequence)
	}

	var dispatch map[string]any
	found := false
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.dispatchCommand" {
			found = true
			if err := json.Unmarshal(rpc.payload, &dispatch); err != nil {
				t.Fatalf("payload: %v", err)
			}
		}
	}
	if !found {
		t.Fatalf("expected dispatchCommand RPC")
	}
	if dispatch["type"] != "message.dispatch" || dispatch["threadId"] != "th-1" {
		t.Fatalf("dispatch = %+v", dispatch)
	}
	if dispatch["deliveryIntent"] != nil {
		t.Fatalf("deliveryIntent must never be sent, got %v", dispatch["deliveryIntent"])
	}
	mode, _ := dispatch["dispatchMode"].(map[string]any)
	if mode == nil || mode["type"] != "queue_after_active" {
		t.Fatalf("dispatchMode = %+v", dispatch["dispatchMode"])
	}
	attachments, _ := dispatch["attachments"].([]any)
	if dispatch["attachments"] == nil || len(attachments) != 0 {
		t.Fatalf("attachments must default to [], got %+v", dispatch["attachments"])
	}
	if _, ok := dispatch["commandId"].(string); !ok {
		t.Fatalf("missing commandId: %+v", dispatch)
	}
	if _, ok := dispatch["messageId"].(string); !ok {
		t.Fatalf("missing messageId: %+v", dispatch)
	}

	// 带图片附件：原样转 camelCase
	result, err = e.th.SendMessage(ctx, userID, envID, "th-1", &T3SendMessageRequest{
		Text: "带图",
		Attachments: []T3ChatAttachment{{
			Type: "image", Id: "att-1", Name: "shot.png", MimeType: "image/png", SizeBytes: 42,
		}},
	})
	if err != nil {
		t.Fatalf("send with attachment: %v", err)
	}
	_ = result
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method != "orchestration.dispatchCommand" {
			continue
		}
		var payload struct {
			Attachments []map[string]any `json:"attachments"`
			Text        string           `json:"text"`
		}
		if json.Unmarshal(rpc.payload, &payload) == nil && payload.Text == "带图" {
			if len(payload.Attachments) != 1 {
				t.Fatalf("attachments = %+v", payload.Attachments)
			}
			attachment := payload.Attachments[0]
			if attachment["type"] != "image" || attachment["id"] != "att-1" || attachment["mimeType"] != "image/png" {
				t.Fatalf("attachment = %+v", attachment)
			}
			return
		}
	}
	t.Fatalf("second dispatch not recorded")
}

func TestT3SendMessageImageOnlyAndStableIDs(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "图片机")
	ctx := context.Background()

	if _, err := e.th.SendMessage(ctx, userID, envID, "th-1", &T3SendMessageRequest{Text: "   "}); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for empty text without attachments, got %v", err)
	}

	commandID := "cmd-stable"
	messageID := "msg-stable"
	if _, err := e.th.SendMessage(ctx, userID, envID, "th-1", &T3SendMessageRequest{
		CommandId: &commandID,
		MessageId: &messageID,
		Text:      "",
		Attachments: []T3ChatAttachment{{
			Type: "image", Id: "att-img", Name: "a.png", MimeType: "image/png", SizeBytes: 8,
		}},
	}); err != nil {
		t.Fatalf("image-only send: %v", err)
	}
	found := false
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method != "orchestration.dispatchCommand" {
			continue
		}
		var payload struct {
			CommandID   string           `json:"commandId"`
			MessageID   string           `json:"messageId"`
			Text        string           `json:"text"`
			Attachments []map[string]any `json:"attachments"`
		}
		if json.Unmarshal(rpc.payload, &payload) != nil {
			continue
		}
		if payload.CommandID == commandID && payload.MessageID == messageID {
			found = true
			if payload.Text != "" || len(payload.Attachments) != 1 {
				t.Fatalf("image-only payload = %+v", payload)
			}
		}
	}
	if !found {
		t.Fatalf("stable IDs not forwarded")
	}
}

// t3ThreadShellWithRuns 造一个投影 + 活跃 shell 的组合 fixture。
func t3ThreadShellWithRuns(e *fakeT3Environment, threadID string, runs []map[string]any, extraRows []map[string]any) {
	rows := append([]map[string]any{
		{"position": 0, "visibility": "local", "sourceThreadId": threadID, "sourceItemId": "i0",
			"item": map[string]any{"id": "i0", "type": "user_message", "status": "completed", "text": "你好"}},
	}, extraRows...)
	e.mu.Lock()
	e.projections[threadID] = wireProjection(appThreadFixture(threadID, "线程", nil), rows)
	e.mu.Unlock()
	_ = runs
}

// TestT3CreateThreadLaunch 覆盖 launchThread 适配：稳定 threadId、种子标题、
// generateTitle、runtime_mode 缺省、workspace 缺省 root。项目表来自
// `/api/projects`：启动路径零 shell 订阅（INT-116）。
func TestT3CreateThreadLaunch(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "启动机")
	// Create 已预热常驻订阅（INT-132）：等它稳定，启动路径的断言窗口从
	// 这里起算，预热流量不进窗口。
	before := awaitPrewarmSubscriptions(t, e.fake)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "P", "workspaceRoot": "/repo", "defaultModelSelection": map[string]any{"instanceId": "prov", "model": "m1"}},
	}})
	e.fake.mu.Unlock()

	result, err := e.th.CreateThread(ctx, userID, envID, &T3CreateThreadRequest{
		ProjectId: "p-1",
		Message:   api.CreateT3ThreadRequest_Message{Text: "帮我实现 INT-85\n第二行"},
	})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if result.ThreadId != "th-launched" || result.Resumed {
		t.Fatalf("result = %+v", result)
	}

	var launch map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.launchThread" {
			if err := json.Unmarshal(rpc.payload, &launch); err != nil {
				t.Fatalf("payload: %v", err)
			}
		}
	}
	if launch == nil {
		t.Fatalf("expected launchThread RPC")
	}
	if _, ok := launch["threadId"].(string); !ok {
		t.Fatalf("launch must carry a stable threadId: %+v", launch)
	}
	if launch["generateTitle"] != true {
		t.Fatalf("seeded title must enable title generation: %+v", launch)
	}
	if launch["title"] != "帮我实现 INT-85 第二行" {
		t.Fatalf("title seed = %v", launch["title"])
	}
	if launch["runtimeMode"] != "full-access" || launch["interactionMode"] != "default" {
		t.Fatalf("modes = %+v", launch)
	}
	workspace, _ := launch["workspaceStrategy"].(map[string]any)
	if workspace == nil || workspace["type"] != "root" {
		t.Fatalf("default workspace = %+v", launch["workspaceStrategy"])
	}

	// Current checkout 丢弃 branch；稳定 ID 原样下发
	threadID := "th-stable"
	commandID := "cmd-launch"
	branch := "should-not-switch"
	if _, err := e.th.CreateThread(ctx, userID, envID, &T3CreateThreadRequest{
		CommandId: &commandID,
		ThreadId:  &threadID,
		ProjectId: "p-1",
		Message:   api.CreateT3ThreadRequest_Message{Text: "root-no-branch"},
		Workspace: &api.T3ThreadWorkspace{Type: "root", Branch: &branch},
	}); err != nil {
		t.Fatalf("create root thread: %v", err)
	}
	var rootLaunch map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.launchThread" {
			var payload map[string]any
			if json.Unmarshal(rpc.payload, &payload) == nil {
				if payload["threadId"] == threadID {
					rootLaunch = payload
				}
			}
		}
	}
	if rootLaunch == nil {
		t.Fatalf("stable threadId launch not found")
	}
	if rootLaunch["commandId"] != commandID {
		t.Fatalf("commandId = %v", rootLaunch["commandId"])
	}
	rootWorkspace, _ := rootLaunch["workspaceStrategy"].(map[string]any)
	if rootWorkspace["type"] != "root" || rootWorkspace["branch"] != nil {
		t.Fatalf("root must not switch branch, got %+v", rootWorkspace)
	}
	model, _ := launch["modelSelection"].(map[string]any)
	if model == nil || model["instanceId"] != "prov" || model["model"] != "m1" {
		t.Fatalf("model selection must fall back to project default, got %+v", launch["modelSelection"])
	}

	// worktree 缺省 base_ref = 主检出当前分支；startFromOrigin 来自 T3 设置
	if _, err := e.th.CreateThread(ctx, userID, envID, &T3CreateThreadRequest{
		ProjectId: "p-1",
		Message:   api.CreateT3ThreadRequest_Message{Text: "worktree"},
		Workspace: &api.T3ThreadWorkspace{Type: "worktree"},
	}); err != nil {
		t.Fatalf("create worktree thread: %v", err)
	}
	var worktreeLaunch map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.launchThread" {
			_ = json.Unmarshal(rpc.payload, &worktreeLaunch)
		}
	}
	workspace, _ = worktreeLaunch["workspaceStrategy"].(map[string]any)
	if workspace["type"] != "worktree" || workspace["baseRef"] != "main" {
		t.Fatalf("worktree workspace = %+v", workspace)
	}
	if workspace["startFromOrigin"] != true {
		t.Fatalf("startFromOrigin must come from t3 settings, got %+v", workspace)
	}

	// 现有工作树需要 worktree_path
	if _, err := e.th.CreateThread(ctx, userID, envID, &T3CreateThreadRequest{
		ProjectId: "p-1",
		Message:   api.CreateT3ThreadRequest_Message{Text: "existing"},
		Workspace: &api.T3ThreadWorkspace{Type: "existing_worktree"},
	}); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for missing worktree_path, got %v", err)
	}

	// 不在环境里的项目 → 40401
	if _, err := e.th.CreateThread(ctx, userID, envID, &T3CreateThreadRequest{
		ProjectId: "p-other",
		Message:   api.CreateT3ThreadRequest_Message{Text: "hi"},
	}); err == nil || t3ErrCode(err) != 40401 {
		t.Fatalf("expected 40401 for foreign project, got %v", err)
	}

	// 启动路径零 shell 调用：config/settings/vcs/launch RPC 照常，订阅 absent
	// （断言窗口从预热稳定后起算，Create 预热的流量不进窗口）。
	for _, rpc := range e.fake.recordedRPC()[before:] {
		switch rpc.method {
		case t3RPCSubscribeShell, "orchestration.subscribeArchivedShell":
			t.Fatalf("create path must not subscribe shell, got %s", rpc.method)
		}
	}
	if len(e.fake.recordedRPC()) <= before {
		t.Fatalf("expected launch-side RPCs to be recorded")
	}
}

// TestT3StopThreadSelection 覆盖 Stop 回退路径（fake 切到旧上游：无
// threadStop 能力声明）的 run 选择口径：活动 run 优先（holdQueue=true）；
// 根 run 结束但后台工作仍在 → 最近 run；无可停 → stopped=false 无写命令。
func TestT3StopThreadSelection(t *testing.T) {
	e := setupT3ServiceTest(t)
	// 能力缺失才走回退：本地读可信 shell 选 run 后发 run.interrupt。
	e.fake.useLegacyThreadStop()
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "停止机")
	ctx := context.Background()

	// 线程在跑：投影 runs 有活动 run
	e.fake.mu.Lock()
	e.fake.projections["th-run"] = mustJSONString(map[string]any{
		"thread":           appThreadFixture("th-run", "运行中", nil),
		"runs":             []any{map[string]any{"id": "run-1", "ordinal": 1, "status": "running", "userMessageId": "m1"}},
		"messages":         []any{map[string]any{"id": "m1", "text": "你好", "attachments": []any{}}},
		"providerSessions": []any{},
		"providerThreads":  []any{},
		"providerTurns":    []any{},
		"runtimeRequests":  []any{},
		"turnItems":        []any{},
		"visibleTurnItems": []any{},
	})
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-run", "运行中", "running", map[string]any{"latestRunId": "run-1", "activeRunId": "run-1"})},
	)
	e.fake.projections["th-bg"] = mustJSONString(map[string]any{
		"thread":           appThreadFixture("th-bg", "后台工作", nil),
		"runs":             []any{map[string]any{"id": "run-9", "ordinal": 9, "status": "completed", "userMessageId": "m9"}},
		"messages":         []any{map[string]any{"id": "m9", "text": "早前消息", "attachments": []any{}}},
		"providerSessions": []any{},
		"providerThreads":  []any{},
		"providerTurns":    []any{},
		"runtimeRequests":  []any{},
		"turnItems":        []any{},
		"visibleTurnItems": []any{},
	})
	e.fake.mu.Unlock()

	result, err := e.th.StopThread(ctx, userID, envID, "th-run")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !result.Stopped || apiStr(result.RunId) != "run-1" {
		t.Fatalf("stop result = %+v", result)
	}
	var interrupt map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.dispatchCommand" {
			_ = json.Unmarshal(rpc.payload, &interrupt)
		}
	}
	if interrupt["type"] != "run.interrupt" || interrupt["runId"] != "run-1" || interrupt["holdQueue"] != true {
		t.Fatalf("interrupt = %+v", interrupt)
	}

	// 后台工作占位的最近 run（shell.latestRunId + pendingBackgroundTasks）：
	// shell 状态变化由订阅流推送增量驱动（INT-116）。
	e.fake.pushThreadDelta(t, shellThread("th-bg", "后台工作", "completed", map[string]any{
		"latestRunId":            "run-9",
		"activeRunId":            nil,
		"pendingBackgroundTasks": []any{map[string]any{"taskId": "bg-1", "kind": "subagent"}},
	}))
	result, err = e.th.StopThread(ctx, userID, envID, "th-bg")
	if err != nil {
		t.Fatalf("stop bg: %v", err)
	}
	if !result.Stopped || apiStr(result.RunId) != "run-9" {
		t.Fatalf("background stop = %+v", result)
	}

	// 静止线程：无可停 run → stopped=false（T3 口径的无操作）
	e.fake.pushThreadDelta(t, shellThread("th-bg", "后台工作", "completed", nil))
	result, err = e.th.StopThread(ctx, userID, envID, "th-bg")
	if err != nil {
		t.Fatalf("stop idle: %v", err)
	}
	if result.Stopped {
		t.Fatalf("idle thread must be a no-op, got %+v", result)
	}

	// 只有 PR watch 的 monitor 不算可中断后台：stopped=false，不发 interrupt。
	e.fake.pushThreadDelta(t, shellThread("th-bg", "看 PR", "completed", map[string]any{
		"latestRunId": "run-9",
		"pendingBackgroundTasks": []any{
			map[string]any{"taskId": "pull-request-watch:pr-1", "kind": "monitor"},
		},
	}))
	before := len(e.fake.recordedRPC())
	result, err = e.th.StopThread(ctx, userID, envID, "th-bg")
	if err != nil {
		t.Fatalf("stop pr-watch: %v", err)
	}
	if result.Stopped {
		t.Fatalf("PR watch only must be a no-op, got %+v", result)
	}
	for _, rpc := range e.fake.recordedRPC()[before:] {
		if rpc.method == "orchestration.dispatchCommand" {
			t.Fatalf("PR watch stop must not dispatch, got %s %s", rpc.method, rpc.payload)
		}
	}

	// R2-3：run 选择以新鲜 shell 的 activeRunId 为主。投影里没有活动 run
	// （它早于本次 shell），但可信 shell 说 run-2 在跑 → 必须中断 run-2，
	// 不能因为旧投影而误报「无事可做」。
	e.fake.mu.Lock()
	e.fake.projections["th-late"] = wireProjection(appThreadFixture("th-late", "后来启动", nil), []map[string]any{
		{"id": "run-1", "ordinal": 1, "status": "completed", "userMessageId": "m1"},
	})
	e.fake.mu.Unlock()
	e.fake.pushThreadDelta(t, shellThread("th-late", "后来启动", "running", map[string]any{
		"latestRunId": "run-2",
		"activeRunId": "run-2",
	}))
	result, err = e.th.StopThread(ctx, userID, envID, "th-late")
	if err != nil {
		t.Fatalf("stop late run: %v", err)
	}
	if !result.Stopped || apiStr(result.RunId) != "run-2" {
		t.Fatalf("stop must target the fresh shell activeRunId, got %+v", result)
	}
	var lateInterrupt map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method != "orchestration.dispatchCommand" {
			continue
		}
		var cmd map[string]any
		if json.Unmarshal(rpc.payload, &cmd) == nil && cmd["runId"] == "run-2" {
			lateInterrupt = cmd
		}
	}
	if lateInterrupt["type"] != "run.interrupt" {
		t.Fatalf("expected run.interrupt for run-2, got %+v", lateInterrupt)
	}

	// F6：Stop 的错误语义恢复基线顺序——先读投影、再取可信 shell。
	// 线程真的不存在（投影 404）→ 40418，不再被 shell 守卫拦成 50202。
	if _, err := e.th.StopThread(ctx, userID, envID, "th-absent"); err == nil || t3ErrCode(err) != errs.ErrT3ThreadNotFound.Code {
		t.Fatalf("stop missing thread = %v, want 40418", err)
	}

	// 投影在、两面可信但 shell 无此线程 → 50202「上游快照面不一致」。
	e.fake.mu.Lock()
	e.fake.projections["th-ghost"] = wireProjection(appThreadFixture("th-ghost", "幽灵", nil), nil)
	e.fake.mu.Unlock()
	_, err = e.th.StopThread(ctx, userID, envID, "th-ghost")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("stop shell-missing thread = %v, want 50202", err)
	}
	var stopErr *errs.AppError
	if !errors.As(err, &stopErr) || !strings.Contains(stopErr.Message, "运行状态不可读") {
		t.Fatalf("expected shell-missing message, got %v", err)
	}
}

// TestT3StopThreadStopCommandPath 覆盖能力存在的主路径（INT-173）：
// capabilities.threadStop=true 时派发公开命令 thread.stop（不带
// runId/holdQueue，服务端选目标），受理即 stopped=true、run_id 留缺省；
// 本地不再读可信 shell——无 shell 记录的线程也直接交服务端判定，不再有
// 50202 守卫；线程不存在（投影 404）→ 40418 在新路径同样保留。
func TestT3StopThreadStopCommandPath(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "新停机")
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projections["th-run"] = mustJSONString(map[string]any{
		"thread":           appThreadFixture("th-run", "运行中", nil),
		"runs":             []any{map[string]any{"id": "run-1", "ordinal": 1, "status": "running", "userMessageId": "m1"}},
		"messages":         []any{map[string]any{"id": "m1", "text": "你好", "attachments": []any{}}},
		"providerSessions": []any{},
		"providerThreads":  []any{},
		"providerTurns":    []any{},
		"runtimeRequests":  []any{},
		"turnItems":        []any{},
		"visibleTurnItems": []any{},
	})
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-run", "运行中", "running", map[string]any{"latestRunId": "run-1", "activeRunId": "run-1"})},
	)
	e.fake.mu.Unlock()

	result, err := e.th.StopThread(ctx, userID, envID, "th-run")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !result.Stopped || result.RunId != nil {
		t.Fatalf("threadStop path: acceptance means stopped=true with no self-picked run, got %+v", result)
	}
	var stop map[string]any
	sawInterrupt := false
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method != "orchestration.dispatchCommand" {
			continue
		}
		var cmd map[string]any
		if json.Unmarshal(rpc.payload, &cmd) != nil {
			continue
		}
		if cmd["type"] == "thread.stop" {
			stop = cmd
		}
		if cmd["type"] == "run.interrupt" {
			sawInterrupt = true
		}
	}
	if stop == nil {
		t.Fatalf("expected thread.stop dispatch")
	}
	if sawInterrupt {
		t.Fatalf("capability path must not dispatch run.interrupt")
	}
	if stop["threadId"] != "th-run" {
		t.Fatalf("thread.stop threadId = %+v", stop)
	}
	if id, _ := stop["commandId"].(string); id == "" {
		t.Fatalf("thread.stop missing commandId: %+v", stop)
	}
	if _, ok := stop["runId"]; ok {
		t.Fatalf("thread.stop must not carry runId (server picks target): %+v", stop)
	}
	if _, ok := stop["holdQueue"]; ok {
		t.Fatalf("thread.stop must not carry holdQueue (server holds queue): %+v", stop)
	}

	// 无可停目标：不必本地先判「无事可做」，直接交服务端空转回执——
	// 投影在、shell 无此线程也不再有 50202 守卫（本地不读运行态）。
	e.fake.mu.Lock()
	e.fake.projections["th-idle"] = mustJSONString(map[string]any{
		"thread":           appThreadFixture("th-idle", "空闲", nil),
		"runs":             []any{},
		"messages":         []any{},
		"providerSessions": []any{},
		"providerThreads":  []any{},
		"providerTurns":    []any{},
		"runtimeRequests":  []any{},
		"turnItems":        []any{},
		"visibleTurnItems": []any{},
	})
	e.fake.mu.Unlock()
	result, err = e.th.StopThread(ctx, userID, envID, "th-idle")
	if err != nil {
		t.Fatalf("stop idle on threadStop path: %v", err)
	}
	if !result.Stopped || result.RunId != nil {
		t.Fatalf("idle thread on threadStop path = %+v", result)
	}

	// 线程真的不存在（投影 404）→ 40418，能力路径同样保留基线语义。
	if _, err := e.th.StopThread(ctx, userID, envID, "th-absent"); err == nil || t3ErrCode(err) != errs.ErrT3ThreadNotFound.Code {
		t.Fatalf("stop missing thread = %v, want 40418", err)
	}
}

// t3ConfigSupportsThreadStop 纯函数口径：显式声明 true 才算支持；false、
// 字段缺失、capabilities/environment 缺失、响应坏 JSON 都算不支持（回退）。
func TestT3ConfigSupportsThreadStop(t *testing.T) {
	caps := func(v map[string]any) json.RawMessage {
		return mustJSON(map[string]any{"environment": map[string]any{"capabilities": v}})
	}
	if !t3ConfigSupportsThreadStop(caps(map[string]any{"threadStop": true})) {
		t.Fatalf("declared true must count as supported")
	}
	for name, raw := range map[string]json.RawMessage{
		"declared false":  caps(map[string]any{"threadStop": false}),
		"field absent":    caps(map[string]any{}),
		"no capabilities": mustJSON(map[string]any{"environment": map[string]any{}}),
		"no environment":  mustJSON(map[string]any{}),
		"broken json":     json.RawMessage(`{`),
	} {
		if t3ConfigSupportsThreadStop(raw) {
			t.Fatalf("%s must not count as supported", name)
		}
	}
}

// Stop 回退路径的 run 选择是纯函数，逐条锁定官方客户端显式指定 run 时的口径：
// shell 的可中断活动 run 优先；没有时看投影（含 waiting）；再否则看后台工作。
func TestSelectStopRunID(t *testing.T) {
	ptr := func(v string) *string { return &v }
	shell := func(activeRunID *string, latestRunID *string, tasks ...t3PendingBackgroundTask) *t3ThreadShell {
		return &t3ThreadShell{ID: "th-1", ActiveRunID: activeRunID, LatestRunID: latestRunID, PendingBackgroundTasks: tasks}
	}
	projection := func(runs ...t3Run) *t3ThreadProjection {
		return &t3ThreadProjection{Runs: runs}
	}

	// shell 的可中断活动 run 优先于投影里的活动 run。
	if got := selectStopRunID(shell(ptr("run-shell"), ptr("run-latest")), projection(t3Run{ID: "run-proj", Status: "running"})); got != "run-shell" {
		t.Fatalf("activeRunId must win, got %q", got)
	}

	// 没有 activeRunId 时看投影，包含 waiting（上游 shell 的可中断集合排除它）。
	if got := selectStopRunID(shell(nil, ptr("run-latest")), projection(t3Run{ID: "run-wait", Status: "waiting"})); got != "run-wait" {
		t.Fatalf("projection waiting run must be interruptible, got %q", got)
	}

	// 投影里只剩已完成的 run，但 shell 有可中断后台 → 中断最近 run。
	if got := selectStopRunID(
		shell(nil, ptr("run-9"), t3PendingBackgroundTask{TaskID: "bg-1", Kind: "subagent"}),
		projection(t3Run{ID: "run-9", Status: "completed"}),
	); got != "run-9" {
		t.Fatalf("background work must fall back to latestRunId, got %q", got)
	}

	// 只有 PR watch 的 monitor 不算可中断后台。
	if got := selectStopRunID(
		shell(nil, ptr("run-9"), t3PendingBackgroundTask{TaskID: "pull-request-watch:pr-1", Kind: "monitor"}),
		projection(t3Run{ID: "run-9", Status: "completed"}),
	); got != "" {
		t.Fatalf("pr watch must not be interruptible, got %q", got)
	}

	// 什么都没有 → 无操作。
	if got := selectStopRunID(shell(nil, ptr("run-9")), projection(t3Run{ID: "run-9", Status: "completed"})); got != "" {
		t.Fatalf("idle thread must be a no-op, got %q", got)
	}
}

// TestT3QueueOperations 覆盖队列四操作与三态附件。
func TestT3QueueOperations(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "队列机")
	ctx := context.Background()

	projectionJSON := mustJSONString(map[string]any{
		"thread": appThreadFixture("th-q", "排队", nil),
		"runs": []any{
			map[string]any{"id": "run-active", "ordinal": 1, "status": "running", "userMessageId": "m-active", "activeAttemptId": "att-1"},
			map[string]any{"id": "run-q1", "ordinal": 2, "status": "queued", "userMessageId": "m-q1", "queuePosition": 1},
			map[string]any{"id": "run-q2", "ordinal": 3, "status": "queued", "userMessageId": "m-q2", "queuePosition": 2},
		},
		"messages": []any{
			map[string]any{"id": "m-active", "text": "进行中", "attachments": []any{}},
			map[string]any{"id": "m-q1", "text": "排队一号", "attachments": []any{}},
			map[string]any{"id": "m-q2", "text": "排队二号", "attachments": []any{}},
		},
		"providerSessions": []any{map[string]any{"id": "s1", "status": "connected", "capabilities": map[string]any{
			"turns": map[string]any{"supportsQueuedMessages": true, "supportsActiveSteering": true},
		}}},
		"providerThreads": []any{},
		"providerTurns":   []any{map[string]any{"runAttemptId": "att-1", "status": "running"}},
		"runtimeRequests": []any{},
		"turnItems":       []any{},
		"visibleTurnItems": []any{
			map[string]any{"position": 0, "visibility": "local", "sourceThreadId": "th-q", "sourceItemId": "i0",
				"item": map[string]any{"id": "i0", "type": "user_message", "status": "completed", "text": "你好"}},
		},
	})
	e.fake.mu.Lock()
	e.fake.projections["th-q"] = projectionJSON
	e.fake.mu.Unlock()

	if _, err := e.th.ResumeQueue(ctx, userID, envID, "th-q"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := e.th.CancelQueuedRun(ctx, userID, envID, "th-q", "run-q2"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := e.th.SteerQueuedRun(ctx, userID, envID, "th-q", "run-q1"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := e.th.ReorderQueuedRun(ctx, userID, envID, "th-q", "run-q1", nil); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	// 三态附件：缺省保留（payload 不带 attachments）；[]=清空；非空替换
	if _, err := e.th.EditQueuedRun(ctx, userID, envID, "th-q", "run-q1", "改文案", nil); err != nil {
		t.Fatalf("edit keep: %v", err)
	}
	empty := []T3ChatAttachment{}
	if _, err := e.th.EditQueuedRun(ctx, userID, envID, "th-q", "run-q1", "改文案", &empty); err != nil {
		t.Fatalf("edit clear: %v", err)
	}
	replacement := []T3ChatAttachment{{Type: "image", Id: "att-9", Name: "x.png", MimeType: "image/png", SizeBytes: 5}}
	if _, err := e.th.EditQueuedRun(ctx, userID, envID, "th-q", "run-q1", "改文案", &replacement); err != nil {
		t.Fatalf("edit replace: %v", err)
	}
	if _, err := e.th.EditQueuedRun(ctx, userID, envID, "th-q", "run-q1", "", &replacement); err != nil {
		t.Fatalf("edit image-only: %v", err)
	}
	if _, err := e.th.EditQueuedRun(ctx, userID, envID, "th-q", "run-q1", "", &empty); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for empty text and empty attachments, got %v", err)
	}

	dispatches := []map[string]any{}
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.dispatchCommand" {
			var payload map[string]any
			if err := json.Unmarshal(rpc.payload, &payload); err == nil {
				dispatches = append(dispatches, payload)
			}
		}
	}
	find := func(commandType string) map[string]any {
		for _, payload := range dispatches {
			if payload["type"] == commandType {
				return payload
			}
		}
		return nil
	}
	if payload := find("queue.resume"); payload == nil {
		t.Fatalf("missing queue.resume in %+v", dispatches)
	}
	cancelPayload := find("queued-run.cancel")
	if cancelPayload == nil || cancelPayload["runId"] != "run-q2" {
		t.Fatalf("cancel = %+v", cancelPayload)
	}
	steerPayload := find("queued-message.promote-to-steer")
	if steerPayload == nil || steerPayload["queuedRunId"] != "run-q1" || steerPayload["targetRunId"] != "run-active" {
		t.Fatalf("steer = %+v", steerPayload)
	}
	reorderPayload := find("queued-run.reorder")
	if reorderPayload == nil || reorderPayload["runId"] != "run-q1" {
		t.Fatalf("reorder = %+v", reorderPayload)
	}
	if value, present := reorderPayload["beforeRunId"]; !present || value != nil {
		t.Fatalf("beforeRunId must be explicit null for tail, got %v (present=%v)", value, present)
	}

	edits := []map[string]any{}
	for _, payload := range dispatches {
		if payload["type"] == "queued-run.edit" {
			edits = append(edits, payload)
		}
	}
	if len(edits) != 4 {
		t.Fatalf("edits = %+v", edits)
	}
	if _, present := edits[0]["attachments"]; present {
		t.Fatalf("absent attachments must stay absent (keep), got %+v", edits[0]["attachments"])
	}
	if cleared, _ := edits[1]["attachments"].([]any); cleared == nil || len(cleared) != 0 {
		t.Fatalf("[] must clear, got %+v", edits[1]["attachments"])
	}
	if replaced, _ := edits[2]["attachments"].([]any); len(replaced) != 1 {
		t.Fatalf("replacement = %+v", edits[2]["attachments"])
	}
	if edits[3]["text"] != "" {
		t.Fatalf("image-only edit must keep empty text, got %+v", edits[3])
	}

	// 无活动 run 时 steer → 40916
	e.fake.mu.Lock()
	e.fake.projections["th-idle"] = mustJSONString(map[string]any{
		"thread": appThreadFixture("th-idle", "静止", nil), "runs": []any{},
		"messages": []any{}, "providerSessions": []any{}, "providerThreads": []any{},
		"providerTurns": []any{}, "runtimeRequests": []any{}, "turnItems": []any{},
		"visibleTurnItems": []any{},
	})
	e.fake.mu.Unlock()
	if _, err := e.th.SteerQueuedRun(ctx, userID, envID, "th-idle", "run-x"); err == nil || t3ErrCode(err) != errs.ErrT3StateConflict.Code {
		t.Fatalf("expected 40916 steering without active run, got %v", err)
	}
}

// TestT3RespondAndDismiss 覆盖审批/提问应答：成员校验（40419）、已决（40916）、
// decision 联合分发。
func TestT3RespondAndDismiss(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "应答机")
	ctx := context.Background()

	decision := "accept"
	e.fake.mu.Lock()
	// Detail 需要 shell 快照里能找到该线程
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-r", "应答", "waiting", nil)},
	)
	e.fake.projections["th-r"] = mustJSONString(map[string]any{
		"thread": appThreadFixture("th-r", "应答", nil), "runs": []any{},
		"messages": []any{}, "providerSessions": []any{}, "providerThreads": []any{},
		"providerTurns": []any{},
		"runtimeRequests": []any{
			map[string]any{"id": "rr-1", "status": "pending", "responseCapability": map[string]any{"type": "live", "providerSessionId": "s1"}},
			map[string]any{"id": "rr-2", "status": "resolved", "responseCapability": map[string]any{"type": "message"}, "decision": "decline", "answers": map[string]any{"q1": "A"}},
		},
		"turnItems": []any{},
		"visibleTurnItems": []any{
			map[string]any{"position": 0, "visibility": "local", "sourceThreadId": "th-r", "sourceItemId": "a1",
				"item": map[string]any{"id": "a1", "type": "approval_request", "status": "waiting", "requestId": "rr-1",
					"requestKind": "command", "prompt": "允许运行 go test？",
					"options": []any{map[string]any{"decision": "accept", "label": "Allow"}}}},
			map[string]any{"position": 1, "visibility": "local", "sourceThreadId": "th-r", "sourceItemId": "a2",
				"item": map[string]any{"id": "a2", "type": "user_input_request", "status": "waiting", "requestId": "rr-2",
					"questions": []any{map[string]any{"id": "q1", "header": "标题", "question": "选哪个？", "options": []any{map[string]any{"label": "A", "description": "选项 A"}}}}}},
		},
	})
	e.fake.mu.Unlock()

	// 详情里审批/提问条目带运行时请求状态
	detail, err := e.th.Detail(ctx, userID, envID, "th-r")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	byType := map[string]T3ThreadItem{}
	for _, item := range detail.Items {
		byType[string(item.Type)] = item
	}
	approval := byType["approval_request"]
	if apiStr(approval.RequestId) != "rr-1" || apiStr(approval.RequestStatus) != "pending" || apiStr(approval.ResponseCapability) != "live" {
		t.Fatalf("approval enrichment = %+v", approval)
	}
	if len(approval.Options) != 1 || approval.Options[0].Decision != "accept" || approval.Options[0].Label != "Allow" {
		t.Fatalf("approval options = %+v", approval.Options)
	}
	userInput := byType["user_input_request"]
	if apiStr(userInput.RequestStatus) != "resolved" || apiStr(userInput.Decision) != "" {
		t.Fatalf("user_input enrichment = %+v", userInput)
	}
	if len(userInput.Questions) != 1 || userInput.Questions[0].Question != "选哪个？" {
		t.Fatalf("questions = %+v", userInput.Questions)
	}
	if userInput.Answers["q1"] != "A" {
		t.Fatalf("user_input answers = %+v", userInput.Answers)
	}

	// 审批应答 → decision 字段
	if _, err := e.th.RespondToRequest(ctx, userID, envID, "th-r", "rr-1", &T3RespondRequest{Type: "approval", Decision: &decision}); err != nil {
		t.Fatalf("respond approval: %v", err)
	}
	// 提问应答 → answers
	if _, err := e.th.RespondToRequest(ctx, userID, envID, "th-r", "rr-2", &T3RespondRequest{
		Type:    "user_input",
		Answers: map[string]any{"q1": "A"},
	}); err == nil || t3ErrCode(err) != errs.ErrT3StateConflict.Code {
		t.Fatalf("expected 40916 for resolved request, got %v", err)
	}
	// 不属于本线程的请求 → 40419
	if _, err := e.th.RespondToRequest(ctx, userID, envID, "th-r", "rr-elsewhere", &T3RespondRequest{Type: "approval", Decision: &decision}); err == nil || t3ErrCode(err) != errs.ErrT3ThreadItemNotFound.Code {
		t.Fatalf("expected 40419 for foreign request, got %v", err)
	}
	// 非法 decision → 40001
	badDecision := "maybe"
	if _, err := e.th.RespondToRequest(ctx, userID, envID, "th-r", "rr-1", &T3RespondRequest{Type: "approval", Decision: &badDecision}); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for bad decision, got %v", err)
	}
	// dismiss
	if _, err := e.th.DismissRequest(ctx, userID, envID, "th-r", "rr-2"); err == nil || t3ErrCode(err) != errs.ErrT3StateConflict.Code {
		t.Fatalf("expected 40916 dismissing resolved request, got %v", err)
	}

	var respond map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.dispatchCommand" {
			_ = json.Unmarshal(rpc.payload, &respond)
		}
	}
	if respond["type"] != "runtime-request.respond" || respond["requestId"] != "rr-1" || respond["decision"] != "accept" {
		t.Fatalf("respond command = %+v", respond)
	}
}

// TestT3SecretAnswerSentinel 用唯一哨兵覆盖密钥安全面：
// ① 值按原样转发给 T3（secrets.answerRequest payload）；② Fast Ship 的响应、
// 日志、数据库中无该值；③ 上游失败只回固定文案，cause 不回显。
func TestT3SecretAnswerSentinel(t *testing.T) {
	sentinel := "sentinel-" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", ""), " ", "")) + "-SECRET"
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "密钥机")
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projections["th-s"] = mustJSONString(map[string]any{
		"thread": appThreadFixture("th-s", "密钥", nil), "runs": []any{},
		"messages": []any{}, "providerSessions": []any{}, "providerThreads": []any{},
		"providerTurns": []any{}, "runtimeRequests": []any{}, "turnItems": []any{},
		"visibleTurnItems": []any{
			map[string]any{"position": 0, "visibility": "local", "sourceThreadId": "th-s", "sourceItemId": "sec-1",
				"item": map[string]any{"id": "sec-1", "type": "secret_request", "status": "waiting",
					"label": "GitHub Token", "reason": "推送需要", "secretStatus": "pending"}},
		},
	})
	e.fake.mu.Unlock()

	// 观察全部日志（zap observer core）
	core, logs := observer.New(zapcore.DebugLevel)
	e.th.logger = zap.New(core)

	// save 路径：哨兵出现在上游 payload，响应为空
	if err := e.th.AnswerSecret(ctx, userID, envID, "th-s", "th-s::sec-1", &T3AnswerSecretRequest{Type: "save", Secret: &sentinel}); err != nil {
		t.Fatalf("answer secret: %v", err)
	}
	answer := e.fake.lastSecretAnswer()
	if answer == nil {
		t.Fatalf("expected secrets.answerRequest RPC")
	}
	answerBody, _ := answer["answer"].(map[string]any)
	if answer["threadId"] != "th-s" || answer["turnItemId"] != "sec-1" {
		t.Fatalf("answer payload = %+v", answer)
	}
	if answerBody["type"] != "save" || answerBody["secret"] != sentinel {
		t.Fatalf("secret forwarding failed: %+v", answerBody)
	}

	// 响应为空、日志与库中无哨兵
	for i, entry := range logs.All() {
		if strings.Contains(entry.Message, sentinel) {
			t.Fatalf("sentinel leaked into log entry %d: %s", i, entry.Message)
		}
		for _, field := range entry.Context {
			if strings.Contains(field.String, sentinel) {
				t.Fatalf("sentinel leaked into log field: %s", field.String)
			}
		}
	}
	var count int64
	e.db.Raw("SELECT count(*) FROM t3_environments").Scan(&count)
	rows, _ := e.db.Raw("SELECT id, name, address FROM t3_environments").Rows()
	defer rows.Close()
	for rows.Next() {
		var id, name, address string
		_ = rows.Scan(&id, &name, &address)
		if strings.Contains(id+name+address, sentinel) {
			t.Fatalf("sentinel leaked into database row")
		}
	}

	// decline 路径
	if err := e.th.AnswerSecret(ctx, userID, envID, "th-s", "th-s::sec-1", &T3AnswerSecretRequest{Type: "decline"}); err != nil {
		t.Fatalf("decline: %v", err)
	}

	// 已回答 → 40916（本地 secretStatus 判定）
	e.fake.mu.Lock()
	e.fake.projections["th-s2"] = mustJSONString(map[string]any{
		"thread": appThreadFixture("th-s2", "密钥二", nil), "runs": []any{},
		"messages": []any{}, "providerSessions": []any{}, "providerThreads": []any{},
		"providerTurns": []any{}, "runtimeRequests": []any{}, "turnItems": []any{},
		"visibleTurnItems": []any{
			map[string]any{"position": 0, "visibility": "local", "sourceThreadId": "th-s2", "sourceItemId": "sec-2",
				"item": map[string]any{"id": "sec-2", "type": "secret_request", "status": "completed",
					"label": "Token", "reason": "", "secretStatus": "saved"}},
		},
	})
	e.fake.mu.Unlock()
	if err := e.th.AnswerSecret(ctx, userID, envID, "th-s2", "th-s2::sec-2", &T3AnswerSecretRequest{Type: "save", Secret: &sentinel}); err == nil || t3ErrCode(err) != errs.ErrT3StateConflict.Code {
		t.Fatalf("expected 40916 for answered secret, got %v", err)
	}

	// 上游失败：固定文案、无上游 message/cause 回显
	e.fake.mu.Lock()
	e.fake.secretFail = "already_answered"
	e.fake.mu.Unlock()
	err := e.th.AnswerSecret(ctx, userID, envID, "th-s", "th-s::sec-1", &T3AnswerSecretRequest{Type: "save", Secret: &sentinel})
	if err == nil || t3ErrCode(err) != errs.ErrT3StateConflict.Code {
		t.Fatalf("expected 40916 from upstream already_answered, got %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || strings.Contains(appErr.Message, "upstream") || strings.Contains(appErr.Message, sentinel) || strings.Contains(appErr.Message, "echoed") {
		t.Fatalf("error must be a fixed message without cause echo, got %q", appErr.Message)
	}

	// 继承行（fork 前缀）不能在本线程应答 → 40419
	e.fake.mu.Lock()
	e.fake.secretFail = ""
	e.fake.projections["th-fork"] = mustJSONString(map[string]any{
		"thread": appThreadFixture("th-fork", "fork", nil), "runs": []any{},
		"messages": []any{}, "providerSessions": []any{}, "providerThreads": []any{},
		"providerTurns": []any{}, "runtimeRequests": []any{}, "turnItems": []any{},
		"visibleTurnItems": []any{
			map[string]any{"position": 0, "visibility": "inherited", "sourceThreadId": "th-s", "sourceItemId": "sec-1",
				"item": map[string]any{"id": "sec-1", "type": "secret_request", "status": "waiting",
					"label": "GitHub Token", "reason": "", "secretStatus": "pending"}},
		},
	})
	e.fake.mu.Unlock()
	if err := e.th.AnswerSecret(ctx, userID, envID, "th-fork", "th-s::sec-1", &T3AnswerSecretRequest{Type: "save", Secret: &sentinel}); err == nil || t3ErrCode(err) != errs.ErrT3ThreadItemNotFound.Code {
		t.Fatalf("expected 40419 for inherited secret request, got %v", err)
	}
}

// TestT3AttachmentUploadAndContent 覆盖图片流转发：上传字节原样到达上游、
// 元数据返回；内容代理；删除。
func TestT3AttachmentUploadAndContent(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "图传机")
	ctx := context.Background()

	image := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	attachment, err := e.th.CreateAttachmentUpload(ctx, userID, envID, "shot.png", "image/png", int64(len(image)), strings.NewReader(string(image)))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if attachment.Id != "att-1" || attachment.MimeType != "image/png" || attachment.SizeBytes != len(image) {
		t.Fatalf("attachment = %+v", attachment)
	}
	if got := e.fake.uploadedAttachment(); string(got) != string(image) {
		t.Fatalf("upstream bytes = %q", got)
	}

	// 拒绝：非图片类型 / 超 10MiB / 缺 Content-Length
	if _, err := e.th.CreateAttachmentUpload(ctx, userID, envID, "x.txt", "text/plain", 3, strings.NewReader("abc")); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for non-image, got %v", err)
	}
	if _, err := e.th.CreateAttachmentUpload(ctx, userID, envID, "big.png", "image/png", t3MaxImageBytes+1, strings.NewReader("x")); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for oversize, got %v", err)
	}
	if _, err := e.th.CreateAttachmentUpload(ctx, userID, envID, "empty.png", "image/png", 0, strings.NewReader("")); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for empty body, got %v", err)
	}

	// 内容代理
	e.fake.mu.Lock()
	e.fake.assetContent["att-1"] = image
	e.fake.mu.Unlock()
	contentType, data, err := e.th.GetAttachmentContent(ctx, userID, envID, "att-1")
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if contentType != "image/png" || string(data) != string(image) {
		t.Fatalf("content = (%q, %d bytes)", contentType, len(data))
	}

	// 上游未就绪（AssetAttachmentNotFoundError）→ 40418
	e.fake.mu.Lock()
	e.fake.assetMissing = true
	e.fake.mu.Unlock()
	if _, _, err := e.th.GetAttachmentContent(ctx, userID, envID, "att-gone"); err == nil || t3ErrCode(err) != errs.ErrT3ThreadNotFound.Code {
		t.Fatalf("expected 40418 for missing attachment, got %v", err)
	}
	e.fake.mu.Lock()
	e.fake.assetMissing = false
	e.fake.mu.Unlock()

	// 删除
	if err := e.th.DeleteAttachment(ctx, userID, envID, "att-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted := e.fake.deletedAttachmentIDs(); len(deleted) != 1 || deleted[0] != "att-1" {
		t.Fatalf("deleted = %+v", deleted)
	}
}

// TestT3ComposerAndWorkspaceOptions 覆盖启动器/工作区选项读取。项目表来自
// `/api/projects`：两条路径都零 shell 订阅（INT-116）。
func TestT3ComposerAndWorkspaceOptions(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "选项机")
	// Create 已预热常驻订阅（INT-132）：等它稳定，两条选项路径的断言窗口
	// 从这里起算，预热流量不进窗口。
	before := awaitPrewarmSubscriptions(t, e.fake)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "Fast Ship 主仓库", "workspaceRoot": "/repo", "defaultModelSelection": map[string]any{"instanceId": "prov", "model": "m1"}},
	}})
	e.fake.mu.Unlock()

	options, err := e.th.ComposerOptions(ctx, userID, envID)
	if err != nil {
		t.Fatalf("composer options: %v", err)
	}
	if len(options.Projects) != 1 || options.Projects[0].Id != "p-1" || apiStr(options.Projects[0].WorkspaceRoot) != "/repo" {
		t.Fatalf("projects = %+v", options.Projects)
	}
	if options.Projects[0].DefaultModelSelection == nil || options.Projects[0].DefaultModelSelection.Model != "m1" {
		t.Fatalf("default model = %+v", options.Projects[0].DefaultModelSelection)
	}
	if _, ok := options.Config["providers"]; !ok {
		t.Fatalf("config passthrough = %+v", options.Config)
	}
	if _, ok := options.Settings["newWorktreesStartFromOrigin"]; !ok {
		t.Fatalf("settings passthrough = %+v", options.Settings)
	}

	workspace, err := e.th.WorkspaceOptions(ctx, userID, envID, "p-1", "", nil)
	if err != nil {
		t.Fatalf("workspace options: %v", err)
	}
	if workspace.Cwd != "/repo" || apiStr(workspace.RefName) != "main" || !apiDerefBool(workspace.IsRepo) {
		t.Fatalf("workspace = %+v", workspace)
	}
	if len(workspace.Refs) != 1 || workspace.Refs[0].Name != "main" || workspace.Refs[0].Kind != "local" {
		t.Fatalf("refs = %+v", workspace.Refs)
	}
	if workspace.NextCursor != nil {
		t.Fatalf("next cursor = %+v", workspace.NextCursor)
	}

	// 项目不在环境 → 40401
	if _, err := e.th.WorkspaceOptions(ctx, userID, envID, "p-other", "", nil); err == nil || t3ErrCode(err) != 40401 {
		t.Fatalf("expected 40401 for foreign project, got %v", err)
	}

	// 带分页参数：cursor/query 透传
	if _, err := e.th.WorkspaceOptions(ctx, userID, envID, "p-1", "feat", apiIntPtr(2)); err != nil {
		t.Fatalf("workspace options paged: %v", err)
	}
	var sawCursor bool
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method != "vcs.listRefs" {
			continue
		}
		var payload map[string]any
		_ = json.Unmarshal(rpc.payload, &payload)
		if payload["query"] == "feat" && payload["cursor"] == float64(2) && payload["cwd"] == "/repo" {
			sawCursor = true
		}
	}
	if !sawCursor {
		t.Fatalf("listRefs pagination params not forwarded")
	}

	e.fake.mu.Lock()
	e.fake.vcsRefsPages = []json.RawMessage{
		mustJSON(map[string]any{
			"refs":       []any{map[string]any{"name": "main", "isRemote": false}},
			"nextCursor": 4, "totalCount": 2,
		}),
		mustJSON(map[string]any{
			"refs":       []any{map[string]any{"name": "feat", "isRemote": false}},
			"nextCursor": nil, "totalCount": 2,
		}),
	}
	e.fake.mu.Unlock()
	followed, err := e.th.WorkspaceOptions(ctx, userID, envID, "p-1", "", nil)
	if err != nil {
		t.Fatalf("workspace follow: %v", err)
	}
	if len(followed.Refs) != 2 || followed.Refs[0].Name != "main" || followed.Refs[1].Name != "feat" {
		t.Fatalf("followed refs = %+v", followed.Refs)
	}
	if followed.NextCursor != nil {
		t.Fatalf("auto-follow should drain next_cursor, got %+v", followed.NextCursor)
	}

	// 两条路径零 shell 订阅：config/settings/vcs RPC 照常，订阅 absent
	// （断言窗口从预热稳定后起算，Create 预热的流量不进窗口）。
	for _, rpc := range e.fake.recordedRPC()[before:] {
		switch rpc.method {
		case t3RPCSubscribeShell, "orchestration.subscribeArchivedShell":
			t.Fatalf("composer/workspace path must not subscribe shell, got %s", rpc.method)
		}
	}
}

// TestT3ComposerSettingsFromConfig 覆盖 INT-173：上游 #26238 删除独立的
// server.getSettings 后，设置改从 getConfig 内嵌的 settings 字段读取。
// 新上游（缺省 fake）：Settings 非空、worktree startFromOrigin 继承内嵌值，
// 且不得再调 server.getSettings；旧上游（legacy 开关）：getConfig 缺
// settings 字段 → 回退一次 server.getSettings 拿到真实设置（字段缺失
// 与「字段为 null/空」严格区分，后者不回退）。
func TestT3ComposerSettingsFromConfig(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "设置机")
	_ = awaitPrewarmSubscriptions(t, e.fake)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.projects = mustJSON(map[string]any{"projects": []any{
		map[string]any{"id": "p-1", "title": "P", "workspaceRoot": "/repo",
			"defaultModelSelection": map[string]any{"instanceId": "prov", "model": "m1"}},
	}})
	e.fake.mu.Unlock()

	createWorktreeThread := func(t *testing.T) map[string]any {
		t.Helper()
		if _, err := e.th.CreateThread(ctx, userID, envID, &T3CreateThreadRequest{
			ProjectId: "p-1",
			Message:   api.CreateT3ThreadRequest_Message{Text: "wt"},
			Workspace: &api.T3ThreadWorkspace{Type: "worktree"},
		}); err != nil {
			t.Fatalf("create worktree thread: %v", err)
		}
		var launch map[string]any
		for _, rpc := range e.fake.recordedRPC() {
			if rpc.method == "orchestration.launchThread" {
				launch = nil
				if json.Unmarshal(rpc.payload, &launch) != nil {
					t.Fatalf("launch payload: %s", rpc.payload)
				}
			}
		}
		ws, _ := launch["workspaceStrategy"].(map[string]any)
		if ws == nil || ws["type"] != "worktree" {
			t.Fatalf("worktree workspace = %+v", launch["workspaceStrategy"])
		}
		return ws
	}

	// 新上游：settings 内嵌在 getConfig 里（fake 缺省 newWorktreesStartFromOrigin=true）。
	options, err := e.th.ComposerOptions(ctx, userID, envID)
	if err != nil {
		t.Fatalf("composer options: %v", err)
	}
	if len(options.Settings) == 0 || options.Settings["newWorktreesStartFromOrigin"] != true {
		t.Fatalf("settings must come from config.settings, got %+v", options.Settings)
	}
	if ws := createWorktreeThread(t); ws["startFromOrigin"] != true {
		t.Fatalf("startFromOrigin must inherit true from config.settings, got %+v", ws)
	}

	// 内嵌设置翻为 false：两条路径都必须如实继承。
	e.fake.mu.Lock()
	e.fake.configValue = mustJSON(map[string]any{
		"providers": map[string]any{}, "shellResumeCompletionMarker": true,
		"settings": map[string]any{"newWorktreesStartFromOrigin": false},
	})
	e.fake.mu.Unlock()
	options, err = e.th.ComposerOptions(ctx, userID, envID)
	if err != nil {
		t.Fatalf("composer options (false): %v", err)
	}
	if options.Settings["newWorktreesStartFromOrigin"] != false {
		t.Fatalf("settings must carry explicit false, got %+v", options.Settings)
	}
	if ws := createWorktreeThread(t); ws["startFromOrigin"] != false {
		t.Fatalf("startFromOrigin must inherit false from config.settings, got %+v", ws)
	}

	// 旧上游：getConfig 缺 settings 字段 → 回退一次 server.getSettings 拿到
	// 真实设置（fake 的 settingsValue=newWorktreesStartFromOrigin=true）。
	before := len(e.fake.recordedRPC())
	e.fake.useLegacySettings()
	options, err = e.th.ComposerOptions(ctx, userID, envID)
	if err != nil {
		t.Fatalf("composer options (legacy): %v", err)
	}
	if options.Settings["newWorktreesStartFromOrigin"] != true {
		t.Fatalf("legacy env must read settings via server.getSettings, got %+v", options.Settings)
	}
	var sawGetSettings bool
	for _, rpc := range e.fake.recordedRPC()[before:] {
		if rpc.method == "server.getSettings" {
			sawGetSettings = true
		}
	}
	if !sawGetSettings {
		t.Fatalf("legacy env must fall back to server.getSettings")
	}

	// 旧上游 worktree 启动同样回退 getSettings 继承 startFromOrigin
	// （R3 P2：worktree 与 ComposerOptions 共用 resolveT3Settings，缺
	// settings 字段不能静默丢用户设置）。
	before = len(e.fake.recordedRPC())
	if ws := createWorktreeThread(t); ws["startFromOrigin"] != true {
		t.Fatalf("legacy worktree must inherit startFromOrigin via server.getSettings, got %+v", ws)
	}
	sawGetSettings = false
	for _, rpc := range e.fake.recordedRPC()[before:] {
		if rpc.method == "server.getSettings" {
			sawGetSettings = true
		}
	}
	if !sawGetSettings {
		t.Fatalf("legacy worktree path must fall back to server.getSettings")
	}

	// 「字段为 null」是新版空设置：不得回退旧 RPC（新上游已删 getSettings，
	// 误调会被 fake 炸出来）。
	e.fake.mu.Lock()
	e.fake.legacySettings = false
	e.fake.configValue = mustJSON(map[string]any{
		"providers": map[string]any{}, "shellResumeCompletionMarker": true,
		"settings": nil,
	})
	e.fake.mu.Unlock()
	before = len(e.fake.recordedRPC())
	options, err = e.th.ComposerOptions(ctx, userID, envID)
	if err != nil {
		t.Fatalf("composer options (null settings): %v", err)
	}
	if len(options.Settings) != 0 {
		t.Fatalf("null settings must yield empty map, got %+v", options.Settings)
	}
	for _, rpc := range e.fake.recordedRPC()[before:] {
		if rpc.method == "server.getSettings" {
			t.Fatalf("null settings (new env) must not fall back to server.getSettings")
		}
	}
}

// TestT3DetailQueueAndActivity 覆盖详情的 queue/activity 派生（对齐
// deriveThreadQueueWorkflowState 的口径）。
func TestT3DetailQueueAndActivity(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "队列详情机")
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-qa", "排队详情", "running", map[string]any{
			"pendingRuntimeRequest":  map[string]any{"id": "rr-1", "kind": "approval", "createdAt": "2026-10-12T04:00:00.000Z"},
			"pendingBackgroundTasks": []any{map[string]any{"taskId": "bg-1", "kind": "subagent"}, map[string]any{"taskId": "bg-2", "kind": "command"}},
			"latestRunId":            "run-2",
		})},
	)
	// run-2 活动中，最近条目是 run_interrupt_request 且无结果 → stopping=true；
	// 两条用户排队消息（一条被通知消息排除）。
	e.fake.projections["th-qa"] = mustJSONString(map[string]any{
		"thread": appThreadFixture("th-qa", "排队详情", nil),
		"runs": []any{
			map[string]any{"id": "run-1", "ordinal": 1, "status": "completed", "userMessageId": "m0"},
			map[string]any{"id": "run-2", "ordinal": 2, "status": "running", "userMessageId": "m1", "activeAttemptId": "att-1"},
			map[string]any{"id": "run-3", "ordinal": 3, "status": "queued", "userMessageId": "m2", "queuePosition": 1},
			map[string]any{"id": "run-4", "ordinal": 4, "status": "queued", "userMessageId": "m3", "queuePosition": 2},
			map[string]any{"id": "run-5", "ordinal": 5, "status": "queued", "userMessageId": "m-notify", "queuePosition": 3},
		},
		"messages": []any{
			map[string]any{"id": "m0", "text": "初始", "attachments": []any{}},
			map[string]any{"id": "m1", "text": "进行中", "attachments": []any{}},
			map[string]any{"id": "m2", "text": "第一排队", "attachments": []any{}},
			map[string]any{"id": "m3", "text": "第二排队", "attachments": []any{}},
			map[string]any{"id": "m-notify", "text": "后台通知", "attachments": []any{}, "notification": map[string]any{"outcome": "completed"}},
		},
		"providerSessions": []any{map[string]any{"id": "s1", "status": "connected", "capabilities": map[string]any{
			"turns": map[string]any{"supportsQueuedMessages": true, "supportsSteeringByInterruptRestart": true},
		}}},
		"providerThreads": []any{},
		"providerTurns":   []any{map[string]any{"runAttemptId": "att-1", "status": "running"}},
		"runtimeRequests": []any{},
		"turnItems": []any{
			map[string]any{"id": "sig-1", "type": "run_interrupt_request", "runId": "run-2", "ordinal": 40},
		},
		"visibleTurnItems": []any{
			map[string]any{"position": 0, "visibility": "local", "sourceThreadId": "th-qa", "sourceItemId": "i0",
				"item": map[string]any{"id": "i0", "type": "user_message", "status": "completed", "text": "你好"}},
		},
	})
	e.fake.mu.Unlock()

	detail, err := e.th.Detail(ctx, userID, envID, "th-qa")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Queue.Items) != 2 {
		t.Fatalf("queue items = %+v", detail.Queue.Items)
	}
	if detail.Queue.Items[0].Text != "第一排队" || detail.Queue.Items[1].Text != "第二排队" {
		t.Fatalf("queue order = %+v", detail.Queue.Items)
	}
	if !detail.Queue.CanReorder || !detail.Queue.CanSteer {
		t.Fatalf("capabilities = %+v", detail.Queue)
	}
	if detail.Queue.Held {
		t.Fatalf("queue must not be held")
	}
	if apiStr(detail.Activity.ActiveRunId) != "run-2" || apiStr(detail.Activity.ActiveRunStatus) != "running" {
		t.Fatalf("activity run = %+v", detail.Activity)
	}
	if !detail.Activity.Stopping {
		t.Fatalf("interrupt request without result must report stopping")
	}
	if !detail.Activity.HasPendingRequests {
		t.Fatalf("pending runtime request must surface")
	}
	if detail.Activity.PendingBackgroundTaskCount != 1 {
		t.Fatalf("background count = %d (command 不占位)", detail.Activity.PendingBackgroundTaskCount)
	}
}

func TestDeriveT3ThreadQueueEmptyTextAndMissingPosition(t *testing.T) {
	pos := 2
	queue := deriveT3ThreadQueue(&t3ThreadProjection{
		Runs: []t3Run{
			{ID: "q-missing-pos", Ordinal: 8, Status: "queued", UserMessageID: "m-late"},
			{ID: "q-image", Ordinal: 3, Status: "queued", QueuePosition: &pos, UserMessageID: "m-img"},
		},
		Messages: []t3Message{
			{ID: "m-late", Text: "后发的文字"},
			{ID: "m-img", Text: "", Attachments: []json.RawMessage{mustJSON(map[string]any{
				"type": "image", "id": "att-1", "name": "shot.png", "mimeType": "image/png", "sizeBytes": 12,
			})}},
		},
	})
	if len(queue.Items) != 2 {
		t.Fatalf("items = %+v", queue.Items)
	}
	if queue.Items[0].RunId != "q-image" || queue.Items[0].Text != "" || len(queue.Items[0].Attachments) != 1 {
		t.Fatalf("image-only first = %+v", queue.Items[0])
	}
	if queue.Items[1].RunId != "q-missing-pos" || queue.Items[1].Text != "后发的文字" {
		t.Fatalf("missing position should sort by ordinal, got %+v", queue.Items[1])
	}
}

func TestT3HasInterruptibleBackgroundWork(t *testing.T) {
	if t3HasInterruptibleBackgroundWork([]t3PendingBackgroundTask{
		{TaskID: "pull-request-watch:1", Kind: "monitor"},
		{TaskID: "cmd-1", Kind: "command"},
	}) {
		t.Fatalf("PR watch + command must not count as interruptible")
	}
	if !t3HasInterruptibleBackgroundWork([]t3PendingBackgroundTask{
		{TaskID: "bg-1", Kind: "subagent"},
	}) {
		t.Fatalf("subagent must count")
	}
	// monitor 不整体排除：普通 monitor（如 agent 挂的文件看护）在上游
	// interruptThreadTurn 的回退门里也算后台工作，唯独 PR watch 前缀不算。
	if !t3HasInterruptibleBackgroundWork([]t3PendingBackgroundTask{
		{TaskID: "watch-src", Kind: "monitor"},
	}) {
		t.Fatalf("plain monitor must count as interruptible")
	}
}

// TestT3HoldsCompletionVsInterruptible 钉住两个判定的分离：占完成位（谁会
// 唤醒代理）与可被停止（Stop 回退还能对什么下手）口径不同，同一条后台工作
// 在两侧可以得出不同结论——awaited 的 command 占位但不可停止；普通 monitor
// 不占位但可停止；PR watch 两侧都不占。
func TestT3HoldsCompletionVsInterruptible(t *testing.T) {
	cases := []struct {
		name          string
		task          t3PendingBackgroundTask
		holds         bool
		interruptible bool
	}{
		{"awaited command：占位、不可停止", t3PendingBackgroundTask{TaskID: "c1", Kind: "command", Awaited: true}, true, false},
		{"普通 command：都不占", t3PendingBackgroundTask{TaskID: "c2", Kind: "command"}, false, false},
		{"普通 monitor：不占位、可停止", t3PendingBackgroundTask{TaskID: "w1", Kind: "monitor"}, false, true},
		{"awaited monitor：占位、可停止", t3PendingBackgroundTask{TaskID: "w2", Kind: "monitor", Awaited: true}, true, true},
		{"PR watch monitor：都不占", t3PendingBackgroundTask{TaskID: "pull-request-watch:o/r#1", Kind: "monitor"}, false, false},
		{"subagent：占位、可停止", t3PendingBackgroundTask{TaskID: "s1", Kind: "subagent"}, true, true},
		{"background_task：占位、可停止", t3PendingBackgroundTask{TaskID: "b1", Kind: "background_task"}, true, true},
		{"未知 kind：占位、可停止", t3PendingBackgroundTask{TaskID: "u1", Kind: "hologram"}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := t3TaskHoldsCompletion(c.task); got != c.holds {
				t.Fatalf("holds = %v, want %v", got, c.holds)
			}
			if got := t3HasInterruptibleBackgroundWork([]t3PendingBackgroundTask{c.task}); got != c.interruptible {
				t.Fatalf("interruptible = %v, want %v", got, c.interruptible)
			}
		})
	}
}

// ------ 小助手 ------

func apiStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func apiDerefBool(v *bool) bool {
	return v != nil && *v
}

func apiIntPtr(v int) *int { return &v }
