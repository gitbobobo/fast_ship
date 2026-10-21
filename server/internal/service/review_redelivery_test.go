package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
)

// INT-146 投递失败对账测试：Fake 里种投递记录，断言只重投该重投的 guid、
// 上限与节流、无启用项目整步跳过、非 pull_request 事件不重投。
// 固定 now 取 2026 年（远早于 Fake 追加行用的真实 time.Now()），保证追加行
// 一定落在回看窗口（now-24h 之后）内。

var redeliveryTestNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func hookRow(id int64, guid, event, action string, code int, redelivery bool, at time.Time) githubapp.HookDelivery {
	return githubapp.HookDelivery{
		ID: id, GUID: guid, Event: event, Action: action,
		StatusCode: code, Redelivery: redelivery, DeliveredAt: at,
	}
}

func countNotifications(t *testing.T, env *reviewEngineEnv, kind model.NotificationKind) int64 {
	t.Helper()
	var n int64
	env.svc.db.Model(&model.Notification{}).
		Where("kind = ? AND user_id = ?", kind, env.user.ID).Count(&n)
	return n
}

// TestRedeliverOnlyWhatNeedsIt 三个失败 guid：(a) 本地幂等账已登记（已送达）、
// (b) 有成功的重投行、(c) 干净失败——只 (c) 被重投；6 分钟后再次对账，(c)
// 已被 Fake 追加一条失败重投行，仍在上限内继续重投。
func TestRedeliverOnlyWhatNeedsIt(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(101, "guid-a", "pull_request", "synchronize", 502, false, base),
		// (b)：原始失败 + 一次成功重投 → 已送达。
		hookRow(102, "guid-b", "pull_request", "synchronize", 502, false, base),
		hookRow(103, "guid-b", "pull_request", "synchronize", 200, true, base.Add(time.Minute)),
		// (c)：干净失败。
		hookRow(104, "guid-c", "pull_request", "synchronize", 500, false, base),
	})
	if err := env.svc.db.Create(&model.GitHubWebhookDelivery{
		DeliveryID: "guid-a", Event: "pull_request", ReceivedAt: base,
	}).Error; err != nil {
		t.Fatalf("seed delivery ledger: %v", err)
	}

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 1 || got[0] != 104 {
		t.Fatalf("redelivered = %v, want [104]", got)
	}

	// 6 分钟后：guid-c 已有一条失败重投行（Attempts=1 < 3），继续重投。
	env.review.reconcileWebhookDeliveries(redeliveryTestNow.Add(6 * time.Minute))
	if got := env.fake.RedeliveredIDs(); len(got) != 2 || got[0] != 104 || got[1] != 104 {
		t.Fatalf("redelivered = %v, want [104 104]", got)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("上限内不应发通知，实际 %d 条", n)
	}
}

// TestRedeliveryExhaustedNotifiesOnce 同一 guid 已重投 3 次仍失败：不再重投、
// 发一条通知；再次对账 dedup 生效仍是 1 条。
func TestRedeliveryExhaustedNotifiesOnce(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-2 * time.Hour)
	rows := []githubapp.HookDelivery{
		hookRow(201, "guid-x", "pull_request", "synchronize", 502, false, base),
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, hookRow(int64(202+i), "guid-x", "pull_request", "synchronize", 500, true,
			base.Add(time.Duration(i+1)*time.Minute)))
	}
	env.fake.SetHookDeliveries(rows)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("超上限不应重投，实际 %v", got)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 1 {
		t.Fatalf("notifications = %d, want 1", n)
	}

	env.review.reconcileWebhookDeliveries(redeliveryTestNow.Add(6 * time.Minute))
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 1 {
		t.Fatalf("重复对账后 notifications = %d, want 1（dedup）", n)
	}
}

// TestRedeliverySkippedWithoutEnabledReview 没有启用审查的项目时整步跳过，
// 连投递列表都不拉。
func TestRedeliverySkippedWithoutEnabledReview(t *testing.T) {
	env := newReviewEngineEnv(t) // 不 enableReview
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(301, "guid-z", "pull_request", "synchronize", 500, false,
			redeliveryTestNow.Add(-time.Hour)),
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if counts := env.fake.CallCounts(); counts["ListHookDeliveries"] != 0 {
		t.Fatalf("无启用项目不应拉投递列表，实际 %v", counts)
	}
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("redelivered = %v, want 空", got)
	}
}

// TestRedeliveryThrottled 同一 tick 窗口内连续两次对账只执行第一次。
func TestRedeliveryThrottled(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(401, "guid-t", "pull_request", "synchronize", 500, false,
			redeliveryTestNow.Add(-time.Hour)),
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	env.review.reconcileWebhookDeliveries(redeliveryTestNow.Add(time.Second))
	if got := env.fake.RedeliveredIDs(); len(got) != 1 {
		t.Fatalf("redelivered = %v, want 只重投一次", got)
	}
}

