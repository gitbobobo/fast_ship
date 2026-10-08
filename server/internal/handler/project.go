package handler

import (
	"encoding/json"
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
// GithubPRToken/ClearGithubPRToken 用 RawMessage 旁路捕获：互斥校验依赖
// 「字段是否显式提供」，显式 null 也算提供——*string/*bool 会把 null 折成
// nil，与缺省不可区分。模式同 updateDocumentRequest 的 parent_id。
type updateProjectInput struct {
	Name               string          `json:"name" binding:"omitempty,min=1,max=100"`
	Description        string          `json:"description"`
	RepositoryURL      string          `json:"repository_url"`
	GithubToken        string          `json:"github_token"`
	GithubPRToken      json.RawMessage `json:"github_pr_token"`
	ClearGithubPRToken json.RawMessage `json:"clear_github_pr_token"`
	SourceProjectID    string          `json:"source_project_id"`
}

// optionalJSONField 把 RawMessage 解成指针：缺省与显式 null 都得到 nil（即不动作），
// 其余按目标类型解码，类型不符返回错误。
func optionalJSONField[T any](raw json.RawMessage) (*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v *T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var input updateProjectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	// 互斥按「字段是否显式提供」判定，显式 null 同样算提供。
	if len(input.GithubPRToken) > 0 && len(input.ClearGithubPRToken) > 0 {
		response.BadRequest(c, 40001, "请求参数无效: clear_github_pr_token 与 github_pr_token 不能同时提供")
		return
	}
	prToken, err := optionalJSONField[string](input.GithubPRToken)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: github_pr_token 类型无效")
		return
	}
	clearPRToken, err := optionalJSONField[bool](input.ClearGithubPRToken)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: clear_github_pr_token 类型无效")
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Update(id, userID, &service.UpdateProjectRequest{
		Name:               api.NonEmpty(input.Name),
		Description:        api.NonEmpty(input.Description),
		RepositoryUrl:      api.NonEmpty(input.RepositoryURL),
		GithubToken:        api.NonEmpty(input.GithubToken),
		GithubPrToken:      prToken,
		ClearGithubPrToken: clearPRToken,
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
