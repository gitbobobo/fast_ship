package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type VersionHandler struct {
	versionService *service.VersionService
	shipService    *service.ShipService
}

func NewVersionHandler(versionService *service.VersionService, shipService *service.ShipService) *VersionHandler {
	return &VersionHandler{
		versionService: versionService,
		shipService:    shipService,
	}
}

func (h *VersionHandler) Create(c *gin.Context) {
	projectID := c.Param("id")
	var req service.CreateVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.versionService.Create(projectID, userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *VersionHandler) Get(c *gin.Context) {
	vid := c.Param("vid")
	userID := middleware.GetUserID(c)
	result, err := h.versionService.Get(vid, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *VersionHandler) List(c *gin.Context) {
	projectID := c.Param("id")
	userID := middleware.GetUserID(c)
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	versions, total, err := h.versionService.List(projectID, userID, status, page, pageSize)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, versions, total, page, pageSize)
}

func (h *VersionHandler) Update(c *gin.Context) {
	vid := c.Param("vid")
	var req service.UpdateVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.versionService.Update(vid, userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *VersionHandler) Delete(c *gin.Context) {
	vid := c.Param("vid")
	userID := middleware.GetUserID(c)

	if err := h.versionService.Delete(vid, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}

func (h *VersionHandler) Ship(c *gin.Context) {
	vid := c.Param("vid")
	userID := middleware.GetUserID(c)

	if err := h.shipService.Ship(vid, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}