// TestRedeliveryIgnoresNonPullRequest 非 pull_request 事件的失败投递不重投。
func TestRedeliveryIgnoresNonPullRequest(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(501, "guid-i", "issues", "opened", 500, false,
			redeliveryTestNow.Add(-time.Hour)),
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("非 pull_request 不应重投，实际 %v", got)
	}
}

// TestRedeliveryExhaustedWithLocalLedgerNotNotified guid 已重投 3 次超上限，
// 但本地幂等账已登记（某次重投实际已送达，只是 GitHub 侧记录滞后）：先过
// 幂等账再判超限——零重投、零通知，不误报故障。
func TestRedeliveryExhaustedWithLocalLedgerNotNotified(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-2 * time.Hour)
	rows := []githubapp.HookDelivery{
		hookRow(601, "guid-ledger", "pull_request", "synchronize", 502, false, base),
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, hookRow(int64(602+i), "guid-ledger", "pull_request", "synchronize", 500, true,
			base.Add(time.Duration(i+1)*time.Minute)))
	}
	env.fake.SetHookDeliveries(rows)
	if err := env.svc.db.Create(&model.GitHubWebhookDelivery{
		DeliveryID: "guid-ledger", Event: "pull_request", ReceivedAt: base,
	}).Error; err != nil {
		t.Fatalf("seed delivery ledger: %v", err)
	}

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("幂等账已登记不应重投，实际 %v", got)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("已送达的 guid 不应发故障通知，实际 %d 条", n)
	}
}

// TestRedeliveryOnlyRowsSkipped 窗口内只剩重投行、没有原始行的 guid（原始行已
// 滑出 24h 窗口）：重投次数计数不完整，整组放弃——零重投、零通知。
func TestRedeliveryOnlyRowsSkipped(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	rows := []githubapp.HookDelivery{}
	// 3 条失败重投行：没有 HasOriginal 的话会撞超限通知路径。
	for i := 0; i < 3; i++ {
		rows = append(rows, hookRow(int64(701+i), "guid-orphan", "pull_request", "synchronize", 500, true,
			redeliveryTestNow.Add(-time.Duration(i+1)*time.Minute)))
	}
	env.fake.SetHookDeliveries(rows)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("无原始行的 guid 不应重投，实际 %v", got)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("无原始行的 guid 不应发通知，实际 %d 条", n)
	}
}

// TestGroupHookDeliveriesSkipsClosedAction closed 的失败投递不进分组：重投它
// 会无条件取消该 PR 当前有效的审查运行（webhook closed 分支先于时间戳判定），
// 有意不重投。
func TestGroupHookDeliveriesSkipsClosedAction(t *testing.T) {
	base := redeliveryTestNow.Add(-time.Hour)
	out := groupHookDeliveries([]githubapp.HookDelivery{
		hookRow(801, "guid-closed", "pull_request", "closed", 500, false, base),
		hookRow(802, "guid-open", "pull_request", "opened", 500, false, base),
	})
	if len(out) != 1 {
		t.Fatalf("states = %d 组, want 1（closed 不入组）", len(out))
	}
	if out[0].GUID != "guid-open" {
		t.Fatalf("留下的组 = %s, want guid-open", out[0].GUID)
	}
	if out[0].TargetID != 802 {
		t.Fatalf("TargetID = %d, want 802", out[0].TargetID)
	}
}

// TestRedeliveryPerSweepCap 单轮重投请求有上限：25 个到期 guid 只发 20 个
// 重投，剩余留到下一轮，不突发几百个 POST。
func TestRedeliveryPerSweepCap(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	rows := make([]githubapp.HookDelivery, 0, 25)
	for i := 0; i < 25; i++ {
		rows = append(rows, hookRow(int64(900+i), fmt.Sprintf("guid-cap-%02d", i),
			"pull_request", "synchronize", 500, false, base))
	}
	env.fake.SetHookDeliveries(rows)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 20 {
		t.Fatalf("单轮重投 = %d, want 20（reviewRedeliveryMaxPerSweep）", len(got))
	}
}

// TestRedeliveryStopsOnRateLimit 重投被限流（403/429）：GitHub 要求停止请求
// 退避——第一个 guid 就 429 的话只发一次请求，后续不再发，零重投成功。
func TestRedeliveryStopsOnRateLimit(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	rows := make([]githubapp.HookDelivery, 0, 3)
	for i := 0; i < 3; i++ {
		rows = append(rows, hookRow(int64(950+i), fmt.Sprintf("guid-rl-%d", i),
			"pull_request", "synchronize", 500, false, base))
	}
	env.fake.SetHookDeliveries(rows)
	env.fake.SetRedeliverError(&githubapi.StatusError{
		StatusCode: 429, Method: "POST", URL: "/app/hook/deliveries/950/attempts",
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	counts := env.fake.CallCounts()
	if counts["RedeliverHookDelivery"] != 1 {
		t.Fatalf("限流后应只试一次，实际 %v", counts)
	}
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("限流重投不应记成功，实际 %v", got)
	}
}
