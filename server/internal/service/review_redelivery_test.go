package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"gorm.io/gorm"
)

// INT-146 投递失败对账测试：Fake 里种投递记录，断言只重投该重投的 guid、
// 重投次数账（github_webhook_redeliveries）跨 24h 窗口累计、上限与去重、
// 无启用项目整步跳过、非 pull_request 事件不重投。
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

// seedRedeliveryLedger 直接落一行重投次数账（模拟此前对账轮已记下的次数）。
func seedRedeliveryLedger(t *testing.T, env *reviewEngineEnv, guid string, attempts int) {
	t.Helper()
	if err := env.svc.db.Create(&model.GitHubWebhookRedelivery{
		GUID: guid, Attempts: attempts,
		CreatedAt: redeliveryTestNow, UpdatedAt: redeliveryTestNow,
	}).Error; err != nil {
		t.Fatalf("seed redelivery ledger: %v", err)
	}
}

// redeliveryLedgerAttempts 查某 guid 的重投次数账：无行返回 (0, false, nil)。
// 不做断言——轮询等待的 goroutine 里不能 FailNow。
func redeliveryLedgerAttempts(env *reviewEngineEnv, guid string) (int, bool, error) {
	var row model.GitHubWebhookRedelivery
	err := env.svc.db.Where("guid = ?", guid).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return row.Attempts, true, nil
}

func mustLedgerAttempts(t *testing.T, env *reviewEngineEnv, guid string) int {
	t.Helper()
	n, ok, err := redeliveryLedgerAttempts(env, guid)
	if err != nil {
		t.Fatalf("查询重投次数账: %v", err)
	}
	if !ok {
		t.Fatalf("重投次数账无 %s 行", guid)
	}
	return n
}

// TestRedeliverOnlyWhatNeedsIt 三个失败 guid：(a) 本地幂等账已登记（已送达）、
// (b) 有成功的重投行、(c) 干净失败——只 (c) 被重投；成功重投后次数账记 1、
// 已送达的 guid-a 不记数。6 分钟后再次对账，(c) 已被 Fake 追加一条失败重投
// 行（列表与账都是 1 < 3），继续重投且账累加到 2。
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
	if n := mustLedgerAttempts(t, env, "guid-c"); n != 1 {
		t.Fatalf("guid-c 重投次数账 = %d, want 1（成功重投后记账）", n)
	}
	if _, ok, _ := redeliveryLedgerAttempts(env, "guid-a"); ok {
		t.Fatalf("guid-a 已送达，不应记重投次数")
	}

	// 6 分钟后：guid-c 已有一条失败重投行（attempts=1 < 3），继续重投。
	env.review.reconcileWebhookDeliveries(redeliveryTestNow.Add(6 * time.Minute))
	if got := env.fake.RedeliveredIDs(); len(got) != 2 || got[0] != 104 || got[1] != 104 {
		t.Fatalf("redelivered = %v, want [104 104]", got)
	}
	if n := mustLedgerAttempts(t, env, "guid-c"); n != 2 {
		t.Fatalf("guid-c 重投次数账 = %d, want 2（第二次重投后累加）", n)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("上限内不应发通知，实际 %d 条", n)
	}
}

// TestRedeliveryExhaustedNotifiesOnce 重投次数账记到 3（此时列表里可以只剩
// 原始失败行——跨窗口的总次数在账里）：不再重投、发一条通知且正文写实际
// 次数；再次对账 dedup 生效仍是 1 条。
func TestRedeliveryExhaustedNotifiesOnce(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-2 * time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(201, "guid-x", "pull_request", "synchronize", 502, false, base),
	})
	seedRedeliveryLedger(t, env, "guid-x", 3)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("超上限不应重投，实际 %v", got)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 1 {
		t.Fatalf("notifications = %d, want 1", n)
	}
	var notif model.Notification
	if err := env.svc.db.Model(&model.Notification{}).
		Where("kind = ? AND user_id = ?", model.NotificationReviewRedeliveryFailed, env.user.ID).
		First(&notif).Error; err != nil {
		t.Fatalf("查询重投失败通知: %v", err)
	}
	if !containsInt(notif.Body, 3) {
		t.Fatalf("通知正文应写实际重投次数 3，实际 %q", notif.Body)
	}

	env.review.reconcileWebhookDeliveries(redeliveryTestNow.Add(6 * time.Minute))
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 1 {
		t.Fatalf("重复对账后 notifications = %d, want 1（dedup）", n)
	}
}

// containsInt 正文里出现「N 次」形态的实际次数（不依赖整句措辞）。
func containsInt(body string, n int) bool {
	return len(body) > 0 && strings.Contains(body, fmt.Sprintf("%d 次", n))
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

// TestRedeliveryLoopRunsOnceAndStops redeliveryLoop 启动即先跑一轮（进程刚
// 起来时补上停机期间漏掉的投递——以次数账落行为证），cancel 后退出；ticker
// 周期 5 分钟，等待窗口内不会有第二轮。种子行贴真实 now 种：循环用的是
// time.Now()，回看窗口以它起算（其余用例传固定的历史 now）。
func TestRedeliveryLoopRunsOnceAndStops(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(401, "guid-loop", "pull_request", "synchronize", 500, false,
			time.Now().UTC().Add(-time.Hour)),
	})

	cancel := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.review.redeliveryLoop(cancel)
	}()

	waitFor(t, 2*time.Second, func() bool {
		n, ok, err := redeliveryLedgerAttempts(env, "guid-loop")
		return err == nil && ok && n == 1
	})
	close(cancel)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 后 redeliveryLoop 未退出")
	}
	if got := env.fake.RedeliveredIDs(); len(got) != 1 || got[0] != 401 {
		t.Fatalf("redelivered = %v, want [401]（只启动那一轮）", got)
	}
}

