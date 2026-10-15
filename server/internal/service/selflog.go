package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
)

// SelfLogService 是 Fast Ship 自身日志（INT-94）的只读查询服务。
// 可见性规则在这里统一收口：调用者只能看到自己归属的日志与无归属的进程级日志。
type SelfLogService struct {
	store *selflog.Store
}

func NewSelfLogService(store *selflog.Store) *SelfLogService {
	return &SelfLogService{store: store}
}

// SelfLogListParams 是列表查询的已校验参数。
type SelfLogListParams struct {
	Filter selflog.Filter
	Cursor string
	Limit  int
	Asc    bool
	Fields []string
}

// ListSelfLogs 分页查询自身日志；requesterUserID 为空时（系统内部调用）不做归属收窄。
func (s *SelfLogService) ListSelfLogs(ctx context.Context, requesterUserID string, p SelfLogListParams) (*api.SelfLogPage, error) {
	p.Filter.VisibleUserID = requesterUserID
	page, err := s.store.Query(ctx, p.Filter, p.Cursor, p.Limit, p.Asc)
	if err != nil {
		if errors.Is(err, selflog.ErrInvalidCursor) {
			return nil, errs.ErrInvalidParams
		}
		return nil, errs.ErrInternal
	}

	items := make([]api.SelfLogEntry, 0, len(page.Items))
	for _, e := range page.Items {
		items = append(items, SelfLogEntryToAPI(e, p.Fields))
	}
	result := &api.SelfLogPage{Items: items, HasMore: page.HasMore}
	if page.NextCursor != "" {
		result.NextCursor = api.Ptr(page.NextCursor)
	}
	return result, nil
}

// SelfLogAggregateParams 是聚合查询的已校验参数。
type SelfLogAggregateParams struct {
	Filter   selflog.Filter
	ByModule bool
	ByLevel  bool
	ByTime   bool
	Bucket   string // minute / hour
}

// AggregateSelfLogs 按维度组合统计条数；可见范围与列表一致。
func (s *SelfLogService) AggregateSelfLogs(ctx context.Context, requesterUserID string, p SelfLogAggregateParams) (*api.SelfLogAggregateResult, error) {
	p.Filter.VisibleUserID = requesterUserID
	rows, truncated, err := s.store.Aggregate(ctx, p.Filter, p.ByModule, p.ByLevel, p.ByTime, p.Bucket)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]api.SelfLogAggregateRow, 0, len(rows))
	for _, r := range rows {
		row := api.SelfLogAggregateRow{Count: int(r.Count)}
		if p.ByModule {
			row.Module = api.Ptr(r.Module)
		}
		if p.ByLevel {
			row.Level = api.Ptr(r.Level)
		}
		if p.ByTime {
			row.TimeBucket = api.Ptr(r.TimeBucket)
		}
		out = append(out, row)
	}
	return &api.SelfLogAggregateResult{Rows: out, Truncated: truncated}, nil
}

// TraceSelfLogs 按请求 ID 或 run ID 返回整条链路（时间正序）。
// 两者必须恰好提供一个；可见范围与列表一致。
func (s *SelfLogService) TraceSelfLogs(ctx context.Context, requesterUserID, requestID, runID string, fields []string) (*api.SelfLogTraceResult, error) {
	if (requestID == "") == (runID == "") {
		return nil, errs.ErrInvalidParams
	}
	f := selflog.Filter{
		RequestID:     requestID,
		RunID:         runID,
		VisibleUserID: requesterUserID,
	}
	entries, truncated, err := s.store.Trace(ctx, f)
	if err != nil {
		return nil, errs.ErrInternal
	}
	items := make([]api.SelfLogEntry, 0, len(entries))
	for _, e := range entries {
		items = append(items, SelfLogEntryToAPI(e, fields))
	}
	return &api.SelfLogTraceResult{Items: items, Truncated: truncated}, nil
}

// SelfLogEntryToAPI 把存储条目投影为契约类型；fields 为空时返回全部字段。
func SelfLogEntryToAPI(e selflog.Entry, fields []string) api.SelfLogEntry {
	selected := func(name string) bool {
		if len(fields) == 0 {
			return true
		}
		for _, f := range fields {
			if f == name {
				return true
			}
		}
		return false
	}

	// 核心字段（id/time/level/message）始终返回；fields 只裁剪关联与附加字段。
	entry := api.SelfLogEntry{
		Id:      e.ID,
		Time:    api.JSONTime(e.Time),
		Level:   api.SelfLogEntryLevel(e.Level),
		Message: e.Message,
	}
	if selected("module") && e.Module != "" {
		entry.Module = api.Ptr(e.Module)
	}
	if selected("request_id") && e.RequestID != "" {
		entry.RequestId = api.Ptr(e.RequestID)
	}
	if selected("user_id") && e.UserID != "" {
		entry.UserId = api.Ptr(e.UserID)
	}
	if selected("project_id") && e.ProjectID != "" {
		entry.ProjectId = api.Ptr(e.ProjectID)
	}
	if selected("issue_id") && e.IssueID != "" {
		entry.IssueId = api.Ptr(e.IssueID)
	}
	if selected("automation_id") && e.AutomationID != "" {
		entry.AutomationId = api.Ptr(e.AutomationID)
	}
	if selected("run_id") && e.RunID != "" {
		entry.RunId = api.Ptr(e.RunID)
	}
	if selected("thread_id") && e.ThreadID != "" {
		entry.ThreadId = api.Ptr(e.ThreadID)
	}
	if selected("extra") && e.Extra != "" {
		var extra map[string]interface{}
		if err := json.Unmarshal([]byte(e.Extra), &extra); err == nil {
			entry.Extra = extra
		}
	}
	return entry
}
