package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

const maxDocumentBodyBytes = 1 << 20

type DocumentHandler struct {
	documentService *service.DocumentService
}

func NewDocumentHandler(documentService *service.DocumentService) *DocumentHandler {
	return &DocumentHandler{documentService: documentService}
}

type createDocumentRequest struct {
	Title    string  `json:"title"`
	Body     string  `json:"body"`
	ParentID *string `json:"parent_id"`
}

type updateDocumentRequest struct {
	Title    *string         `json:"title"`
	Body     *string         `json:"body"`
	ParentID json.RawMessage `json:"parent_id"`
}

func parseOptionalParentID(raw json.RawMessage) (**string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if string(raw) == "null" {
		var nilParent *string
		outer := &nilParent
		return outer, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	inner := value
	ptr := &inner
	outer := &ptr
	return outer, nil
}

func (h *DocumentHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.documentService.List(projectID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DocumentHandler) Create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDocumentBodyBytes)

	projectID := c.Param("id")
	userID := middleware.GetUserID(c)

	var req createDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.documentService.Create(projectID, userID, &service.CreateDocumentRequest{
		Title:    req.Title,
		Body:     req.Body,
		ParentID: req.ParentID,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DocumentHandler) Get(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := middleware.GetUserID(c)

	result, err := h.documentService.Get(docID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DocumentHandler) Update(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDocumentBodyBytes)

	docID := c.Param("doc_id")
	userID := middleware.GetUserID(c)

	var req updateDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	parentID, err := parseOptionalParentID(req.ParentID)
	if err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.documentService.Update(docID, userID, &service.UpdateDocumentRequest{
		Title:    req.Title,
		Body:     req.Body,
		ParentID: parentID,
	})
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DocumentHandler) Delete(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := middleware.GetUserID(c)

	if err := h.documentService.Delete(docID, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, nil)
}