// waitFor 轮询等待 cond 成立或超时（等异步启动那一轮的落库效果用）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
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

// TestRedeliveryExhaustedWithLocalLedgerNotNotified guid 超上限，但本地幂等
// 账已登记（某次重投实际已送达，只是 GitHub 侧记录滞后）：先过幂等账再判
// 超限——零重投、零通知、也不记次数账，不误报故障。
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
	if _, ok, _ := redeliveryLedgerAttempts(env, "guid-ledger"); ok {
		t.Fatalf("幂等账已登记的 guid 不应记重投次数")
	}
}

// TestRedeliveryContinuesWhenOriginalOutOfWindow 原始行已滑出 24h 窗口、列表
// 只剩一条失败的重投行（旧实现按 HasOriginal 整组放弃）：跨窗口的总次数在
// 次数账里记着 1——仍会继续重投，账累加到 2，不发通知。
func TestRedeliveryContinuesWhenOriginalOutOfWindow(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(701, "guid-orphan", "pull_request", "synchronize", 500, true,
			redeliveryTestNow.Add(-time.Minute)),
	})
	seedRedeliveryLedger(t, env, "guid-orphan", 1)

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if got := env.fake.RedeliveredIDs(); len(got) != 1 || got[0] != 701 {
		t.Fatalf("redelivered = %v, want [701]", got)
	}
	if n := mustLedgerAttempts(t, env, "guid-orphan"); n != 2 {
		t.Fatalf("guid-orphan 重投次数账 = %d, want 2", n)
	}
	if n := countNotifications(t, env, model.NotificationReviewRedeliveryFailed); n != 0 {
		t.Fatalf("上限内不应发通知，实际 %d 条", n)
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
// 退避——第一个 guid 就 429 的话只发一次请求，后续不再发，零重投成功；
// 被拒的重投不记次数账（GitHub 没产生投递记录，下一轮会再试）。
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
	var ledgerRows int64
	env.svc.db.Model(&model.GitHubWebhookRedelivery{}).Count(&ledgerRows)
	if ledgerRows != 0 {
		t.Fatalf("被拒的重投不应记次数账，实际 %d 行", ledgerRows)
	}
}

// TestRedeliveryPerSweepCapCountsFailures 单轮上限按「尝试」计：重投接口持续
// 5xx（非 403/429，不触发限流停止）时，25 个到期 guid 也只发 20 次请求——
// 只按成功计数的话上限形同虚设，一轮会打满 GitHub 与本服务的 webhook 入口。
func TestRedeliveryPerSweepCapCountsFailures(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)

	base := redeliveryTestNow.Add(-time.Hour)
	rows := make([]githubapp.HookDelivery, 0, 25)
	for i := 0; i < 25; i++ {
		rows = append(rows, hookRow(int64(960+i), fmt.Sprintf("guid-5xx-%02d", i),
			"pull_request", "synchronize", 500, false, base))
	}
	env.fake.SetHookDeliveries(rows)
	env.fake.SetRedeliverError(&githubapi.StatusError{
		StatusCode: 500, Method: "POST", URL: "/app/hook/deliveries/960/attempts",
	})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	if counts := env.fake.CallCounts(); counts["RedeliverHookDelivery"] != reviewRedeliveryMaxPerSweep {
		t.Fatalf("重投尝试 = %d, want %d（失败也占名额）",
			counts["RedeliverHookDelivery"], reviewRedeliveryMaxPerSweep)
	}
	if got := env.fake.RedeliveredIDs(); len(got) != 0 {
		t.Fatalf("全部被拒不应记成功，实际 %v", got)
	}
}

// TestGroupHookDeliveriesIncludesCommentCreated 评论唤醒投递（issue_comment/
// created，INT-179）纳入对账：同 guid 分组去重、目标取最早失败行、任一行
// 2xx 视为已送达；非 created 的评论投递（edited 等）不入组。
func TestGroupHookDeliveriesIncludesCommentCreated(t *testing.T) {
	base := redeliveryTestNow.Add(-time.Hour)
	out := groupHookDeliveries([]githubapp.HookDelivery{
		// 同 guid 两条失败行：目标取最早一条（602）。
		hookRow(602, "guid-comment", "issue_comment", "created", 502, false, base),
		hookRow(603, "guid-comment", "issue_comment", "created", 500, false, base.Add(time.Minute)),
		// edited/removed 不开轮，不入组。
		hookRow(604, "guid-comment-edited", "issue_comment", "edited", 500, false, base),
		hookRow(605, "guid-comment-removed", "issue_comment", "removed", 500, false, base),
		// 任一行 2xx 即整组已送达，不再重投。
		hookRow(606, "guid-comment-ok", "issue_comment", "created", 200, false, base),
	})
	guids := map[string]hookDeliveryState{}
	for _, st := range out {
		guids[st.GUID] = st
	}
	st, ok := guids["guid-comment"]
	if !ok {
		t.Fatal("issue_comment/created 失败投递应入组")
	}
	if st.Event != "issue_comment" || st.Action != "created" {
		t.Fatalf("st = %s/%s, want issue_comment/created", st.Event, st.Action)
	}
	if st.TargetID != 602 || st.Delivered {
		t.Fatalf("TargetID = %d delivered = %v, want 602/false", st.TargetID, st.Delivered)
	}
	for _, guid := range []string{"guid-comment-edited", "guid-comment-removed"} {
		if _, ok := guids[guid]; ok {
			t.Fatalf("%s 不应入组（非 created 不开轮）", guid)
		}
	}
	if st, ok := guids["guid-comment-ok"]; !ok || !st.Delivered {
		t.Fatalf("guid-comment-ok 应入组且已送达: %+v (ok=%v)", st, ok)
	}
}
