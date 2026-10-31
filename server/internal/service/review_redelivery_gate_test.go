package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
)

// INT-146 重投前的 PR 实况闸门测试：payload 只能定位 repo/PR（旧快照写着
// open），是否还开着以 GetPullRequest 实时 State 为准。已关闭的投递记终态后
// 静默跳过（不重投、不通知、下一轮不再查询）；读不到 payload/PR 实况时降级
// 为原行为照常重投。种子与断言辅助复用 review_redelivery_test.go。

// redeliveryGatePayload 构造投递详情里的 request.payload：闸门定位 repo/PR
// 用，只含必要字段（payload 是投递时刻的旧快照，这里不带 PR 状态——状态
// 也不该从它读）。
func redeliveryGatePayload(repo string, number int) json.RawMessage {
	body, err := json.Marshal(map[string]any{
		"number":       number,
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": 1},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// redeliveryClosedRow 查某 guid 的重投账行；无行返回 false。
func redeliveryClosedRow(t *testing.T, env *reviewEngineEnv, guid string) (model.GitHubWebhookRedelivery, bool) {
	t.Helper()
	var row model.GitHubWebhookRedelivery
	err := env.svc.db.Where("guid = ?", guid).First(&row).Error
	if err != nil {
		return model.GitHubWebhookRedelivery{}, false
	}
	return row, true
}

// TestRedeliveryGateOpenPRStillRedelivers 投递指向的 PR 仍 open：闸门放行，
// 照常重投并记次数账，不记终态、不发通知。
func TestRedeliveryGateOpenPRStillRedelivers(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(1101, "guid-open", "pull_request", "synchronize", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(1101, redeliveryGatePayload("owner/repo", 7))
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 1 || got[0] != 1101 {
		t.Fatalf("redelivered = %v, want [1101]", got)
	}
	if n := mustLedgerAttempts(t, env, "guid-open"); n != 1 {
		t.Fatalf("guid-open 重投次数账 = %d, want 1", n)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-open"); !ok || row.ClosedAt != nil {
		t.Fatalf("open PR 不应记终态: %+v (ok=%v)", row, ok)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("notifications = %d, want 0", n)
	}
}

// TestRedeliveryGateClosedPRSkippedQuietly 投递指向的 PR 已关闭：不重投、不
// 记次数、不通知，账记终态（closed_at 非空）；再次对账不重发任何查询——
// 终态后 payload/PR 都不再拉。
func TestRedeliveryGateClosedPRSkippedQuietly(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(2101, "guid-closed", "pull_request", "synchronize", 502, false, base),
	})
	env.fake.SetHookDeliveryPayload(2101, redeliveryGatePayload("owner/repo", 7))
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{State: "closed"})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("已关闭 PR 不应重投，实际 %v", got)
	}
	row, ok := redeliveryClosedRow(t, env, "guid-closed")
	if !ok || row.ClosedAt == nil {
		t.Fatalf("应记 closed_at 终态: %+v (ok=%v)", row, ok)
	}
	if row.Attempts != 0 {
		t.Fatalf("跳过不应记重投次数，实际 %d", row.Attempts)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("notifications = %d, want 0", n)
	}

	before := env.fake.CallCounts()
	env.review.reconcileWebhookDeliveries(redeliveryTestNow.Add(6 * time.Minute))
	after := env.fake.CallCounts()
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("终态后不应重投，实际 %v", got)
	}
	for _, m := range []string{"GetHookDelivery", "GetPullRequest"} {
		if after[m] != before[m] {
			t.Fatalf("终态后不应再查 %s：%d → %d", m, before[m], after[m])
		}
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("终态后不应发通知，实际 %d 条", n)
	}
}

// TestRedeliveryGateClosedPRWithExhaustedAttemptsNotNotified 误报主场景：
// 已超 3 次上限的失败投递，其 PR 已关闭——闸门先于超限通知判定，记终态后
// 静默跳过，不发 NotificationReviewRedeliveryFailed；账行保留原次数、只补
// closed_at。
func TestRedeliveryGateClosedPRWithExhaustedAttemptsNotNotified(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-2 * time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(2201, "guid-x3", "pull_request", "synchronize", 502, false, base),
	})
	env.fake.SetHookDeliveryPayload(2201, redeliveryGatePayload("owner/repo", 7))
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{State: "closed"})
	seedRedeliveryLedger(t, env, "guid-x3", 3)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("不应重投，实际 %v", got)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("已关闭的 PR 不应发超限误报，实际 %d 条", n)
	}
	row, ok := redeliveryClosedRow(t, env, "guid-x3")
	if !ok || row.ClosedAt == nil {
		t.Fatalf("应记 closed_at 终态: %+v (ok=%v)", row, ok)
	}
	if row.Attempts != 3 {
		t.Fatalf("记终态不应改写次数，实际 %d, want 3", row.Attempts)
	}
}

