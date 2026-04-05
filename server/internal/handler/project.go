package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type ProjectHandler struct {
	projectService *service.ProjectService
}

func NewProjectHandler(projectService *service.ProjectService) *ProjectHandler {
	return &ProjectHandler{projectService: projectService}
}

func (h *ProjectHandler) Create(c *gin.Context) {
	var req service.CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Create(userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ProjectHandler) Get(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.projectService.Get(id, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ProjectHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	projects, total, err := h.projectService.List(userID, page, pageSize)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessPaginated(c, projects, total, page, pageSize)
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req service.UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Update(id, userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ProjectHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	if err := h.projectService.Delete(id, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}
