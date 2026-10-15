package service

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
)

// 只读查询与运行控制入口：全部仅 JWT，按机器人归属取用户身份。

func (s *BotService) checkOwnership(userID, botID string) error {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrBotNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

func (s *BotService) ListRuns(userID, botID string, limit int) ([]BotRunResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.runs.ListByBot(botID, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotRunResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botRunResponseOf(&rows[i]))
	}
	return out, nil
}

func (s *BotService) GetRun(userID, botID, runID string) (*BotRunResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	run, err := s.runs.FindByID(runID)
	if err != nil || run.BotID != botID {
		return nil, errs.ErrBotRunNotFound
	}
	resp := botRunResponseOf(run)
	return &resp, nil
}

func (s *BotService) ListCalls(userID, botID string, statuses []string, limit int) ([]BotToolCallResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.calls.ListByBot(botID, statuses, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotToolCallResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botToolCallResponseOf(&rows[i]))
	}
	return out, nil
}

func (s *BotService) findOwnCall(userID, botID, callID string) (*model.BotToolCall, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	call, err := s.calls.FindByID(callID)
	if err != nil || call.BotID != botID {
		return nil, errs.ErrBotCallNotFound
	}
	return call, nil
}

func (s *BotService) GetCall(userID, botID, callID string) (*BotToolCallResponse, error) {
	call, err := s.findOwnCall(userID, botID, callID)
	if err != nil {
		return nil, err
	}
	resp := botToolCallResponseOf(call)
	return &resp, nil
}

// CallResultResponse 是完整结果的读取响应（UI 查看；模型走 fs_result.read）。
type CallResultResponse struct {
	CallID      string          `json:"call_id"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result"`
	Truncated   bool            `json:"truncated"`
	ResultChars int64           `json:"result_chars"`
}

func (s *BotService) GetCallResult(userID, botID, callID string) (*CallResultResponse, error) {
	call, err := s.findOwnCall(userID, botID, callID)
	if err != nil {
		return nil, err
	}
	resp := &CallResultResponse{
		CallID:      call.ID,
		Status:      string(call.Status),
		Truncated:   call.Truncated,
		ResultChars: call.ResultChars,
	}
	if call.ResultJSON != nil {
		resp.Result = json.RawMessage(*call.ResultJSON)
	}
	return resp, nil
}

// CancelCall 取消后台调用；终态调用幂等返回现状。
func (s *BotService) CancelCall(userID, botID, callID string) (*BotToolCallResponse, error) {
	call, err := s.findOwnCall(userID, botID, callID)
	if err != nil {
		return nil, err
	}
	if call.Status.IsTerminal() {
		resp := botToolCallResponseOf(call)
		return &resp, nil
	}
	fresh, _ := s.cancelBotCall(call)
	if fresh == nil {
		return nil, errs.ErrInternal
	}
	resp := botToolCallResponseOf(fresh)
	return &resp, nil
}

// ---------- SSE 支撑 ----------

// BotEventResponse 是事件的 API 投影（重放用）。
type BotEventResponse struct {
	Seq       int64           `json:"seq"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

func botEventResponseOf(e *model.BotEvent) BotEventResponse {
	return BotEventResponse{
		Seq:       e.Seq,
		EventType: e.EventType,
		Payload:   json.RawMessage(e.PayloadJSON),
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ListEvents 重放：seq 大于 after 的全部事件（正序）。
func (s *BotService) ListEvents(userID, botID string, after int64, limit int) ([]BotEventResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.events.ListAfterSeq(botID, after, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotEventResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botEventResponseOf(&rows[i]))
	}
	return out, nil
}

// SubscribeEvents 注册 SSE 订阅；先订阅后补读之间不漏事件。返回的取消函数
// 只结束订阅，不影响 run。
func (s *BotService) SubscribeEvents(botID string) (<-chan int64, func()) {
	return s.streamHub.subscribe(botID)
}
