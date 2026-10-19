package selflog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ErrInvalidCursor 表示游标无法解析或不匹配当前排序方向，调用方应回 40001。
var ErrInvalidCursor = errors.New("invalid self log cursor")

// Filter 是列表/聚合/链路共用的过滤条件。VisibleUserID 非空时只返回
// 归属该用户的条目，或显式系统行（system_scope=1 且无任何业务关联列）。
// 空归属且未标系统的行（解析失败、键冲突、漏标）对任何认证用户均不可见。
// VisibleUserID 为空时不收窄（仅内部调用；公开查询入口拒绝空 requester）。
type Filter struct {
	StartTime     *time.Time
	EndTime       *time.Time
	Level         string // 精确级别
	MinLevel      string // 不低于该级别
	Modules       []string
	RequestID     string
	UserID        string
	ProjectID     string
	IssueID       string
	RunID         string
	ThreadID      string
	Keyword       string // 包含（匹配 message 与 extra）
	ExcludeKw     string // 排除（同上）
	ExtraEq       [][2]string
	VisibleUserID string
}

// Cursor 是键集分页游标：(time_unix, id) 定位 + 排序方向防串页。
type cursor struct {
	Time int64 `json:"t"`
	ID   int64 `json:"id"`
	Asc  bool  `json:"asc"`
}

// EncodeCursor 把游标序列化为不透明字符串。
func EncodeCursor(t time.Time, id int64, asc bool) string {
	raw, _ := json.Marshal(cursor{Time: t.UnixNano(), ID: id, Asc: asc})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor 解析游标；asc 与请求方向不一致视为无效。
func DecodeCursor(s string, asc bool) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	if c.Asc != asc || c.ID <= 0 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return time.Unix(0, c.Time).UTC(), c.ID, nil
}

// Page 是一页查询结果；NextCursor 为空表示没有更多。
type Page struct {
	Items      []Entry
	NextCursor string
	HasMore    bool
}

// Query 按过滤条件返回一页条目。limit ≤ 0 取默认值；方向由 asc 决定，默认倒序。
func (s *Store) Query(ctx context.Context, f Filter, cursorStr string, limit int, asc bool) (*Page, error) {
	if limit <= 0 {
		limit = 100
	}

	db := s.db.WithContext(ctx).Model(&Record{})
	db = applyFilter(db, f)

	if cursorStr != "" {
		ct, cid, err := DecodeCursor(cursorStr, asc)
		if err != nil {
			return nil, err
		}
		tn := ct.UnixNano()
		if asc {
			db = db.Where("(time_unix > ?) OR (time_unix = ? AND id > ?)", tn, tn, cid)
		} else {
			db = db.Where("(time_unix < ?) OR (time_unix = ? AND id < ?)", tn, tn, cid)
		}
	}

	order := "time_unix DESC, id DESC"
	if asc {
		order = "time_unix ASC, id ASC"
	}

	// 多取一条判断 has_more，避免额外 COUNT。
	var records []Record
	if err := db.Order(order).Limit(limit + 1).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("query self logs: %w", err)
	}

	page := &Page{Items: make([]Entry, 0, len(records))}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	for _, r := range records {
		page.Items = append(page.Items, recordToEntry(r))
	}
	if hasMore && len(records) > 0 {
		last := records[len(records)-1]
		page.NextCursor = EncodeCursor(time.Unix(0, last.TimeUnix).UTC(), last.ID, asc)
	}
	page.HasMore = hasMore
	return page, nil
}

// AggregateRow 是一行聚合结果：按所选维度取值，未选维度为空串。
type AggregateRow struct {
	Module     string `json:"module,omitempty"`
	Level      string `json:"level,omitempty"`
	TimeBucket string `json:"time_bucket,omitempty"` // RFC3339，对齐分钟/小时起点
	Count      int64  `json:"count"`
}

// 聚合单组上限：过滤命中极端多时保护响应体积。
const maxAggregateRows = 2000

