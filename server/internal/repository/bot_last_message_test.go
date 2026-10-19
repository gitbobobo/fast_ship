package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// INT-125：机器人列表端点的 last_message 选取规则。一次批量查询按 bot 取
// 主线（session_kind='main'）最近一条正文 trim 后非空的 user/assistant 消息；
// 定时任务会话、空正文、纯空白正文与坏 JSON 消息不参与，坏 JSON 不得让查询
// 失败。

func setupBotLastMessageTestDB(t *testing.T) (*gorm.DB, *BotRepository) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Bot{}, &model.BotMessage{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewBotRepository(db)
}

func createBotLastMessageBot(t *testing.T, db *gorm.DB, userID string) *model.Bot {
	t.Helper()
	now := time.Now().UTC()
	// bots.user_id 外键指向 users，先补齐归属用户（同一用户可建多个机器人）。
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{
		ID: userID, Username: "u-" + userID, Email: userID + "@test.local",
		PasswordHash: "x", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	bot := &model.Bot{
		ID: uuid.NewString(), UserID: userID, Name: "bot-" + uuid.NewString()[:8],
		PersonaPrompt: "p", SystemPromptText: "p", PromptRevision: 1,
		PlatformPromptVersion: 1, ToolsetVersion: 1, LastToldVersion: 1,
		BaseURL: "https://api.example.com/v1", Model: "m",
		APIKeyEncrypted: []byte("cipher"), APIKeyLast4: "1234",
		NextMessageSeq: 1, NextEventSeq: 1, OfferedInputSeq: 0,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("create bot: %v", err)
	}
	return bot
}

// appendBotLastMessage 按给定 seq 直插一条消息（绕过取号水位，测试只关心
// 选取顺序）；contentJSON 传坏数据时原样落库。
func appendBotLastMessage(t *testing.T, db *gorm.DB, botID string, seq int64, kind model.BotMessageKind, sessionKind, contentJSON string, createdAt time.Time) {
	t.Helper()
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		Kind: kind, ContentJSON: contentJSON, SessionKind: sessionKind, CreatedAt: createdAt,
	}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("append message seq=%d: %v", seq, err)
	}
}

// botTestUserContent 是 user 消息正文的测试投影，与 service.botUserContent 同形。
type botTestUserContent struct {
	Text string `json:"text"`
}

func userTextJSON(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(botTestUserContent{Text: text})
	if err != nil {
		t.Fatalf("marshal user content: %v", err)
	}
	return string(raw)
}

