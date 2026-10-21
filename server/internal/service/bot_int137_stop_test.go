package service

import (
	"errors"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-137 停止链路：主线「停止」在宽限到期或重复点击时硬取消卡住的模型流；
// 定时任务运行由用户经 REST 停止且不产生主线事件。

// stopMessageSeqOf 取该 run 的停止消息 seq（RequestStop 幂等键 stop:<runID>）。
func stopMessageSeqOf(t *testing.T, db *gorm.DB, botID, runID string) int64 {
	t.Helper()
	var seqs []int64
	if err := db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND client_message_id = ?", botID, "stop:"+runID).
		Pluck("seq", &seqs).Error; err != nil || len(seqs) != 1 {
		t.Fatalf("停止消息应恰一条: %v %v", err, seqs)
	}
	return seqs[0]
}

func runStatusOf(t *testing.T, db *gorm.DB, runID string) (model.BotRunStatus, *time.Time) {
	t.Helper()
	var run model.BotRun
	if err := db.Where("id = ?", runID).First(&run).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	return run.Status, run.FinishedAt
}

func TestBotStopHardStopsAfterGrace(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "长回答", Hold: hold},
		fakeModelTurn{Text: "好的，已停止"},
	)
	env.cfg.Bots.Runtime.StopGraceSeconds = 1
	userID := createBotTestUser(t, env.db, "int137stop")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "开始", ClientMessageID: "hs1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	env.fake.nextRequest(t, 10*time.Second)

	runs, _ := env.bots.ListRuns(userID, bot.ID, 5)
	runID := runs[0].ID
	if _, err := env.bots.RequestStop(userID, bot.ID, runID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 宽限未到：run 仍 running，未置 stop_requested（第一次点击仍是软停止）。
	fresh, err := env.bots.GetRun(userID, bot.ID, runID)
	if err != nil || fresh.Status != string(model.BotRunStatusRunning) || fresh.StopRequested {
		t.Fatalf("宽限未到 run 不应被硬取消: %v %+v", err, fresh)
	}
	// 宽限到期：卡住的模型流被硬中断，run 落 stopped。
	waitForBot(t, "run stopped", 15*time.Second, func() bool {
		r, err := env.bots.GetRun(userID, bot.ID, runID)
		return err == nil && r.Status == string(model.BotRunStatusStopped)
	})

	// 停止消息已被消费：offered 水位推进到停止消息 seq，扫描器不得因它再起新 run。
	stopSeq := stopMessageSeqOf(t, env.db, bot.ID, runID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if botRow.OfferedInputSeq < stopSeq {
		t.Fatalf("停止消息应已消费: offered=%d stop_seq=%d", botRow.OfferedInputSeq, stopSeq)
	}
	env.bots.scanOnce()
	if run := env.activeRunOrNothing(bot.ID); run != nil {
		t.Fatalf("停止消息消费后扫描不得起新 run: %+v", run)
	}
	var runCount int64
	env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&runCount)
	if runCount != 1 {
		t.Fatalf("停止消息不得驱动新 run，实得 %d 条 run", runCount)
	}

	// 之后到达的普通消息照常处理：新 run 跑完（completed），协议块闭合（有
	// assistant 回复）。botSettled 只要求「存在 run 且无活动 run」，旧 run 就
	// 满足，须等第二条 run 出现并收尾后再断言。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "继续", ClientMessageID: "hs2"}); err != nil {
		t.Fatalf("send next: %v", err)
	}
	waitForBot(t, "next run settled", 15*time.Second, func() bool {
		var n int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&n)
		return n >= 2 && env.botSettled(bot.ID)
	})
	var secondRun model.BotRun
	if err := env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC, id DESC").First(&secondRun).Error; err != nil {
		t.Fatalf("load second run: %v", err)
	}
	if secondRun.Status != model.BotRunStatusCompleted {
		t.Fatalf("后续 run 应自然跑完，实得 %s（error=%v %v）", secondRun.Status, secondRun.ErrorCode, secondRun.ErrorMessage)
	}
	var assistants int64
	env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindAssistant).Count(&assistants)
	if assistants == 0 {
		t.Fatalf("后续消息的 run 应有 assistant 回复（协议块闭合）")
	}
}