// TestRedeliveryGateDegradesWhenUnreadable 读不到实况时降级为原行为：payload
// 未登记（详情拉取失败）、payload 解析得出 PR 但实时读失败（PR 未登记）两种
// 情况都照常重投并记次数账，且不记终态——闸门只省白跑，不能反过来漏掉真投递。
func TestRedeliveryGateDegradesWhenUnreadable(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		// payload 未登记：GetHookDelivery 失败。
		hookRow(2301, "guid-deg1", "pull_request", "synchronize", 500, false, base),
		// payload 指向的 PR 未登记：GetPullRequest 失败。
		hookRow(2302, "guid-deg2", "pull_request", "synchronize", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(2302, redeliveryGatePayload("owner/ghost", 99))

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 2 || got[0] != 2301 || got[1] != 2302 {
		t.Fatalf("redelivered = %v, want [2301 2302]（降级照常重投）", got)
	}
	for _, guid := range []string{"guid-deg1", "guid-deg2"} {
		if n := mustLedgerAttempts(t, env, guid); n != 1 {
			t.Fatalf("%s 重投次数账 = %d, want 1", guid, n)
		}
		if row, ok := redeliveryClosedRow(t, env, guid); !ok || row.ClosedAt != nil {
			t.Fatalf("%s 读不到实况不应记终态: %+v (ok=%v)", guid, row, ok)
		}
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("notifications = %d, want 0", n)
	}
}

// TestRedeliveryGateBudgetAndPRStateCache 同一 PR 的多个投递只实时读一次
// State（缓存）；payload/PR 查询共享每轮预算，到顶后不再发查询、剩余投递
// 降级为照常重投（受重投上限约束）。
func TestRedeliveryGateBudgetAndPRStateCache(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	rows := make([]githubapp.HookDelivery, 0, 25)
	for i := 0; i < 25; i++ {
		id := int64(2400 + i)
		rows = append(rows, hookRow(id, fmt.Sprintf("guid-cache-%02d", i),
			"pull_request", "synchronize", 500, false, base))
		env.fake.SetHookDeliveryPayload(id, redeliveryGatePayload("owner/repo", 7))
	}
	env.fake.SetHookDeliveries(rows)
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	counts := env.fake.CallCounts()
	if counts["GetPullRequest"] != 1 {
		t.Fatalf("同一 PR 的实时读 = %d, want 1（repo#pr 缓存）", counts["GetPullRequest"])
	}
	// 闸门查询（详情 + 实时读）各占一个名额，合计不超预算；重投另有自己的上限。
	if queried := counts["GetHookDelivery"] + counts["GetPullRequest"]; queried > reviewRedeliveryMaxPerSweep {
		t.Fatalf("闸门查询合计 = %d, 应不超过每轮预算 %d", queried, reviewRedeliveryMaxPerSweep)
	}
	if got := env.fake.RedeliveredIDs(); len(got) != reviewRedeliveryMaxPerSweep {
		t.Fatalf("重投 = %d, want %d（预算耗尽后降级，仍受重投上限约束）",
			len(got), reviewRedeliveryMaxPerSweep)
	}
}

// TestRedeliveryProbeHonoursItsOwnContext 闸门有自己的时间预算：ctx 已结束时
// 不再发任何请求（直接降级），不能把整轮 90s 预算耗在可选校验上、让重投请求
// 拿到已取消的 ctx。
func TestRedeliveryProbeHonoursItsOwnContext(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(2501, "guid-ctx", "pull_request", "synchronize", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(2501, redeliveryGatePayload("owner/repo", 7))
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	probe := &redeliveryPRProbe{
		hooks:    env.fake,
		client:   env.fake,
		budget:   reviewRedeliveryMaxPerSweep,
		prStates: map[string]*probePRState{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := env.fake.CallCounts()
	check := env.review.redeliveryPRReviewable(ctx, probe, 2501, "pull_request")
	if check.Known {
		t.Fatal("闸门 ctx 已结束时应按未知处理")
	}
	after := env.fake.CallCounts()
	for _, m := range []string{"GetHookDelivery", "GetPullRequest"} {
		if after[m] != before[m] {
			t.Fatalf("闸门预算用完时不应再请求 %s：%d → %d", m, before[m], after[m])
		}
	}
}

// TestRedeliveryGateBudgetExhaustedClosedPRDegrades 闸门查询预算用完后的降级：
// 前 10 个不同 PR 判出关闭（各 2 次查询），第 11 个查不到结果 → 按未知照常
// 重投。这是有意接受的降级（闸门只省白跑，不能因为查不完就漏掉真投递），
// 锁住它避免以后误以为「所有关闭 PR 都能免于重投」。
func TestRedeliveryGateBudgetExhaustedClosedPRDegrades(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	rows := make([]githubapp.HookDelivery, 0, 11)
	for i := 0; i < 11; i++ {
		id := int64(2600 + i)
		rows = append(rows, hookRow(id, fmt.Sprintf("guid-budget-%02d", i),
			"pull_request", "synchronize", 500, false, base))
		env.fake.SetHookDeliveryPayload(id, redeliveryGatePayload("owner/repo", 100+i))
		env.fake.SetPR("owner/repo", 100+i, &githubapi.PullRequest{State: "closed"})
	}
	env.fake.SetHookDeliveries(rows)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 1 || got[0] != 2610 {
		t.Fatalf("redelivered = %v, want [2610]（预算耗尽后的第 11 个按未知降级）", got)
	}
	var closed int64
	env.svc.db.Model(&model.GitHubWebhookRedelivery{}).Where("closed_at IS NOT NULL").Count(&closed)
	if closed != 10 {
		t.Fatalf("closed 行 = %d, want 10（每轮 20 次查询预算 ÷ 每 PR 2 次）", closed)
	}
	var wrongClosed int64
	env.svc.db.Model(&model.GitHubWebhookRedelivery{}).
		Where("guid = ? AND closed_at IS NOT NULL", "guid-budget-10").Count(&wrongClosed)
	if wrongClosed != 0 {
		t.Fatal("第 11 个 guid 不应被记成关闭（没查到实况）")
	}
}

// redeliveryCommentPayload 构造 issue_comment 投递详情的 request.payload：
// PR 号在 issue.number（与 pull_request 的顶层 number 字段名不同），仓库与
// installation 同形。
// redeliveryCommentPayload 组唤醒形态的评论投递 payload（association 可
// 覆盖，默认 OWNER：仓库属主点名）。
func redeliveryCommentPayload(repo string, number int, association ...string) json.RawMessage {
	a := "OWNER"
	if len(association) > 0 {
		a = association[0]
	}
	body, err := json.Marshal(map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       number,
			"pull_request": map[string]any{"url": "https://github.com/" + repo + "/pulls/" + fmt.Sprint(number)},
		},
		"comment":      map[string]any{"body": "@fast-ship-bot 请再审一轮", "author_association": a},
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": 1},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// redeliveryPlainCommentPayload 非唤醒评论：正文未点名机器人（普通讨论
// 评论，webhook 静默忽略，对账不应重投）。
func redeliveryPlainCommentPayload(repo string, number int) json.RawMessage {
	body, err := json.Marshal(map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       number,
			"pull_request": map[string]any{"url": "https://github.com/" + repo + "/pulls/" + fmt.Sprint(number)},
		},
		"comment":      map[string]any{"body": "普通讨论，不需要审查", "author_association": "NONE"},
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": 1},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// TestRedeliveryGateLocatesCommentPayload 评论唤醒投递的闸门定位（INT-179）：
// payload 按 issue.number 定位 repo/PR——PR 仍 open 照常重投；已关闭则记终
// 态静默跳过（评论唤醒重投关 PR 只会白跑）。
func TestRedeliveryGateLocatesCommentPayload(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(3101, "guid-cw-open", "issue_comment", "created", 500, false, base),
		hookRow(3102, "guid-cw-closed", "issue_comment", "created", 502, false, base),
		hookRow(3103, "guid-cw-plain", "issue_comment", "created", 500, false, base),
		hookRow(3104, "guid-cw-fork", "issue_comment", "created", 500, false, base),
		hookRow(3105, "guid-cw-unauth", "issue_comment", "created", 500, false, base),
		hookRow(3106, "guid-cw-auth", "issue_comment", "created", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(3101, redeliveryCommentPayload("owner/repo", 7))
	env.fake.SetHookDeliveryPayload(3102, redeliveryCommentPayload("owner/repo", 8))
	// 未点名机器人的普通讨论评论：webhook 会静默忽略，对账不重投、记终态。
	env.fake.SetHookDeliveryPayload(3103, redeliveryPlainCommentPayload("owner/repo", 7))
	// fork PR 的唤醒投递：守卫必拒，重投只会白跑，记终态跳过。
	env.fake.SetHookDeliveryPayload(3104, redeliveryCommentPayload("owner/repo", 9))
	// 同 PR 两条投递：无资格（NONE）在前、属主（OWNER）在后——
	// 身份判定按投递现算，不得污染 prStates 缓存连累后者（codex 第 13 轮）。
	env.fake.SetHookDeliveryPayload(3105, redeliveryCommentPayload("owner/repo", 10, "NONE"))
	env.fake.SetHookDeliveryPayload(3106, redeliveryCommentPayload("owner/repo", 10, "OWNER"))
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})
	env.fake.SetPR("owner/repo", 8, &githubapi.PullRequest{State: "closed"})
	env.fake.SetPR("owner/repo", 9, &githubapi.PullRequest{HeadRepoFullName: "someone-else/repo"})
	env.fake.SetPR("owner/repo", 10, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 2 || (got[0] != 3101 && got[0] != 3106) || (got[1] != 3101 && got[1] != 3106) {
		t.Fatalf("redelivered = %v, want {3101 3106}（open PR 的评论投递照常重投；无资格/ fork/非唤醒不投）", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-closed"); !ok || row.ClosedAt == nil {
		t.Fatalf("已关闭 PR 的评论投递应记终态: %+v (ok=%v)", row, ok)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-plain"); !ok || row.ClosedAt == nil {
		t.Fatalf("非唤醒评论投递应记终态跳过: %+v (ok=%v)", row, ok)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-fork"); !ok || row.ClosedAt == nil {
		t.Fatalf("fork PR 的唤醒投递应记终态跳过: %+v (ok=%v)", row, ok)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-unauth"); !ok || row.ClosedAt == nil {
		t.Fatalf("无资格评论投递应记终态跳过: %+v (ok=%v)", row, ok)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-auth"); ok && row.ClosedAt != nil {
		t.Fatalf("有资格评论投递不应记终态: %+v", row)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-open"); ok && row.ClosedAt != nil {
		t.Fatalf("open PR 的评论投递不应记终态: %+v", row)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("notifications = %d, want 0", n)
	}
}

// 唤醒串读取降级时评论投递按未知重试（codex 第 23 轮 P2）：slug-only 的
// 失败投递恰是因读取故障失败、最需要重试的——字面量集合会把它们误判成
// 非唤醒记终态，永不重试（与降级「宁多投不漏投」自相矛盾）。
func TestRedeliveryGateSlugOnlyRetryableWhenMentionsDegraded(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	env.review.appSource = stubAppSourceErr{}

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(4101, "guid-cw-slug", "issue_comment", "created", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(4101, func() json.RawMessage {
		body, err := json.Marshal(map[string]any{
			"action": "created",
			"issue": map[string]any{
				"number":       7,
				"pull_request": map[string]any{"url": "https://github.com/owner/repo/pulls/7"},
			},
			"comment":      map[string]any{"body": "@fast-ship-demo 请再审一轮", "author_association": "OWNER"},
			"repository":   map[string]any{"full_name": "owner/repo"},
			"installation": map[string]any{"id": 1},
		})
		if err != nil {
			panic(err)
		}
		return body
	}())
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 1 || got[0] != 4101 {
		t.Fatalf("降级时 slug-only 评论投递应按未知照常重投: %v", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-slug"); ok && row.ClosedAt != nil {
		t.Fatalf("降级时不得记终态: %+v", row)
	}
}

// fork 判定用实拉 base 仓库名而非 payload 仓库名（fast-ship-bot 第 24 轮
// P2）：payload 是投递时刻的旧快照，仓库改名/转移后拿它当同仓基准会把
// 改名窗口内的同仓 PR 误判成跨仓 fork 记终态、永不重投。
func TestRedeliveryGateRenamedRepoNotFork(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(5101, "guid-cw-renamed", "issue_comment", "created", 500, false, base),
	})
	// payload 记着旧仓库名（改名前的投递快照）；实拉 PR 的 base 与 head
	// 都已是新名——同仓，不是 fork。
	env.fake.SetHookDeliveryPayload(5101, redeliveryCommentPayload("owner/repo", 12))
	env.fake.SetPR("owner/repo", 12, &githubapi.PullRequest{
		RepoFullName: "owner/repo-renamed", HeadRepoFullName: "owner/repo-renamed",
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 1 || got[0] != 5101 {
		t.Fatalf("改名后的同仓 PR 投递应照常重投: %v", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-renamed"); ok && row.ClosedAt != nil {
		t.Fatalf("同仓 PR 不得按 fork 记终态: %+v", row)
	}
}

// 投递 payload 解析失败 ≠ 非唤醒（fast-ship-bot 第 29 轮 P2）：空/损坏的
// request.payload 按未知降级重投（与 pull_request 分支同口径），不得把真
// 实唤醒投递记终态永不重试；解析成功的守卫拒绝才是非唤醒终态。
func TestRedeliveryGateCorruptPayloadDegrades(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(6101, "guid-cw-corrupt", "issue_comment", "created", 500, false, base),
		hookRow(6102, "guid-cw-plain2", "issue_comment", "created", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(6101, json.RawMessage("{oops 不是合法 JSON"))
	env.fake.SetHookDeliveryPayload(6102, redeliveryPlainCommentPayload("owner/repo", 7))
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 1 || got[0] != 6101 {
		t.Fatalf("损坏 payload 应降级重投: %v", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-corrupt"); ok && row.ClosedAt != nil {
		t.Fatalf("损坏 payload 不得记终态: %+v", row)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-plain2"); !ok || row.ClosedAt == nil {
		t.Fatalf("守卫拒绝（非唤醒）仍应记终态: %+v (ok=%v)", row, ok)
	}
}

// 草稿 PR 的投递不重投（codex 第 34 轮 P2）：触发面与评论唤醒事务都会
// 丢弃草稿——草稿期间重投只会白吃预算、误发故障通知；记终态跳过。
func TestRedeliveryGateDraftPRSkipped(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(7101, "guid-cw-draft", "issue_comment", "created", 500, false, base),
		hookRow(7102, "guid-pr-draft", "pull_request", "synchronize", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(7101, redeliveryCommentPayload("owner/repo", 13))
	env.fake.SetHookDeliveryPayload(7102, redeliveryGatePayload("owner/repo", 13))
	env.fake.SetPR("owner/repo", 13, &githubapi.PullRequest{Draft: true})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("草稿 PR 的投递不得重投: %v", got)
	}
	for _, guid := range []string{"guid-cw-draft", "guid-pr-draft"} {
		if row, ok := redeliveryClosedRow(t, env, guid); !ok || row.ClosedAt == nil {
			t.Fatalf("%s 应记终态: %+v (ok=%v)", guid, row, ok)
		}
	}
}

// 唤醒串来自缓存时 slug-only 评论投递按未知重试（fast-ship-bot 第 46 轮
// P2）：缓存窗口内 slug 可能过期（部署切换/重建 App），判终态会永久丢掉
// 合法唤醒——宁多投不漏投。
func TestRedeliveryGateCachedMentionsRetrySlugOnly(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	// 部署已切换到新句柄：缓存窗口内还是旧句柄，payload 用新句柄——只有
	// 走到 fresh 复核（刷新后命中新句柄）才会被捡回重投。
	env.review.appSource = stubAppSource{cfg: GitHubAppConfig{AppSlug: "Fresh-Slug"}}

	// 先走一次唤醒判定把旧 slug 写进缓存（5 分钟窗口）。
	if _, _, err := env.review.commentWakeMentions(context.Background()); err != nil {
		t.Fatalf("预热缓存: %v", err)
	}

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(8101, "guid-cw-slug-cached", "issue_comment", "created", 500, false, base),
		hookRow(8102, "guid-cw-slug-miss", "issue_comment", "created", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(8101, func() json.RawMessage {
		body, err := json.Marshal(map[string]any{
			"action": "created",
			"issue": map[string]any{
				"number":       7,
				"pull_request": map[string]any{"url": "https://github.com/owner/repo/pulls/7"},
			},
			"comment":      map[string]any{"body": "@fresh-slug 请再审一轮", "author_association": "OWNER"},
			"repository":   map[string]any{"full_name": "owner/repo"},
			"installation": map[string]any{"id": 1},
		})
		if err != nil {
			panic(err)
		}
		return body
	}())
	// fresh 复核仍不命中（@unknown-x 不在刷新后的集合里）：确定的非唤醒，
	// 记终态。
	env.fake.SetHookDeliveryPayload(8102, func() json.RawMessage {
		body, err := json.Marshal(map[string]any{
			"action": "created",
			"issue": map[string]any{
				"number":       7,
				"pull_request": map[string]any{"url": "https://github.com/owner/repo/pulls/7"},
			},
			"comment":      map[string]any{"body": "@unknown-x 帮忙看下", "author_association": "OWNER"},
			"repository":   map[string]any{"full_name": "owner/repo"},
			"installation": map[string]any{"id": 1},
		})
		if err != nil {
			panic(err)
		}
		return body
	}())
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 1 || got[0] != 8101 {
		t.Fatalf("缓存窗口内 slug-only 投递应经 fresh 复核捡回重投: %v", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-slug-cached"); ok && row.ClosedAt != nil {
		t.Fatalf("缓存窗口内不得记终态: %+v", row)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-slug-miss"); !ok || row.ClosedAt == nil {
		t.Fatalf("fresh 复核仍不命中应记终态: %+v (ok=%v)", row, ok)
	}
}

// 降级/缓存窗口下正文不含 @ 的评论仍是非唤醒（codex 第 48 轮 P2）：无
// @ 不可能含任何唤醒串，记终态跳过——不白吃重投预算、不制造误导通知；
// 含 @ 的未命中才按未知重试（前一条缓存用例）。
func TestRedeliveryGateDegradedNoAtSignTerminal(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	env.review.appSource = stubAppSourceErr{}

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(9101, "guid-cw-noat", "issue_comment", "created", 500, false, base),
		hookRow(9102, "guid-cw-at", "issue_comment", "created", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(9101, redeliveryPlainCommentPayload("owner/repo", 7))
	env.fake.SetHookDeliveryPayload(9102, func() json.RawMessage {
		body, err := json.Marshal(map[string]any{
			"action": "created",
			"issue": map[string]any{
				"number":       7,
				"pull_request": map[string]any{"url": "https://github.com/owner/repo/pulls/7"},
			},
			"comment":      map[string]any{"body": "@someone-else 帮忙看下", "author_association": "NONE"},
			"repository":   map[string]any{"full_name": "owner/repo"},
			"installation": map[string]any{"id": 1},
		})
		if err != nil {
			panic(err)
		}
		return body
	}())
	env.fake.SetPR("owner/repo", 7, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 1 || got[0] != 9102 {
		t.Fatalf("降级下含 @ 的未命中应按未知重投: %v", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-noat"); !ok || row.ClosedAt == nil {
		t.Fatalf("不含 @ 的普通评论应记终态: %+v (ok=%v)", row, ok)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-cw-at"); ok && row.ClosedAt != nil {
		t.Fatalf("含 @ 的未命中不得记终态: %+v", row)
	}
}

// 负向 PR 状态不进缓存（codex 第 50 轮 P2）：同一 sweep 内 PR 可能转为可
// 审（ready_for_review/reopened 投递紧随草稿期的投递），缓存的 usable=
// false 会把后到投递误记终态永不重试——负向按投递现查。
func TestRedeliveryGateNegativeStateNotCached(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(10101, "guid-pr-draftfirst", "pull_request", "synchronize", 500, false, base),
		hookRow(10102, "guid-pr-ready", "pull_request", "synchronize", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(10101, redeliveryGatePayload("owner/repo", 14))
	env.fake.SetHookDeliveryPayload(10102, redeliveryGatePayload("owner/repo", 14))
	// 第一次探测读到草稿；第二次（同 sweep 内后到投递）读到已转正式。
	env.fake.SetPR("owner/repo", 14, &githubapi.PullRequest{Draft: true})
	calls := 0
	env.fake.SetPRHook(func(call int) {
		calls = call
		if call >= 2 {
			env.fake.SetPR("owner/repo", 14, &githubapi.PullRequest{})
		}
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	got := env.fake.RedeliveredIDs()
	if len(got) != 1 || got[0] != 10102 {
		t.Fatalf("后到的已转正式投递应照常重投: %v", got)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-pr-draftfirst"); !ok || row.ClosedAt == nil {
		t.Fatalf("草稿期投递应记终态: %+v (ok=%v)", row, ok)
	}
	if row, ok := redeliveryClosedRow(t, env, "guid-pr-ready"); ok && row.ClosedAt != nil {
		t.Fatalf("已转正式投递不得因缓存误记终态: %+v", row)
	}
	if calls < 2 {
		t.Fatalf("负向结果不得走缓存，应现查: calls = %d", calls)
	}
}
