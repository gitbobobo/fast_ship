package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// SelfLogHandler 是 Fast Ship 自身日志查询端点（INT-94，只读）。
type SelfLogHandler struct {
	selfLogService *service.SelfLogService
}

func NewSelfLogHandler(selfLogService *service.SelfLogService) *SelfLogHandler {
	return &SelfLogHandler{selfLogService: selfLogService}
}

// selfLogFieldNames 是 fields 参数的合法取值；核心四字段始终返回。
var selfLogFieldNames = map[string]bool{
	"id": true, "time": true, "level": true, "message": true,
	"module": true, "request_id": true, "user_id": true, "project_id": true,
	"issue_id": true, "run_id": true, "thread_id": true,
	"extra": true,
}

// selfLogVisibilityUserID 返回可见性收窄用的用户 ID（JWT 与 API Key 均有归属）。
func selfLogVisibilityUserID(c *gin.Context) string {
	return middleware.GetUserID(c)
}

// ListSelfLogs GET /api/self-logs
func (h *SelfLogHandler) ListSelfLogs(c *gin.Context) {
	filter, fields, err := parseSelfLogFilter(c)
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	limit := parseSelfLogLimit(c.DefaultQuery("limit", "100"))
	asc := c.DefaultQuery("order", "desc") == "asc"

	result, err := h.selfLogService.ListSelfLogs(c.Request.Context(), selfLogVisibilityUserID(c), service.SelfLogListParams{
		Filter: *filter,
		Cursor: c.Query("cursor"),
		Limit:  limit,
		Asc:    asc,
		Fields: fields,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// AggregateSelfLogs GET /api/self-logs/aggregate
func (h *SelfLogHandler) AggregateSelfLogs(c *gin.Context) {
	by := c.Query("by")
	if by == "" {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}
	byModule, byLevel, byTime := false, false, false
	for _, dim := range strings.Split(by, ",") {
		switch strings.TrimSpace(dim) {
		case "module":
			byModule = true
		case "level":
			byLevel = true
		case "time":
			byTime = true
		default:
			middleware.HandleAppError(c, errs.ErrInvalidParams)
			return
		}
	}

	bucket := "minute"
	if byTime {
		bucket = c.DefaultQuery("bucket", "minute")
		if bucket != "minute" && bucket != "hour" {
			middleware.HandleAppError(c, errs.ErrInvalidParams)
			return
		}
	}

	filter, _, err := parseSelfLogFilter(c)
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.selfLogService.AggregateSelfLogs(c.Request.Context(), selfLogVisibilityUserID(c), service.SelfLogAggregateParams{
		Filter:   *filter,
		ByModule: byModule,
		ByLevel:  byLevel,
		ByTime:   byTime,
		Bucket:   bucket,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// TraceSelfLogs GET /api/self-logs/trace
func (h *SelfLogHandler) TraceSelfLogs(c *gin.Context) {
	requestID := strings.TrimSpace(c.Query("request_id"))
	runID := strings.TrimSpace(c.Query("run_id"))

	fields, err := parseSelfLogFields(c.Query("fields"))
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.selfLogService.TraceSelfLogs(c.Request.Context(), selfLogVisibilityUserID(c), requestID, runID, fields)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// parseSelfLogFilter 解析列表/聚合共用的过滤参数；解析失败返回 40001。
func parseSelfLogFilter(c *gin.Context) (*selflog.Filter, []string, error) {
	filter := &selflog.Filter{}

	parseTime := func(name string) (*time.Time, error) {
		raw := c.Query(name)
		if raw == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}

	var err error
	if filter.StartTime, err = parseTime("start_time"); err != nil {
		return nil, nil, err
	}
	if filter.EndTime, err = parseTime("end_time"); err != nil {
		return nil, nil, err
	}

	// level 精确优先；min_level 仅在未给 level 时生效。
	if raw := c.Query("level"); raw != "" {
		level, ok := selflog.ParseLevel(raw)
		if !ok {
			return nil, nil, errs.ErrInvalidParams
		}
		filter.Level = level
	} else if raw := c.Query("min_level"); raw != "" {
		level, ok := selflog.ParseLevel(raw)
		if !ok {
			return nil, nil, errs.ErrInvalidParams
		}
		filter.MinLevel = level
	}

	if raw := c.Query("modules"); raw != "" {
		for _, m := range strings.Split(raw, ",") {
			if m = strings.TrimSpace(m); m != "" {
				filter.Modules = append(filter.Modules, m)
			}
		}
	}

	filter.RequestID = strings.TrimSpace(c.Query("request_id"))
	filter.UserID = strings.TrimSpace(c.Query("user_id"))
	filter.ProjectID = strings.TrimSpace(c.Query("project_id"))
	filter.IssueID = strings.TrimSpace(c.Query("issue_id"))
	filter.RunID = strings.TrimSpace(c.Query("run_id"))
	filter.ThreadID = strings.TrimSpace(c.Query("thread_id"))
	filter.Keyword = c.Query("keyword")
	filter.ExcludeKw = c.Query("exclude_keyword")

	// extra_eq=key=value 可重复；值内允许出现等号（以首个等号切分）。
	for _, raw := range c.QueryArray("extra_eq") {
		key, value, found := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, nil, errs.ErrInvalidParams
		}
		filter.ExtraEq = append(filter.ExtraEq, [2]string{key, value})
	}

	fields, err := parseSelfLogFields(c.Query("fields"))
	if err != nil {
		return nil, nil, err
	}
	return filter, fields, nil
}

func parseSelfLogFields(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	fields := []string{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !selfLogFieldNames[name] {
			return nil, errs.ErrInvalidParams
		}
		fields = append(fields, name)
	}
	return fields, nil
}

// parseSelfLogLimit 解析单页条数；越界（<1 或 >500）回落为 100。
func parseSelfLogLimit(raw string) int {
	const defaultLimit, maxLimit = 100, 500
	limit := defaultLimit
	if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
		limit = n
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}
