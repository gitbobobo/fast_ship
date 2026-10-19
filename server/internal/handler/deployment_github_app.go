package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// DeploymentGitHubAppHandler 部署级 GitHub App（INT-129）：GET 所有登录用户
// 可读（成员页也要显示安装入口），写入/检测/Manifest 仅管理员（路由层
// RequireAdmin）。
type DeploymentGitHubAppHandler struct {
	svc      *service.IntegrationService
	manifest *service.GitHubAppManifestService
}

func NewDeploymentGitHubAppHandler(svc *service.IntegrationService, manifest *service.GitHubAppManifestService) *DeploymentGitHubAppHandler {
	return &DeploymentGitHubAppHandler{svc: svc, manifest: manifest}
}

// Get GET /api/deployment/github-app
func (h *DeploymentGitHubAppHandler) Get(c *gin.Context) {
	out, err := h.svc.Get(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Update PUT /api/deployment/github-app
func (h *DeploymentGitHubAppHandler) Update(c *gin.Context) {
	var req api.UpdateGitHubAppDeploymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.Update(c.Request.Context(), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Test POST /api/deployment/github-app/test
func (h *DeploymentGitHubAppHandler) Test(c *gin.Context) {
	out, err := h.svc.Test(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// CreateManifest POST /api/deployment/github-app/manifest
func (h *DeploymentGitHubAppHandler) CreateManifest(c *gin.Context) {
	var req api.GitHubAppManifestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.manifest.Create(c.Request.Context(), middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// CompleteManifest POST /api/deployment/github-app/manifest/complete：GitHub
// 回跳后前端把 code/state 交上来；成功后返回最新配置投影。
func (h *DeploymentGitHubAppHandler) CompleteManifest(c *gin.Context) {
	var req api.CompleteGitHubAppManifestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	if err := h.manifest.Complete(c.Request.Context(), middleware.GetUserID(c), &req); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	out, err := h.svc.Get(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}
