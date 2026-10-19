package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-125：last_message 是机器人列表端点专属字段——主线最近一条正文 trim 后
// 非空的 user/assistant 消息，契约只含 kind/text，无消息时显式 null；详情端点
// 不填充。

func TestBotListFillsLastMessageListOnly(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "list-last-message")
	withMsg := env.createBot(t, userID)
	empty := env.createBot(t, userID)

	// 账本：主线 user 正文 → 只带 tool_calls、text 为空的 assistant（最后一条
	// 主线消息，应被跳过）→ 更晚的定时任务会话消息（绝不能入选）。
	msgs := repository.NewBotMessageRepository(env.db)
	appendMsg := func(sessionKind string, kind model.BotMessageKind, content interface{}) {
		t.Helper()
		raw, err := json.Marshal(content)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		seq, err := env.bots.bots.NextMessageSeqTx(env.db, withMsg.ID)
		if err != nil {
			t.Fatalf("seq: %v", err)
		}
		msg := &model.BotMessage{ID: uuid.NewString(), BotID: withMsg.ID, Seq: seq,
			Kind: kind, ContentJSON: string(raw), SessionKind: sessionKind, CreatedAt: time.Now().UTC()}
		if err := msgs.AppendTx(env.db, msg); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	appendMsg(model.BotSessionKindMain, model.BotMessageKindUser, botUserContent{Text: "你好"})
	appendMsg(model.BotSessionKindMain, model.BotMessageKindAssistant, botAssistantContent{Text: "", ToolCalls: []botAssistantToolCall{
		{ProviderToolCallID: "call-1", ToolName: "fs_issue", Action: "list", Arguments: "{}"},
	}})
	appendMsg(model.BotSessionKindSchedule, model.BotMessageKindUser, botUserContent{Text: "定时任务消息不许入选"})
	// 最新的主线消息只回了换行：trim 后为空，同样要跳过，选取不受影响。
	appendMsg(model.BotSessionKindMain, model.BotMessageKindAssistant, botAssistantContent{Text: "\n\n"})

	items, err := env.bots.List(userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]BotResponse{}
	for _, item := range items {
		got[item.ID] = item
	}
	last := got[withMsg.ID].LastMessage
	if last == nil || last.Kind != "user" || last.Text != "你好" {
		t.Fatalf("列表应取主线最后一条正文 trim 后非空消息: %+v", got[withMsg.ID].LastMessage)
	}
	// 契约只有 kind/text，不带回消息时间。
	rawLast, err := json.Marshal(last)
	if err != nil {
		t.Fatalf("marshal last: %v", err)
	}
	if strings.Contains(string(rawLast), "created_at") {
		t.Fatalf("last_message 不应带 created_at: %s", rawLast)
	}
	if got[empty.ID].LastMessage != nil {
		t.Fatalf("没有对话消息的机器人 last_message 应为 null: %+v", got[empty.ID].LastMessage)
	}

	// JSON 契约：无消息机器人的 last_message 以显式 null 出现，而不是缺字段。
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	if !strings.Contains(string(raw), `"last_message":null`) {
		t.Fatalf("无消息机器人应序列化为显式 null: %s", raw)
	}

	// 详情端点不填充：该字段仅列表端点返回。
	detail, err := env.bots.Get(userID, withMsg.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if detail.LastMessage != nil {
		t.Fatalf("详情端点不得返回 last_message: %+v", detail.LastMessage)
	}
}