func TestBotLastMessagesByBotIDsPicksLastNonEmptyMainline(t *testing.T) {
	db, repo := setupBotLastMessageTestDB(t)
	base := time.Now().UTC().Add(-time.Hour)

	// Bot A：最后一条是纯空白正文的 assistant（模型只回了换行和空格）→ trim
	// 后为空跳过；再往前是只带 tool_calls、text 为空的 assistant → 也跳过；
	// 取更早的 user 正文。
	botA := createBotLastMessageBot(t, db, "user-a")
	appendBotLastMessage(t, db, botA.ID, 1, model.BotMessageKindUser, model.BotSessionKindMain, userTextJSON(t, "第一条"), base)
	appendBotLastMessage(t, db, botA.ID, 2, model.BotMessageKindAssistant, model.BotSessionKindMain, `{"text":"","tool_calls":[{"call_id":"c1"}]}`, base.Add(time.Minute))
	appendBotLastMessage(t, db, botA.ID, 3, model.BotMessageKindUser, model.BotSessionKindMain, userTextJSON(t, "最后一条"), base.Add(2*time.Minute))
	appendBotLastMessage(t, db, botA.ID, 4, model.BotMessageKindAssistant, model.BotSessionKindMain, `{"text":"\n\n  \t"}`, base.Add(3*time.Minute))

	// Bot B：主线之后插了更晚 seq 的定时任务会话消息 → 绝不参与，取主线
	// 最后一条 assistant 正文。
	botB := createBotLastMessageBot(t, db, "user-a")
	appendBotLastMessage(t, db, botB.ID, 1, model.BotMessageKindUser, model.BotSessionKindMain, userTextJSON(t, "早上好"), base)
	appendBotLastMessage(t, db, botB.ID, 2, model.BotMessageKindAssistant, model.BotSessionKindMain, `{"text":"回复正文"}`, base.Add(time.Minute))
	appendBotLastMessage(t, db, botB.ID, 3, model.BotMessageKindUser, model.BotSessionKindSchedule, userTextJSON(t, "定时任务消息不许出现"), base.Add(2*time.Minute))
	appendBotLastMessage(t, db, botB.ID, 4, model.BotMessageKindAssistant, model.BotSessionKindSchedule, `{"text":"定时任务回复"}`, base.Add(3*time.Minute))

	// Bot C：正文非空但最后一条 content 是坏 JSON → 按无正文跳过，继续往前。
	// 选中的正文带首尾空白：trim 只用于过滤，返回值必须保持原样。
	botC := createBotLastMessageBot(t, db, "user-a")
	appendBotLastMessage(t, db, botC.ID, 1, model.BotMessageKindUser, model.BotSessionKindMain, userTextJSON(t, "  好的  "), base)
	appendBotLastMessage(t, db, botC.ID, 2, model.BotMessageKindAssistant, model.BotSessionKindMain, `{"text": oops`, base.Add(time.Minute))

	got, err := repo.LastMessagesByBotIDs([]string{botA.ID, botB.ID, botC.ID})
	if err != nil {
		t.Fatalf("LastMessagesByBotIDs: %v", err)
	}

	a, ok := got[botA.ID]
	if !ok {
		t.Fatalf("bot A 应有最近消息")
	}
	if a.Kind != string(model.BotMessageKindUser) || a.Text != "最后一条" {
		t.Fatalf("bot A 应跳过纯空白与空正文取最后一条 user 正文: %+v", a)
	}

	b, ok := got[botB.ID]
	if !ok {
		t.Fatalf("bot B 应有最近消息")
	}
	if b.Kind != string(model.BotMessageKindAssistant) || b.Text != "回复正文" {
		t.Fatalf("bot B 不得选中定时任务会话消息: %+v", b)
	}

	c, ok := got[botC.ID]
	if !ok {
		t.Fatalf("bot C 坏 JSON 之后还有更早正文，应有最近消息")
	}
	if c.Text != "  好的  " {
		t.Fatalf("bot C 坏 JSON 消息应被跳过并继续往前，返回正文保持原样不 trim: %+v", c)
	}
}

func TestBotLastMessagesByBotIDsWithoutMessagesIsAbsent(t *testing.T) {
	db, repo := setupBotLastMessageTestDB(t)
	bot := createBotLastMessageBot(t, db, "user-b")

	got, err := repo.LastMessagesByBotIDs([]string{bot.ID})
	if err != nil {
		t.Fatalf("LastMessagesByBotIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("没有对话消息的机器人不应出现在结果里: %+v", got)
	}

	// 空入参直接返回空 map，不产生查询。
	empty, err := repo.LastMessagesByBotIDs(nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空入参应返回空 map: %+v err=%v", empty, err)
	}
}

func TestBotLastMessagesByBotIDsAllBadJSONIsAbsent(t *testing.T) {
	db, repo := setupBotLastMessageTestDB(t)
	base := time.Now().UTC()

	// 全部消息的 content 都是坏 JSON：按「没有正文」处理，机器人无预览。
	bot := createBotLastMessageBot(t, db, "user-c")
	appendBotLastMessage(t, db, bot.ID, 1, model.BotMessageKindUser, model.BotSessionKindMain, "not-json{{", base)
	appendBotLastMessage(t, db, bot.ID, 2, model.BotMessageKindAssistant, model.BotSessionKindMain, `{"text":`, base.Add(time.Minute))

	got, err := repo.LastMessagesByBotIDs([]string{bot.ID})
	if err != nil {
		t.Fatalf("坏 JSON 不得让查询报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("全部坏 JSON 的机器人应按无正文处理: %+v", got)
	}
}
