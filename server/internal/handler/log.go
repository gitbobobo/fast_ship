package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

const maxLogUploadBodyBytes = 4 << 20

type LogHandler struct {
	logService *service.LogService
}

func NewLogHandler(logService *service.LogService) *LogHandler {
	return &LogHandler{logService: logService}
}

func (h *LogHandler) requireApiKey(c *gin.Context) bool {
	if middleware.IsJWTAuth(c) {
		middleware.HandleAppError(c, errs.ErrApiKeyRequired)
		return false
	}
	return true
}

type uploadLogsRequest struct {
	RunID       string                  `json:"run_id"`
	Source      string                  `json:"source"`
	Description string                  `json:"description"`
	Entries     []service.LogEntryInput `json:"entries"`
}

func (h *LogHandler) Upload(c *gin.Context) {
	if !h.requireApiKey(c) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLogUploadBodyBytes)

	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	var req uploadLogsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	var uploaderAPIKeyID *string
	if id := middleware.GetAPIKeyID(c); id != "" {
		uploaderAPIKeyID = &id
	}

	result, err := h.logService.UploadLogs(projectID, userID, uploaderAPIKeyID, &service.UploadLogsRequest{
		RunID:       req.RunID,
		Source:      req.Source,
		Description: req.Description,
		Entries:     req.Entries,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *LogHandler) ListEntries(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	page, pageSize := parseLogPagination(c)
	sort, err := parseLogSort(c.DefaultQuery("sort", "timestamp_desc"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	from, err := parseOptionalTimeQuery(c.Query("from"))
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}
	to, err := parseOptionalTimeQuery(c.Query("to"))
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	items, total, err := h.logService.ListEntries(projectID, userID, service.ListLogEntriesRequest{
		BatchID:     c.Query("batch_id"),
		RunID:       c.Query("run_id"),
		Level:       c.Query("level"),
		EntrySource: c.Query("entry_source"),
		BatchSource: c.Query("batch_source"),
		Query:       c.Query("q"),
		From:        from,
		To:          to,
		Page:        page,
		PageSize:    pageSize,
		Sort:        sort,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, items, total, page, pageSize)
}

func (h *LogHandler) ListBatches(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	page, pageSize := parseLogPagination(c)

	from, err := parseOptionalTimeQuery(c.Query("from"))
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}
	to, err := parseOptionalTimeQuery(c.Query("to"))
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	items, total, err := h.logService.ListBatches(projectID, userID, service.ListLogBatchesRequest{
		RunID:       c.Query("run_id"),
		BatchSource: c.Query("batch_source"),
		From:        from,
		To:          to,
		Page:        page,
		PageSize:    pageSize,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, items, total, page, pageSize)
}

func (h *LogHandler) GetBatch(c *gin.Context) {
	batchID := c.Param("batch_id")
	userID := middleware.GetUserID(c)

	item, err := h.logService.GetBatch(batchID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, item)
}

func (h *LogHandler) DeleteBatch(c *gin.Context) {
	batchID := c.Param("batch_id")
	userID := middleware.GetUserID(c)

	if err := h.logService.DeleteBatch(batchID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}

func (h *LogHandler) DeleteByProject(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	if err := h.logService.DeleteByProject(projectID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}

func parseLogPagination(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	return page, pageSize
}

func parseLogSort(raw string) (string, error) {
	switch raw {
	case "", "timestamp_desc":
		return "timestamp_desc", nil
	case "timestamp_asc":
		return "timestamp_asc", nil
	default:
		return "", errs.ErrInvalidParams
	}
}

func parseOptionalTimeQuery(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
