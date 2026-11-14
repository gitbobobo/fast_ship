package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
)

// int227SQLRecorder 抓取 gorm 执行的 SQL 文本，供扫描查询形状断言使用。
type int227SQLRecorder struct {
	mu    sync.Mutex
	stmts []string
}

func (r *int227SQLRecorder) LogMode(logger.LogLevel) logger.Interface      { return r }
func (r *int227SQLRecorder) Info(context.Context, string, ...interface{})  {}
func (r *int227SQLRecorder) Warn(context.Context, string, ...interface{})  {}
func (r *int227SQLRecorder) Error(context.Context, string, ...interface{}) {}
func (r *int227SQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	if err != nil {
		return
	}
	sql, _ := fc()
	r.mu.Lock()
	r.stmts = append(r.stmts, sql)
	r.mu.Unlock()
}

func (r *int227SQLRecorder) reset() {
	r.mu.Lock()
	r.stmts = nil
	r.mu.Unlock()
}

func (r *int227SQLRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stmts...)
}

// scanQueryStmts 挑出未消费输入扫描那条 SQL（bots 表 + EXISTS 子查询）。
func scanQueryStmts(stmts []string) []string {
	var out []string
	for _, sql := range stmts {
		lower := strings.ToLower(sql)
		if strings.Contains(lower, "from `bots`") && strings.Contains(lower, "exists") {
			out = append(out, sql)
		}
	}
	return out
}

// INT-227：5 秒空闲扫描改成一条按真实未消费输入筛选的 SQL——完成过纯文本
// 回复、没有新输入的 bot 不再被整行选出并逐个 COUNT；查询数不随闲置 bot
// 数线性增长；真正有未消费输入时仍照常唤醒。
func TestBotScanOnceIdleBotsCostOneQuery(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "扫描唤醒回复"})
	userID := createBotTestUser(t, env.db, "botint227scan")
	msgs := repository.NewBotMessageRepository(env.db)

	appendMsg := func(botID string, kind model.BotMessageKind) int64 {
		t.Helper()
		var seq int64
		if err := env.db.Transaction(func(tx *gorm.DB) error {
			s, err := env.bots.bots.NextMessageSeqTx(tx, botID)
			if err != nil {
				return err
			}
			seq = s
			return msgs.AppendTx(tx, &model.BotMessage{
				ID: uuid.NewString(), BotID: botID, Seq: s,
				Kind: kind, ContentJSON: `{}`, CreatedAt: time.Now().UTC(),
			})
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
		return seq
	}
	markReplied := func(botID string) {
		t.Helper()
		seq := appendMsg(botID, model.BotMessageKindUser)
		appendMsg(botID, model.BotMessageKindAssistant)
		if err := env.db.Model(&model.Bot{}).Where("id = ?", botID).
			Update("offered_input_seq", seq).Error; err != nil {
			t.Fatalf("consume input: %v", err)
		}
	}

	// 先落 3 个已完成回复、水位落后的空闲 bot。
	bots := make([]*BotResponse, 0, 7)
	for i := 0; i < 3; i++ {
		bot := env.createBot(t, userID)
		markReplied(bot.ID)
		bots = append(bots, bot)
	}

	rec := &int227SQLRecorder{}
	env.db.Logger = rec
	rec.reset()
	env.bots.scanOnce()
	allFirst := rec.all()
	scanStmts := scanQueryStmts(allFirst)
	if len(scanStmts) != 1 {
		t.Fatalf("3 个空闲 bot 的扫描应恰好一条 SQL，实得 %d 条: %v", len(scanStmts), scanStmts)
	}
	lower := strings.ToLower(scanStmts[0])
	if strings.Contains(lower, "persona_prompt") || strings.Contains(lower, "system_prompt_text") {
		t.Fatalf("扫描不得加载人设大字段: %s", scanStmts[0])
	}
	if strings.Contains(lower, "count(") {
		t.Fatalf("扫描不得逐 bot COUNT: %s", scanStmts[0])
	}
	// 全量 SQL 里也不得有对 bot_messages 的 COUNT（防新查询旁边叠加旧路径）。
	for _, sql := range allFirst {
		l := strings.ToLower(sql)
		if strings.Contains(l, "bot_messages") && strings.Contains(l, "count(") {
			t.Fatalf("扫描不得对 bot_messages 做 COUNT: %s", sql)
		}
	}

	// 再落 4 个同样的空闲 bot：查询数不变，不随闲置 bot 数线性增长。
	for i := 0; i < 4; i++ {
		bot := env.createBot(t, userID)
		markReplied(bot.ID)
		bots = append(bots, bot)
	}
	rec.reset()
	env.bots.scanOnce()
	allSecond := rec.all()
	if got := scanQueryStmts(allSecond); len(got) != 1 {
		t.Fatalf("7 个空闲 bot 的扫描仍应恰好一条 SQL，实得 %d 条: %v", len(got), got)
	}
	// 完整查询数也不随闲置 bot 数增长（防其他扫描子路径偷偷随 bot 数线性化）。
	if len(allSecond) != len(allFirst) {
		t.Fatalf("扫描总查询数不应随闲置 bot 数变化: 3 bot=%d 条，7 bot=%d 条\nfirst=%v\nsecond=%v",
			len(allFirst), len(allSecond), allFirst, allSecond)
	}

	// 空闲 bot 不得被扫描唤醒。
	for _, bot := range bots {
		if run := env.activeRunOrNothing(bot.ID); run != nil {
			t.Fatalf("无未消费输入的 bot 不得被扫描唤醒: bot=%s run=%+v", bot.ID, run)
		}
		var runCount int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&runCount)
		if runCount != 0 {
			t.Fatalf("空闲 bot 不得产生 run: bot=%s runs=%d", bot.ID, runCount)
		}
	}

	// 真正有未消费输入时仍会唤醒：扫描起 run、跑完回复、推进输入水位。
	target := bots[0]
	newSeq := appendMsg(target.ID, model.BotMessageKindUser)
	env.bots.scanOnce()
	waitForBot(t, "扫描唤醒的 run 完成", 15*time.Second, func() bool {
		var n int64
		env.db.Model(&model.BotRun{}).
			Where("bot_id = ? AND status = ?", target.ID, model.BotRunStatusCompleted).
			Count(&n)
		return n >= 1
	})
	fresh, err := env.bots.bots.Get(target.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.OfferedInputSeq < newSeq {
		t.Fatalf("唤醒后输入水位应推进: offered=%d new=%d", fresh.OfferedInputSeq, newSeq)
	}
	// 其余空闲 bot 在真实输入被处理后仍保持沉默（防误删 bot_id 关联的误唤醒）。
	for _, bot := range bots[1:] {
		var runCount int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&runCount)
		if runCount != 0 {
			t.Fatalf("无输入的 bot 不得被误唤醒: bot=%s runs=%d", bot.ID, runCount)
		}
	}
}
