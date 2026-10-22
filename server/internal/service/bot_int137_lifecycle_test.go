package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-137 R6：toolset_updated 的投影角色。新追加的消息带 render=user 标记，
// 以 user 角色进请求；旧消息（无标记）保持 system 角色，已有会话的
// prefix_hash 在压缩边界前不变。
func TestBotToolsetUpdatedLifecycleRole(t *testing.T) {
	legacy, err := json.Marshal(botLifecycleContent{
		Event:  "toolset_updated",
		Detail: json.RawMessage(`{"from_version":1,"to_version":2,"note":"legacy"}`),
	})
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	fresh, err := json.Marshal(botLifecycleContent{
		Event:  "toolset_updated",
		Render: "user",
		Detail: json.RawMessage(`{"from_version":2,"to_version":3,"note":"fresh"}`),
	})
	if err != nil {
		t.Fatalf("marshal fresh: %v", err)
	}

	rows := []model.BotMessage{
		{ID: "m1", Seq: 1, Kind: model.BotMessageKindLifecycle, ContentJSON: string(legacy)},
		{ID: "m2", Seq: 2, Kind: model.BotMessageKindLifecycle, ContentJSON: string(fresh)},
	}
	msgs, _, _, err := botProjectMessages(rows, nil, "", false)
	if err != nil {
		t.Fatalf("project messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 projected messages, got %d", len(msgs))
	}
	if msgs[0].OfSystem == nil {
		t.Fatalf("legacy toolset_updated 必须是 system 角色，实际 union=%+v", msgs[0])
	}
	if msgs[1].OfUser == nil {
		t.Fatalf("新 toolset_updated 必须是 user 角色，实际 union=%+v", msgs[1])
	}
	if got := msgs[1].OfUser.Content.OfString.Value; !strings.HasPrefix(got, "[平台事件]") {
		t.Fatalf("新 toolset_updated 必须带 [平台事件] 前缀，实际 %q", got)
	}
}

// 旧格式消息（无 render 标记）的投影在改动前后必须逐字节一致：这是
// 「已有会话在压缩边界前 prefix_hash 不变」的可核查证据。
func TestBotLegacyLifecycleProjectionStable(t *testing.T) {
	raw := `{"event":"toolset_updated","detail":{"from_version":1,"to_version":2,"note":"旧会话"}}`
	rows := []model.BotMessage{
		{ID: "m1", Seq: 1, Kind: model.BotMessageKindLifecycle, ContentJSON: raw},
	}
	msgs, _, _, err := botProjectMessages(rows, nil, "", false)
	if err != nil {
		t.Fatalf("project messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].OfSystem == nil {
		t.Fatalf("旧消息必须仍是 system 角色，实际 %+v", msgs)
	}
	want := "[工具集变更] 以下是平台可信系统事件（非用户发言）：\n" + raw
	if got := msgs[0].OfSystem.Content.OfString.Value; got != want {
		t.Fatalf("旧消息投影正文变了（会改变 prefix_hash）:\n got=%q\nwant=%q", got, want)
	}
}