// Aggregate 按维度组合（module/level/time）统计条数，过滤条件与 Query 一致。
// bucket 为 minute 或 hour，仅在维度含 time 时生效。超出 maxAggregateRows 时
// 按桶时间倒序截断并 truncated=true，返回行按时间正序。
func (s *Store) Aggregate(ctx context.Context, f Filter, byModule, byLevel, byTime bool, bucket string) ([]AggregateRow, bool, error) {
	db := s.db.WithContext(ctx).Model(&Record{})
	db = applyFilter(db, f)

	selects := []string{}
	groups := []string{}
	if byModule {
		selects = append(selects, "module AS module")
		groups = append(groups, "module")
	}
	if byLevel {
		selects = append(selects, "level AS level")
		groups = append(groups, "level")
	}
	if byTime {
		switch bucket {
		case "hour":
			selects = append(selects, "(time_unix / 3600000000000) * 3600000000000 AS bucket_start")
		default:
			selects = append(selects, "(time_unix / 60000000000) * 60000000000 AS bucket_start")
		}
		groups = append(groups, "bucket_start")
	}
	selects = append(selects, "COUNT(*) AS count")

	var rows []AggregateRow
	var raws []struct {
		Module      string
		Level       string
		BucketStart int64
		Count       int64
	}
	order := join(groups)
	if byTime {
		// 先按桶时间倒序截断，保证保留最近的桶；返回前再翻回正序。
		parts := []string{"bucket_start DESC"}
		if byModule {
			parts = append(parts, "module")
		}
		if byLevel {
			parts = append(parts, "level")
		}
		order = join(parts)
	}

	if err := db.Select(selects).Group(join(groups)).Order(order).Limit(maxAggregateRows + 1).Scan(&raws).Error; err != nil {
		return nil, false, fmt.Errorf("aggregate self logs: %w", err)
	}
	truncated := len(raws) > maxAggregateRows
	if truncated {
		raws = raws[:maxAggregateRows]
	}
	for _, r := range raws {
		row := AggregateRow{Module: r.Module, Level: r.Level, Count: r.Count}
		if byTime && r.BucketStart != 0 {
			row.TimeBucket = time.Unix(0, r.BucketStart).UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	if byTime {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	return rows, truncated, nil
}

// TraceLimit 是单次链路查询的条目上限，超出时 Truncated=true。
const TraceLimit = 1000

// Trace 按请求 ID 或 run ID 返回整条链路（时间正序）。
// 超出上限时倒序取最新 TraceLimit 条再翻回正序，避免把链路末尾的 panic/5xx 丢掉。
func (s *Store) Trace(ctx context.Context, f Filter) ([]Entry, bool, error) {
	page, err := s.Query(ctx, f, "", TraceLimit, false)
	if err != nil {
		return nil, false, err
	}
	truncated := page.HasMore
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page.Items, truncated, nil
}

// applyFilter 组装共用 WHERE 条件；全部走参数绑定，无拼接注入面。
func applyFilter(db *gorm.DB, f Filter) *gorm.DB {
	if f.StartTime != nil {
		db = db.Where("time_unix >= ?", f.StartTime.UnixNano())
	}
	if f.EndTime != nil {
		db = db.Where("time_unix <= ?", f.EndTime.UnixNano())
	}
	if f.Level != "" {
		db = db.Where("level = ?", f.Level)
	}
	if f.MinLevel != "" {
		// SQLite 侧无级别数值函数，展开为级别集合等值匹配。
		db = db.Where("level IN ?", levelsAtLeast(f.MinLevel))
	}
	if len(f.Modules) > 0 {
		db = db.Where("module IN ?", f.Modules)
	}
	eq := map[string]string{
		"request_id": f.RequestID,
		"user_id":    f.UserID,
		"project_id": f.ProjectID,
		"issue_id":   f.IssueID,
		"run_id":     f.RunID,
		"thread_id":  f.ThreadID,
	}
	for col, val := range eq {
		if val != "" {
			db = db.Where(col+" = ?", val)
		}
	}
	if f.Keyword != "" {
		like := "%" + escapeLike(f.Keyword) + "%"
		// COALESCE 抹平 NULL extra 的三值逻辑：NULL LIKE 结果为 NULL，会把整行排除。
		db = db.Where("(COALESCE(message, '') LIKE ? ESCAPE '\\' OR COALESCE(extra, '') LIKE ? ESCAPE '\\')", like, like)
	}
	if f.ExcludeKw != "" {
		like := "%" + escapeLike(f.ExcludeKw) + "%"
		db = db.Where("NOT (COALESCE(message, '') LIKE ? ESCAPE '\\' OR COALESCE(extra, '') LIKE ? ESCAPE '\\')", like, like)
	}
	for _, kv := range f.ExtraEq {
		// CAST 成文本使数字值（如 status:200）与查询串相等可比。
		db = db.Where("CAST(json_extract(self_logs.extra, ?) AS TEXT) = ?", "$."+kv[0], kv[1])
	}
	if f.VisibleUserID != "" {
		db = db.Where(
			"user_id = ? OR (system_scope = 1 AND user_id = '' AND project_id = '' AND issue_id = '' AND run_id = '' AND thread_id = '')",
			f.VisibleUserID,
		)
	}
	return db
}

// levelsAtLeast 展开“不低于某级别”的级别集合。
func levelsAtLeast(min string) []string {
	out := []string{}
	for _, lv := range []string{LevelDebug, LevelInfo, LevelWarn, LevelError} {
		if LevelValue(lv) >= LevelValue(min) {
			out = append(out, lv)
		}
	}
	return out
}

func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '%', '_':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func recordToEntry(r Record) Entry {
	extra := ""
	if r.Extra != nil {
		extra = *r.Extra
	}
	return Entry{
		ID:          r.ID,
		Time:        time.Unix(0, r.TimeUnix).UTC(),
		Level:       r.Level,
		Module:      r.Module,
		Message:     r.Message,
		RequestID:   r.RequestID,
		UserID:      r.UserID,
		ProjectID:   r.ProjectID,
		IssueID:     r.IssueID,
		RunID:       r.RunID,
		ThreadID:    r.ThreadID,
		Extra:       extra,
		SystemScope: r.SystemScope != 0,
	}
}
