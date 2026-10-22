package service

// INT-148 第 1、2 部分（后端）：线程读取补 run_status / pull_requests[]（含
// watching），fs_thread 新增 settle/unsettle/resume_queue 写动作。投影用纯
// 函数断言（normalizeT3ThreadShell / normalizeT3PullRequests），写动作对 fake
// T3 断言 dispatchCommand payload、能力缺失报错与只读守卫。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// int148ThreadEnv 构造归一化用的最小环境。
func int148ThreadEnv() *model.T3Environment {
	return &model.T3Environment{ID: "env-1", Name: "测试环境"}
}

func TestT3Int148PullRequestProjection(t *testing.T) {
	shell := &t3ThreadShell{
		ID:     "th-1",
		Status: "idle",
		PullRequests: []json.RawMessage{
			// 快照齐全 + 挂监听：watch 存在即 watching=true。
			json.RawMessage(`{"host":"github.com","repository":"owner/repo","number":7,
				"url":"https://github.com/owner/repo/pull/7","source":"created",
				"linkedAt":"2026-01-01T00:00:00Z",
				"snapshot":{"state":"open","title":"Add settle","headBranch":"a","baseBranch":"main",
					"isDraft":false,"updatedAt":"2026-01-02T00:00:00Z","syncedAt":"2026-01-02T00:00:00Z",
					"mergedAt":"2026-01-03T00:00:00Z","checksState":"passing","mergeability":"mergeable"},
				"watch":{"startedAt":"2026-01-02T00:00:00Z","headSha":"abc","failedChecks":[],"passed":false,
					"remarksThrough":"2026-01-02T00:00:00Z","remarkIds":[],"conflicting":false,"wakes":0}}`),
			// snapshot 为 null（首次同步前）：只投影 repo/number/url/watching。
			json.RawMessage(`{"host":"github.com","repository":"owner/other","number":8,
				"url":"https://github.com/owner/other/pull/8","source":"manual","snapshot":null}`),
			// 坏条目：跳过，不毁掉整个列表。
			json.RawMessage(`{"host":`),
		},
	}
	summary := normalizeT3ThreadShell(int148ThreadEnv(), shell, nil)
	prs := summary.PullRequests
	if len(prs) != 2 {
		t.Fatalf("应投影 2 条 PR（坏条目跳过），实得 %d: %+v", len(prs), prs)
	}
	first := prs[0]
	if first.Repo != "github.com/owner/repo" || first.Number != 7 ||
		first.Url != "https://github.com/owner/repo/pull/7" || !first.Watching {
		t.Fatalf("第 1 条投影不符: %+v", first)
	}
	if first.State == nil || *first.State != "open" || first.MergedAt == nil || *first.MergedAt != "2026-01-03T00:00:00Z" {
		t.Fatalf("第 1 条 snapshot 字段不符: %+v", first)
	}
	if first.IsDraft == nil || *first.IsDraft {
		t.Fatalf("is_draft 应为 false: %+v", first)
	}
	if first.ChecksState == nil || *first.ChecksState != "passing" ||
		first.Mergeability == nil || *first.Mergeability != "mergeable" {
		t.Fatalf("checks/mergeability 不符: %+v", first)
	}
	second := prs[1]
	if second.Repo != "github.com/owner/other" || second.Number != 8 || second.Watching {
		t.Fatalf("第 2 条投影不符: %+v", second)
	}
	// snapshot 为 null：可选字段全部缺省。
	if second.State != nil || second.MergedAt != nil || second.IsDraft != nil ||
		second.ChecksState != nil || second.Mergeability != nil {
		t.Fatalf("snapshot=null 时可选字段应省略: %+v", second)
	}

	// 序列化恒为数组：无 PR 的线程 pull_requests 是 []，不是 null。
	noPR := normalizeT3ThreadShell(int148ThreadEnv(), &t3ThreadShell{ID: "th-2", Status: "idle"}, nil)
	if noPR.PullRequests == nil || len(noPR.PullRequests) != 0 {
		t.Fatalf("无 PR 时应为空数组，实得 %+v", noPR.PullRequests)
	}
	raw, err := json.Marshal(noPR)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"pull_requests":[]`) {
		t.Fatalf("无 PR 应序列化为空数组: %s", raw)
	}
	// 旧服务端只有 linkedPullRequest/branchPullRequest：明细仍为空数组，
	// 不合成缺 state 的条目（计数回退逻辑另行保持）。
	legacyRaw := json.RawMessage(`{"repository":"owner/x","number":3,"url":"u"}`)
	legacy := &t3ThreadShell{ID: "th-3", Status: "idle", LinkedPullReq: &legacyRaw}
	legacySummary := normalizeT3ThreadShell(int148ThreadEnv(), legacy, nil)
	if len(legacySummary.PullRequests) != 0 || legacySummary.PullRequestCount != 1 {
		t.Fatalf("旧服务端形状：明细为空、计数回退为 1，实得 %+v count=%d",
			legacySummary.PullRequests, legacySummary.PullRequestCount)
	}
}

// 墓碑条目（source=stack-dismissed，用户取消关联的栈成员）：上游只为同步去重
// 保留，客户端要隐藏——不进 pull_requests[] 也不计入 pull_request_count，
// 计数与可见明细恒一致。
func TestT3Int148PullRequestTombstones(t *testing.T) {
	shell := &t3ThreadShell{
		ID: "th-1", Status: "idle",
		PullRequests: []json.RawMessage{
			// 正常链接 + 墓碑 + snapshot=null 的正常链接，同一数组混合。
			json.RawMessage(`{"host":"github.com","repository":"o/r","number":1,"url":"u1","source":"created",
				"snapshot":{"state":"open","isDraft":false}}`),
			json.RawMessage(`{"host":"github.com","repository":"o/r","number":2,"url":"u2","source":"stack-dismissed","snapshot":null}`),
			json.RawMessage(`{"host":"github.com","repository":"o/r","number":3,"url":"u3","source":"stack","snapshot":null}`),
		},
	}
	summary := normalizeT3ThreadShell(int148ThreadEnv(), shell, nil)
	if len(summary.PullRequests) != 2 || summary.PullRequestCount != 2 {
		t.Fatalf("墓碑应被过滤且计数等于可见条数: len=%d count=%d",
			len(summary.PullRequests), summary.PullRequestCount)
	}
	for _, pr := range summary.PullRequests {
		if pr.Number == 2 {
			t.Fatalf("墓碑条目不得出现在明细: %+v", summary.PullRequests)
		}
	}

	// 全为墓碑：列表空、count=0。
	allTomb := &t3ThreadShell{
		ID: "th-2", Status: "idle",
		PullRequests: []json.RawMessage{
			json.RawMessage(`{"host":"github.com","repository":"o/r","number":4,"url":"u4","source":"stack-dismissed"}`),
			json.RawMessage(`{"host":"github.com","repository":"o/r","number":5,"url":"u5","source":"stack-dismissed"}`),
		},
	}
	allTombSummary := normalizeT3ThreadShell(int148ThreadEnv(), allTomb, nil)
	if len(allTombSummary.PullRequests) != 0 || allTombSummary.PullRequestCount != 0 {
		t.Fatalf("全为墓碑应列表空、count=0: len=%d count=%d",
			len(allTombSummary.PullRequests), allTombSummary.PullRequestCount)
	}
}

// run_status 是 shell.status 原文透传：completed/failed/cancelled 在归一化
// status 里折叠成 idle，终态区分只能靠 run_status。
func TestT3Int148RunStatusPassthrough(t *testing.T) {
	for _, rawStatus := range []string{"completed", "failed", "cancelled", "interrupted", "idle", "running"} {
		summary := normalizeT3ThreadShell(int148ThreadEnv(), &t3ThreadShell{ID: "th-1", Status: rawStatus}, nil)
		if summary.RunStatus != rawStatus {
			t.Fatalf("run_status 应原样透传 %q，实得 %q", rawStatus, summary.RunStatus)
		}
	}
	for _, rawStatus := range []string{"completed", "failed", "cancelled"} {
		summary := normalizeT3ThreadShell(int148ThreadEnv(), &t3ThreadShell{ID: "th-1", Status: rawStatus}, nil)
		if summary.Status != api.T3ThreadSummaryStatusIdle {
			t.Fatalf("%q 应折叠成 idle（run_status 才带终态），实得 %s", rawStatus, summary.Status)
		}
	}
}

// capability 响应 → 支持判定的纯函数分支。
func TestT3Int148SettlementCapabilityParse(t *testing.T) {
	if !t3ConfigSupportsThreadSettlement(json.RawMessage(
		`{"environment":{"capabilities":{"threadSettlement":true}}}`)) {
		t.Fatalf("threadSettlement=true 应判支持")
	}
	for name, raw := range map[string]json.RawMessage{
		"缺失":     json.RawMessage(`{"environment":{"capabilities":{}}}`),
		"false":  json.RawMessage(`{"environment":{"capabilities":{"threadSettlement":false}}}`),
		"无环境段":   json.RawMessage(`{"providers":{}}`),
		"坏 JSON": json.RawMessage(`{`),
		"旧服务端全缺": json.RawMessage(`{"shellResumeCompletionMarker":true}`),
	} {
		if t3ConfigSupportsThreadSettlement(raw) {
			t.Fatalf("%s 应判不支持", name)
		}
	}
}

func int148SettlementConfig(enabled bool) json.RawMessage {
	return mustJSON(map[string]any{
		"environment": map[string]any{
			"capabilities": map[string]any{"threadSettlement": enabled},
		},
	})
}

// settle/unsettle/resume_queue 的 dispatchCommand payload：type/threadId/
// commandId 齐备，unsettle 带 reason=user。
func TestT3Int148SettlementCommandsDispatch(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "结算机")
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.configValue = int148SettlementConfig(true)
	e.fake.mu.Unlock()

	if _, err := e.th.SettleThread(ctx, userID, envID, "th-1"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if _, err := e.th.UnsettleThread(ctx, userID, envID, "th-1"); err != nil {
		t.Fatalf("unsettle: %v", err)
	}
	if _, err := e.th.ResumeQueue(ctx, userID, envID, "th-1"); err != nil {
		t.Fatalf("resume_queue: %v", err)
	}

	var settle, unsettle, resume map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method != "orchestration.dispatchCommand" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(rpc.payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		switch payload["type"] {
		case "thread.settle":
			settle = payload
		case "thread.unsettle":
			unsettle = payload
		case "queue.resume":
			resume = payload
		}
	}
	for name, payload := range map[string]map[string]any{
		"thread.settle": settle, "thread.unsettle": unsettle, "queue.resume": resume,
	} {
		if payload == nil {
			t.Fatalf("缺 %s 命令", name)
		}
		if payload["threadId"] != "th-1" {
			t.Fatalf("%s threadId 不符: %+v", name, payload)
		}
		if id, _ := payload["commandId"].(string); id == "" {
			t.Fatalf("%s 缺 commandId: %+v", name, payload)
		}
	}
	if unsettle["reason"] != "user" {
		t.Fatalf("unsettle 应带 reason=user: %+v", unsettle)
	}
}

// 能力缺失：派发前明确拒绝（50202 + 固定提示），不发出任何命令。
func TestT3Int148SettlementRequiresCapability(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	envID := pairWritable(t, e, userID, "旧版机")
	ctx := context.Background()

	for name, enabled := range map[string]*bool{"缺失": nil, "false": api.Ptr(false)} {
		e.fake.mu.Lock()
		if enabled == nil {
			// 缺省 configValue 就没有 capabilities.threadSettlement。
			e.fake.configValue = mustJSON(map[string]any{"providers": map[string]any{}})
		} else {
			e.fake.configValue = int148SettlementConfig(*enabled)
		}
		e.fake.mu.Unlock()

		_, err := e.th.SettleThread(ctx, userID, envID, "th-1")
		if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
			t.Fatalf("%s：settle 应返回 50202，实得 %v", name, err)
		}
		if !strings.Contains(err.Error(), t3SettlementUnsupportedMessage) {
			t.Fatalf("%s：应带明确提示，实得 %v", name, err)
		}
		if _, err := e.th.UnsettleThread(ctx, userID, envID, "th-1"); err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
			t.Fatalf("%s：unsettle 应返回 50202，实得 %v", name, err)
		}
		for _, rpc := range e.fake.recordedRPC() {
			if rpc.method == "orchestration.dispatchCommand" {
				t.Fatalf("%s：能力缺失不得派发命令", name)
			}
		}
	}
}

// 只读环境（access=read-only）：写入口统一 40300，不发出任何 RPC。
func TestT3Int148SettlementReadOnlyGuard(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.grantScope = t3ScopeOrchestrationRead
	e.fake.mu.Unlock()
	created, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "只读结算机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Access != T3EnvironmentAccessReadOnly {
		t.Fatalf("access = %v", created.Access)
	}

	if _, err := e.th.SettleThread(ctx, userID, created.Id, "th-1"); err == nil || t3ErrCode(err) != errs.ErrPermissionDenied.Code {
		t.Fatalf("settle 应返回 40300，实得 %v", err)
	}
	if _, err := e.th.UnsettleThread(ctx, userID, created.Id, "th-1"); err == nil || t3ErrCode(err) != errs.ErrPermissionDenied.Code {
		t.Fatalf("unsettle 应返回 40300，实得 %v", err)
	}
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.dispatchCommand" || rpc.method == "server.getConfig" {
			t.Fatalf("只读环境不得发出 %s", rpc.method)
		}
	}
	var appErr *errs.AppError
	if _, err := e.th.SettleThread(ctx, userID, "env-missing", "th-1"); !errors.As(err, &appErr) || appErr.Code != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("环境不存在应返回 40417，实得 %v", err)
	}
}