func TestBotStopRepeatedClickHardStopsImmediately(t *testing.T) {
	hold := make(chan struct{})
	// 默认宽限 15s：重复点击不依赖宽限就必须落 stopped。
	env := setupBotTestEnv(t, fakeModelTurn{Text: "长回答", Hold: hold})
	userID := createBotTestUser(t, env.db, "int137stopdup")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "开始", ClientMessageID: "dup1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	env.fake.nextRequest(t, 10*time.Second)

	runs, _ := env.bots.ListRuns(userID, bot.ID, 5)
	runID := runs[0].ID
	if _, err := env.bots.RequestStop(userID, bot.ID, runID); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if _, err := env.bots.RequestStop(userID, bot.ID, runID); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	stopMessageSeqOf(t, env.db, bot.ID, runID) // 只有第一条停止消息，第二条不得再落
	waitForBot(t, "run stopped immediately", 5*time.Second, func() bool {
		status, finished := runStatusOf(t, env.db, runID)
		return status == model.BotRunStatusStopped && finished != nil
	})
}

func TestBotStopHardCancelDropsDraftAndFailsModelRequest(t *testing.T) {
	// 先流出部分正文再卡住：草稿已挂出，硬取消后必须丢弃且请求记 failed。
	env := setupBotTestEnv(t, fakeModelTurn{Text: "草稿正文", StallAfterText: 30 * time.Second})
	env.cfg.Bots.Runtime.StopGraceSeconds = 1
	userID := createBotTestUser(t, env.db, "int137stopdraft")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "开始", ClientMessageID: "dc1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	env.fake.nextRequest(t, 10*time.Second)

	runs, _ := env.bots.ListRuns(userID, bot.ID, 5)
	runID := runs[0].ID
	if _, err := env.bots.RequestStop(userID, bot.ID, runID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitForBot(t, "run stopped", 15*time.Second, func() bool {
		status, finished := runStatusOf(t, env.db, runID)
		return status == model.BotRunStatusStopped && finished != nil
	})
	var discards int64
	env.db.Model(&model.BotEvent{}).
		Where("bot_id = ? AND run_id = ? AND payload_json LIKE ?", bot.ID, runID, "%message_discard%").
		Count(&discards)
	if discards == 0 {
		t.Fatalf("硬取消后应有 message_discard 事件")
	}
	var failed int64
	env.db.Model(&model.BotModelRequest{}).
		Where("run_id = ? AND status = ?", runID, model.BotModelRequestFailed).Count(&failed)
	if failed == 0 {
		t.Fatalf("被中断的模型请求应记 failed")
	}
}

func TestBotScheduleRunStop(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int137sched")
	bot := env.createBot(t, userID)
	task, err := env.bots.createScheduleTask(bot.ID, "每日汇报", "0 9 * * *", "Asia/Shanghai", "汇报进展", "", false)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	now := time.Now().UTC()
	taskID := task.ID
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerSchedule, TriggerKey: "int137-schedule-stop",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		SessionKind: model.BotSessionKindSchedule, ScheduleTaskID: &taskID,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}

	resp, err := env.bots.StopScheduleRun(userID, bot.ID, task.ID, run.ID)
	if err != nil {
		t.Fatalf("stop schedule run: %v", err)
	}
	if !resp.StopRequested {
		t.Fatalf("响应应带 stop_requested: %+v", resp)
	}
	waitForBot(t, "schedule run stopped", 15*time.Second, func() bool {
		status, finished := runStatusOf(t, env.db, run.ID)
		return status == model.BotRunStatusStopped && finished != nil
	})
	// 用户停止不追加主线事件：没有 schedule_event，也没有 user 停止消息。
	var scheduleEvents, users int64
	env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindScheduleEvent).Count(&scheduleEvents)
	env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindUser).Count(&users)
	if scheduleEvents != 0 || users != 0 {
		t.Fatalf("定时任务停止不得产生主线消息: schedule_event=%d user=%d", scheduleEvents, users)
	}

	// 非归属人 → 404（ErrBotNotFound），归属校验先于一切。
	other := createBotTestUser(t, env.db, "int137sched-other")
	if _, err := env.bots.StopScheduleRun(other, bot.ID, task.ID, run.ID); !errors.Is(err, errs.ErrBotNotFound) {
		t.Fatalf("非归属人应 404，实得 %v", err)
	}
	// run 不属于该 taskId → 404（ErrBotRunNotFound）。
	task2, err := env.bots.createScheduleTask(bot.ID, "另一任务", "0 10 * * *", "Asia/Shanghai", "别的指令", "", false)
	if err != nil {
		t.Fatalf("create task2: %v", err)
	}
	if _, err := env.bots.StopScheduleRun(userID, bot.ID, task2.ID, run.ID); !errors.Is(err, errs.ErrBotRunNotFound) {
		t.Fatalf("run 与任务不符应 404，实得 %v", err)
	}
	// 已终态再停 → 409。
	if _, err := env.bots.StopScheduleRun(userID, bot.ID, task.ID, run.ID); !errors.Is(err, errs.ErrBotRunStateConflict) {
		t.Fatalf("已终态 run 应 409，实得 %v", err)
	}
}
