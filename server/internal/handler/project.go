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

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Create(userID, &service.CreateProjectRequest{
		Name:                input.Name,
		Description:         api.NonEmpty(input.Description),
		RepositoryUrl:       api.NonEmpty(input.RepositoryURL),
		CodeRepositoryUrl:   api.NonEmpty(input.CodeRepositoryURL),
		PublicGithubTokenId: api.NonEmpty(input.PublicGithubTokenID),
		CodeGithubTokenId:   api.NonEmpty(input.CodeGithubTokenID),
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

// createProjectInput / updateProjectInput 保留手写绑定，原因同 updateMeInput：
// 生成类型的指针字段把显式 null 折成 nil，而 Token 引用与代码仓库字段的三态
// 语义（key 缺省保留 / 显式 null 或空串清空 / 非空替换）依赖「JSON key 是否
// 出现」的 presence 判定。create 无保留语义，可选项仍可走 string + api.NonEmpty。
type createProjectInput struct {
	Name                string `json:"name" binding:"required,min=1,max=100"`
	Description         string `json:"description"`
	RepositoryURL       string `json:"repository_url"`
	CodeRepositoryURL   string `json:"code_repository_url"`
	PublicGithubTokenID string `json:"public_github_token_id"`
	CodeGithubTokenID   string `json:"code_github_token_id"`
}

type updateProjectInput struct {
	Name                string          `json:"name" binding:"omitempty,min=1,max=100"`
	Description         *string         `json:"description"`
	RepositoryURL       string          `json:"repository_url"`
	CodeRepositoryURL   json.RawMessage `json:"code_repository_url"`
	PublicGithubTokenID json.RawMessage `json:"public_github_token_id"`
	CodeGithubTokenID   json.RawMessage `json:"code_github_token_id"`
}

// decodeOptionalStringUpdate 按 presence 语义解析可选字符串字段（Token 引用、
// 代码仓库链接）：key 缺省 → Set=false（保留现值）；显式 null 或空串 →
// Set=true、空值（清空）；非空字符串 → Set=true（替换）。类型不符返回错误。
func decodeOptionalStringUpdate(raw json.RawMessage) (service.OptionalStringUpdate, error) {
	if len(raw) == 0 {
		return service.OptionalStringUpdate{}, nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return service.OptionalStringUpdate{}, errors.New("类型无效")
	}
	if value == nil {
		return service.OptionalStringUpdate{Set: true}, nil
	}
	return service.OptionalStringUpdate{Set: true, Value: *value}, nil
}

func (h *ProjectHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var input updateProjectInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	publicTokenRef, err := decodeOptionalStringUpdate(input.PublicGithubTokenID)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: public_github_token_id 类型无效")
		return
	}
	codeTokenRef, err := decodeOptionalStringUpdate(input.CodeGithubTokenID)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: code_github_token_id 类型无效")
		return
	}
	codeRepoRef, err := decodeOptionalStringUpdate(input.CodeRepositoryURL)
	if err != nil {
		response.BadRequest(c, 40001, "请求参数无效: code_repository_url 类型无效")
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.projectService.Update(id, userID, &service.UpdateProjectRequest{
		Name:                api.NonEmpty(input.Name),
		Description:         input.Description,
		RepositoryUrl:       api.NonEmpty(input.RepositoryURL),
		CodeRepositoryUrl:   codeRepoRef,
		PublicGithubTokenId: publicTokenRef,
		CodeGithubTokenId:   codeTokenRef,
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
