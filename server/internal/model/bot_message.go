package model

import "time"

// BotMessage 是不可改写的完整对话账本：批准、拒绝、错误与恢复都追加新消息，
// 不修改旧消息；卡片当前状态从调用账本派生。数据库层有禁止 UPDATE 的触发器，
// 整机器人删除仍按外键级联清理。
type BotMessage struct {
	ID    string `gorm:"type:text;primaryKey" json:"id"`
	BotID string `gorm:"type:text;not null" json:"bot_id"`
	Seq   int64  `gorm:"not null" json:"seq"`

	// RunID / RequestID / CallID 是可选的来源关联；可空外键随对应记录级联删除。
	RunID     *string `gorm:"type:text" json:"run_id,omitempty"`
	RequestID *string `gorm:"type:text" json:"request_id,omitempty"`
	CallID    *string `gorm:"type:text" json:"call_id,omitempty"`

	Kind BotMessageKind `gorm:"type:text;not null" json:"kind"`
	// ContentJSON 按 kind 携带完整正文、assistant 的工具调用数组或结构化事件。
	ContentJSON string `gorm:"type:text;not null" json:"content"`

	// SessionKind 区分主线与定时任务会话；两套会话共用 seq 水位，读取面按
	// 此列隔离（主线读者只看 main，定时任务会话不对用户展示）。默认 main。
	SessionKind string `gorm:"type:text;not null;default:main" json:"session_kind"`

	// ClientMessageID 是用户发送幂等键；DedupKey 例如 receipt:<call_id>、
	// terminal:<call_id>，两者都靠唯一索引挡重复追加。
	ClientMessageID *string   `gorm:"type:text" json:"client_message_id,omitempty"`
	DedupKey        *string   `gorm:"type:text" json:"-"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`

	Bot     Bot              `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
	Run     *BotRun          `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
	Request *BotModelRequest `gorm:"foreignKey:RequestID;constraint:OnDelete:CASCADE" json:"-"`
	Call    *BotToolCall     `gorm:"foreignKey:CallID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotMessage) TableName() string {
	return "bot_messages"
}

type BotMessageKind string

const (
	BotMessageKindUser             BotMessageKind = "user"
	BotMessageKindAssistant        BotMessageKind = "assistant"
	BotMessageKindToolReply        BotMessageKind = "tool_reply"
	BotMessageKindBackgroundResult BotMessageKind = "background_result"
	BotMessageKindApprovalDecision BotMessageKind = "approval_decision"
	BotMessageKindLifecycle        BotMessageKind = "lifecycle"
	// BotMessageKindScheduleEvent 是定时任务运行事件（INT-103）：落在主线
	// 会话末尾，触发机器人判断是否通知用户。
	BotMessageKindScheduleEvent BotMessageKind = "schedule_event"
)

// IsModelInput 报告该类消息是否算「新输入」：用户消息、平台后台结果与定时
// 任务运行事件会触发/延续主线；回执与审计类消息随工具块进入上下文，不算
// 新输入。
func (k BotMessageKind) IsModelInput() bool {
	return k == BotMessageKindUser || k == BotMessageKindBackgroundResult || k == BotMessageKindScheduleEvent
}

// BotEvent 是 SSE 的持久重放账本：不进入模型上下文，解决断线补齐、后台结果
// 晚于 run 完成、流式片段不改消息三个问题。联合主键 (bot_id, seq)。
type BotEvent struct {
	BotID string `gorm:"type:text;primaryKey" json:"bot_id"`
	Seq   int64  `gorm:"primaryKey;autoIncrement:false" json:"seq"`

	RunID *string `gorm:"type:text" json:"run_id,omitempty"`
	// EventType 固定五种 SSE 类型：delta / tool_call / tool_result / done / error。
	EventType string `gorm:"type:text;not null" json:"event_type"`
	// SessionKind 隔离定时任务的流式事件：聊天页读者只返回 main。
	SessionKind string    `gorm:"type:text;not null;default:main" json:"session_kind"`
	PayloadJSON string    `gorm:"type:text;not null" json:"payload"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`

	Bot Bot     `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
	Run *BotRun `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotEvent) TableName() string {
	return "bot_events"
}

// SSE 事件类型常量；与 handler 的 SSE 编码共用，不设第六种。
const (
	BotEventDelta      = "delta"
	BotEventToolCall   = "tool_call"
	BotEventToolResult = "tool_result"
	BotEventDone       = "done"
	BotEventError      = "error"
)
