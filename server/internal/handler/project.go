package handler

import (
	"encoding/json"
	"errors"
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
	var input createProjectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	prTokenSourceKind, err := decodePRTokenSourceKind(input.PRTokenSourceKind)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: pr_token_source_kind "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Create(userID, &service.CreateProjectRequest{
		Name:                   input.Name,
		Description:            api.NonEmpty(input.Description),
		RepositoryUrl:          api.NonEmpty(input.RepositoryURL),
		GithubToken:            api.NonEmpty(input.GithubToken),
		GithubPrToken:          api.NonEmpty(input.GithubPRToken),
		SourceProjectId:        api.NonEmpty(input.SourceProjectID),
		PrTokenSourceProjectId: api.NonEmpty(input.PRTokenSourceProjectID),
		PrTokenSourceKind:      prTokenSourceKind,
	})
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

// createProjectInput 与 updateProjectInput 同因手写绑定：PRTokenSourceKind 需
// 按「JSON key 出现即算提供」的 presence 语义区分显式 null 与缺省（*string 会把
// null 折成 nil）。其余可选字段沿用 string + api.NonEmpty 映射，service 端
// api.Deref 语义不变；name 的 binding 校验照抄生成类型。
type createProjectInput struct {
	Name                   string          `json:"name" binding:"required,min=1,max=100"`
	Description            string          `json:"description"`
	RepositoryURL          string          `json:"repository_url"`
	GithubToken            string          `json:"github_token"`
	GithubPRToken          string          `json:"github_pr_token"`
	SourceProjectID        string          `json:"source_project_id"`
	PRTokenSourceProjectID string          `json:"pr_token_source_project_id"`
	PRTokenSourceKind      json.RawMessage `json:"pr_token_source_kind"`
}

// updateProjectInput 保留手写绑定，各字段按所需语义选形态：
// name 沿用 string+omitempty——生成类型 *string 无法对显式空串走 omitempty
// 跳过校验（原因同 updateMeInput）；description 改为 *string 三态直传——
// 缺省与显式 null 都是 nil（保留现值），显式 "" 清空，非空替换，不经
// api.NonEmpty（它会把空串折成 nil，清空不可达）。
// GithubPRToken/ClearGithubPRToken/PRTokenSourceProjectID 用 RawMessage 旁路
// 捕获：互斥校验依赖「字段是否显式提供」，显式 null 也算提供——*string/*bool
// 会把 null 折成 nil，与缺省不可区分。PRTokenSourceKind 同法捕获 presence，
// null/类型错误在此拦 40001，取值合法性与搭配校验在 service 层。
// 模式同 updateDocumentRequest 的 parent_id。
type updateProjectInput struct {
	Name                   string          `json:"name" binding:"omitempty,min=1,max=100"`
	Description            *string         `json:"description"`
	RepositoryURL          string          `json:"repository_url"`
	GithubToken            string          `json:"github_token"`
	GithubPRToken          json.RawMessage `json:"github_pr_token"`
	ClearGithubPRToken     json.RawMessage `json:"clear_github_pr_token"`
	PRTokenSourceProjectID json.RawMessage `json:"pr_token_source_project_id"`
	PRTokenSourceKind      json.RawMessage `json:"pr_token_source_kind"`
	SourceProjectID        string          `json:"source_project_id"`
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

// decodePRTokenSourceKind 按 presence 语义解码 pr_token_source_kind：key 缺省
// 返回 (nil, nil)；key 出现（含显式 null）则必须解出非空字符串——null 与非
// 字符串都不满足「提供即须为 access/pr 合法值」。取值合法性由 service 校验。
func decodePRTokenSourceKind(raw json.RawMessage) (*api.PrTokenSourceKind, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	s, err := optionalJSONField[string](raw)
	if err != nil {
		return nil, errors.New("类型无效")
	}
	if s == nil {
		return nil, errors.New("取值无效")
	}
	kind := api.PrTokenSourceKind(*s)
	return &kind, nil
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var input updateProjectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	// 互斥按「字段是否显式提供」判定，显式 null 同样算提供。
	if len(input.ClearGithubPRToken) > 0 && (len(input.GithubPRToken) > 0 || len(input.PRTokenSourceProjectID) > 0) {
		response.BadRequest(c, 40001, "请求参数无效: clear_github_pr_token 与 github_pr_token 或 pr_token_source_project_id 不能同时提供")
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
	prTokenSource, err := optionalJSONField[string](input.PRTokenSourceProjectID)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: pr_token_source_project_id 类型无效")
		return
	}
	prTokenSourceKind, err := decodePRTokenSourceKind(input.PRTokenSourceKind)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: pr_token_source_kind "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Update(id, userID, &service.UpdateProjectRequest{
		Name:                   api.NonEmpty(input.Name),
		Description:            input.Description,
		RepositoryUrl:          api.NonEmpty(input.RepositoryURL),
		GithubToken:            api.NonEmpty(input.GithubToken),
		GithubPrToken:          prToken,
		ClearGithubPrToken:     clearPRToken,
		PrTokenSourceProjectId: prTokenSource,
		PrTokenSourceKind:      prTokenSourceKind,
		SourceProjectId:        api.NonEmpty(input.SourceProjectID),
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
