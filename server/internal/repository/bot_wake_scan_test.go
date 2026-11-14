package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// scanSQLRecorder 抓取 gorm 执行的 SQL 文本，供扫描查询形状断言使用。
type scanSQLRecorder struct {
	mu    sync.Mutex
	stmts []string
}

func (r *scanSQLRecorder) LogMode(logger.LogLevel) logger.Interface      { return r }
func (r *scanSQLRecorder) Info(context.Context, string, ...interface{})  {}
func (r *scanSQLRecorder) Warn(context.Context, string, ...interface{})  {}
func (r *scanSQLRecorder) Error(context.Context, string, ...interface{}) {}
func (r *scanSQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	if err != nil {
		return
	}
	sql, _ := fc()
	r.mu.Lock()
	r.stmts = append(r.stmts, sql)
	r.mu.Unlock()
}

func (r *scanSQLRecorder) reset() {
	r.mu.Lock()
	r.stmts = nil
	r.mu.Unlock()
}

func (r *scanSQLRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stmts...)
}

func setupWakeScanDB(t *testing.T, rec *scanSQLRecorder) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	cfg := &gorm.Config{}
	if rec != nil {
		cfg.Logger = rec
	}
	db, err := gorm.Open(sqlite.Open(dsn), cfg)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(BotModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func createWakeScanBot(t *testing.T, db *gorm.DB, persona, system string) *model.Bot {
	t.Helper()
	bot := &model.Bot{
		ID:               uuid.NewString(),
		UserID:           uuid.NewString(),
		Name:             "bot-" + uuid.NewString()[:8],
		PersonaPrompt:    persona,
		SystemPromptText: system,
		Model:            "fake-model",
		NextMessageSeq:   1,
		UntrustedNonce:   uuid.NewString(),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("create bot: %v", err)
	}
	return bot
}

// appendWakeMsg 追加一条账本消息并推进账本水位（next_message_seq）。
func appendWakeMsg(t *testing.T, db *gorm.DB, bot *model.Bot, kind model.BotMessageKind, sessionKind string) int64 {
	t.Helper()
	seq := bot.NextMessageSeq
	bot.NextMessageSeq++
	if err := db.Create(&model.BotMessage{
		ID: uuid.NewString(), BotID: bot.ID, Seq: seq,
		Kind: kind, SessionKind: sessionKind,
		ContentJSON: `{"text":"内容"}`, CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("next_message_seq", bot.NextMessageSeq).Error; err != nil {
		t.Fatalf("advance watermark: %v", err)
	}
	return seq
}

func idsOf(rows []BotWakeScanRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// 口径：只有主线会话内 seq 大于 offered_input_seq 的输入消息（user /
// background_result / schedule_event）命中；非输入消息、定时任务会话消息
// 与已消费输入都不命中。
func TestListBotsWithUnconsumedInputScopes(t *testing.T) {
	db := setupWakeScanDB(t, nil)
	bigPersona := "人设正文" + fmt.Sprintf("%6000s", "")
	bigSystem := "平台规则" + fmt.Sprintf("%6000s", "")
	bot := createWakeScanBot(t, db, bigPersona, bigSystem)
	repo := NewBotRepository(db)

	// 一轮已完成的纯文本回复：输入已消费，非输入消息推高账本水位。
	seqUser := appendWakeMsg(t, db, bot, model.BotMessageKindUser, model.BotSessionKindMain)
	appendWakeMsg(t, db, bot, model.BotMessageKindAssistant, model.BotSessionKindMain)
	if err := db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("offered_input_seq", seqUser).Error; err != nil {
		t.Fatalf("consume input: %v", err)
	}
	if rows, err := repo.ListBotsWithUnconsumedInput(); err != nil || len(rows) != 0 {
		t.Fatalf("只有非输入消息推高水位时不得命中: rows=%v err=%v", rows, err)
	}

	// 定时任务会话的消息共用 seq 水位，但不算主线新输入。
	appendWakeMsg(t, db, bot, model.BotMessageKindUser, model.BotSessionKindSchedule)
	if rows, err := repo.ListBotsWithUnconsumedInput(); err != nil || len(rows) != 0 {
		t.Fatalf("schedule 会话消息不得命中主线扫描: rows=%v err=%v", rows, err)
	}

	// 主线的回执与审计类消息不算新输入。
	for _, kind := range []model.BotMessageKind{
		model.BotMessageKindToolReply,
		model.BotMessageKindApprovalDecision,
		model.BotMessageKindLifecycle,
	} {
		appendWakeMsg(t, db, bot, kind, model.BotSessionKindMain)
	}
	if rows, err := repo.ListBotsWithUnconsumedInput(); err != nil || len(rows) != 0 {
		t.Fatalf("非输入消息不得命中: rows=%v err=%v", rows, err)
	}

	// 主线后台结果与 schedule_event 是真实输入，命中且带触发键所需水位。
	seqBg := appendWakeMsg(t, db, bot, model.BotMessageKindBackgroundResult, model.BotSessionKindMain)
	rows, err := repo.ListBotsWithUnconsumedInput()
	if err != nil || len(rows) != 1 {
		t.Fatalf("后台结果应命中一条: rows=%v err=%v", rows, err)
	}
	if rows[0].ID != bot.ID || rows[0].NextMessageSeq != seqBg+1 {
		t.Fatalf("投影不符: got id=%s next=%d want id=%s next=%d",
			rows[0].ID, rows[0].NextMessageSeq, bot.ID, seqBg+1)
	}

	// 消费后不再命中。
	if err := db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("offered_input_seq", seqBg).Error; err != nil {
		t.Fatalf("consume bg: %v", err)
	}
	if rows, err := repo.ListBotsWithUnconsumedInput(); err != nil || len(rows) != 0 {
		t.Fatalf("消费后不得再命中: rows=%v err=%v", rows, err)
	}

	// schedule_event 命中主线扫描。
	seqEvt := appendWakeMsg(t, db, bot, model.BotMessageKindScheduleEvent, model.BotSessionKindMain)
	if rows, err := repo.ListBotsWithUnconsumedInput(); err != nil || len(rows) != 1 ||
		!containsID(idsOf(rows), bot.ID) {
		t.Fatalf("schedule_event 应命中: rows=%v err=%v", rows, err)
	}
	_ = seqEvt
}

// 查询形状：一条 SQL 完成，不随闲置 bot 数增长，不加载人设大字段，
// 不做逐 bot COUNT（INT-227 验收）。
func TestListBotsWithUnconsumedInputQueryShape(t *testing.T) {
	rec := &scanSQLRecorder{}
	db := setupWakeScanDB(t, rec)
	repo := NewBotRepository(db)

	// 6 个已完成回复、水位落后的空闲 bot（旧扫描会整行选出并逐个 COUNT）。
	for i := 0; i < 6; i++ {
		bot := createWakeScanBot(t, db, "大段人设正文", "大段平台规则")
		seq := appendWakeMsg(t, db, bot, model.BotMessageKindUser, model.BotSessionKindMain)
		appendWakeMsg(t, db, bot, model.BotMessageKindAssistant, model.BotSessionKindMain)
		if err := db.Model(&model.Bot{}).Where("id = ?", bot.ID).
			Update("offered_input_seq", seq).Error; err != nil {
			t.Fatalf("consume input: %v", err)
		}
	}

	rec.reset()
	for i := 0; i < 3; i++ {
		if rows, err := repo.ListBotsWithUnconsumedInput(); err != nil || len(rows) != 0 {
			t.Fatalf("空闲 bot 不应命中: rows=%v err=%v", rows, err)
		}
	}
	stmts := rec.all()
	if len(stmts) != 3 {
		t.Fatalf("每次调用应恰好一条 SQL，实得 %d 条: %v", len(stmts), stmts)
	}
	for _, sql := range stmts {
		lower := strings.ToLower(sql)
		if !strings.Contains(lower, "bots") || !strings.Contains(lower, "select") {
			t.Fatalf("应为 bots 表查询: %s", sql)
		}
		if strings.Contains(lower, "persona_prompt") || strings.Contains(lower, "system_prompt_text") {
			t.Fatalf("扫描不得加载人设大字段: %s", sql)
		}
		if strings.Contains(lower, "count(") {
			t.Fatalf("扫描不得逐 bot COUNT: %s", sql)
		}
		if !strings.Contains(lower, "next_message_seq") {
			t.Fatalf("投影应带触发键所需水位: %s", sql)
		}
	}
}
