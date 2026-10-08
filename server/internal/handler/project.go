package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
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

// updateProjectInput 保留基线的 string+omitempty 绑定语义，原因同
// updateMeInput：生成类型 *string 无法对显式空串走 omitempty 跳过。
// GithubPRToken/ClearGithubPRToken 用指针绑定：互斥校验依赖「字段是否显式提供」，
// string+omitempty 折回会丢失这个信息（false/空串与缺省不可区分）。两者没有
// binding 校验标签，不受 string+omitempty 例外约束。
type updateProjectInput struct {
	Name               string  `json:"name" binding:"omitempty,min=1,max=100"`
	Description        string  `json:"description"`
	RepositoryURL      string  `json:"repository_url"`
	GithubToken        string  `json:"github_token"`
	GithubPRToken      *string `json:"github_pr_token"`
	ClearGithubPRToken *bool   `json:"clear_github_pr_token"`
	SourceProjectID    string  `json:"source_project_id"`
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var input updateProjectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Update(id, userID, &service.UpdateProjectRequest{
		Name:               api.NonEmpty(input.Name),
		Description:        api.NonEmpty(input.Description),
		RepositoryUrl:      api.NonEmpty(input.RepositoryURL),
		GithubToken:        api.NonEmpty(input.GithubToken),
		GithubPrToken:      input.GithubPRToken,
		ClearGithubPrToken: input.ClearGithubPRToken,
		SourceProjectId:    api.NonEmpty(input.SourceProjectID),
	})
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

	response.SuccessEmpty(c)
}

func (h *ProjectHandler) GetBranches(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	branches, defaultBranch, err := h.projectService.GetBranches(c.Request.Context(), id, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, api.GetProjectBranches200JSONResponseBody_Data{
		Branches:      branches,
		DefaultBranch: defaultBranch,
	})
}
