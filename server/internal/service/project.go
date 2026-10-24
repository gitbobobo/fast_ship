package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var repoNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// ProjectReviewCleaner 删除项目前清理审查远端资源（线程/工作区）的出口；
// 由 ReviewService 实现，main 注入。nil 时跳过（测试与未启用审查的部署）。
type ProjectReviewCleaner interface {
	CleanupBeforeProjectDelete(ctx context.Context, projectID string)
}

type ProjectService struct {
	projectRepo   *repository.ProjectRepository
	versionRepo   *repository.VersionRepository
	syncStateRepo *repository.IssueSyncStateRepository
	storage       storage.Storage
	credentials   *ProjectCredentials
	newClient     ghclient.ClientFactory
	reviewCleaner ProjectReviewCleaner
	// forgeIntegrations 部署级代码仓集成查找（INT-166）：仓库 URL 的非
	// github 主机按 base_url 匹配 forgejo 集成。nil 时视为「没有已配置集成」，
	// 非 github 主机一律报未匹配。
	forgeIntegrations *repository.ForgeIntegrationRepository
}

// SetReviewCleaner 绑定删除项目前的审查远端清理（main.go 注入）。
func (s *ProjectService) SetReviewCleaner(c ProjectReviewCleaner) { s.reviewCleaner = c }

func NewProjectService(
	projectRepo *repository.ProjectRepository,
	versionRepo *repository.VersionRepository,
	syncStateRepo *repository.IssueSyncStateRepository,
	tokenRepo *repository.UserGitHubTokenRepository,
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
	forgeIntegrations *repository.ForgeIntegrationRepository,
) *ProjectService {
	return &ProjectService{
		projectRepo:       projectRepo,
		versionRepo:       versionRepo,
		syncStateRepo:     syncStateRepo,
		storage:           storage,
		credentials:       newProjectCredentials(projectRepo, tokenRepo, cfg, logger),
		forgeIntegrations: forgeIntegrations,
		newClient: func(token, owner, repo string) ghclient.Client {
			return ghclient.NewClient(token, owner, repo)
		},
	}
}

func (s *ProjectService) Create(userID string, req *CreateProjectRequest) (*ProjectResponse, error) {
	exists, err := s.projectRepo.ExistsByName(userID, req.Name)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrProjectNameExists
	}

	var owner, repo string
	if repositoryURL := api.Deref(req.RepositoryUrl); repositoryURL != "" {
		var appErr *errs.AppError
		owner, repo, _, appErr = s.parseRepoBinding(repositoryURL, true)
		if appErr != nil {
			return nil, appErr
		}
	}

	// 代码仓库（PR 审查用）：解析后落 code 字段并按 host 派生集成绑定；与
	// 公开仓库相同的归一在 normalizeCodeRepo 统一做（与 Update 同一条规则）。
	var codeOwner, codeRepo, codeIntegrationID string
	if codeURL := api.Deref(req.CodeRepositoryUrl); codeURL != "" {
		var appErr *errs.AppError
		codeOwner, codeRepo, codeIntegrationID, appErr = s.parseRepoBinding(codeURL, false)
		if appErr != nil {
			return nil, appErr
		}
	}

	// 引用必须属于当前用户；仓库与 Token 相互独立，可只填仓库不选 Token。
	publicTokenID := api.Deref(req.PublicGithubTokenId)
	codeTokenID := api.Deref(req.CodeGithubTokenId)
	if appErr := s.credentials.CheckTokenReference(userID, publicTokenID); appErr != nil {
		return nil, appErr
	}
	if appErr := s.credentials.CheckTokenReference(userID, codeTokenID); appErr != nil {
		return nil, appErr
	}

	project := &model.Project{
		ID:                  uuid.New().String(),
		UserID:              userID,
		Name:                req.Name,
		Description:         api.Deref(req.Description),
		GithubOwner:         owner,
		GithubRepo:          repo,
		CodeGithubOwner:     codeOwner,
		CodeGithubRepo:      codeRepo,
		CodeIntegrationID:   codeIntegrationID,
		PublicGitHubTokenID: publicTokenID,
		CodeGitHubTokenID:   codeTokenID,
	}

	normalizeCodeRepo(project)

	if err := s.projectRepo.Create(project); err != nil {
		if errors.Is(err, repository.ErrGitHubTokenMissing) {
			return nil, errs.ErrGitHubTokenNotFound
		}
		return nil, errs.ErrInternal
	}

	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

func (s *ProjectService) Get(id, userID string) (*ProjectResponse, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

// List 分页返回项目；includeShelved 控制是否包含搁置中的项目（INT-165）。
// HTTP GET /api/projects 传 true（搁置项目照常列出），fs_project list 缺省
// 排除搁置项目、include_shelved=true 时传 true。
func (s *ProjectService) List(userID string, page, pageSize int, includeShelved bool) ([]ProjectResponse, int64, error) {
	projects, total, err := s.projectRepo.List(userID, page, pageSize, includeShelved)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	// 列表项附带 issue_count（Issue 总数，不分状态）与 issue_workflow_counts
	// （仅 state=open 的 Issue 按内部状态分组）；无 Issue 的项目两者都保持缺省
	projectIDs := make([]string, len(projects))
	for i, p := range projects {
		projectIDs[i] = p.ID
	}
	issueCounts, err := s.projectRepo.GetIssueCounts(projectIDs)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}
	latestVersions, err := s.versionRepo.GetLatestByProjectIDs(projectIDs)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}
	// 同步状态一次批量补齐+取回；失败时整页缺省（旧实现按项目容错跳过，
	// 批量化后粒度放大到整页，语义同为「不拖垮列表」）。
	var syncStates map[string]*model.IssueSyncState
	if s.syncStateRepo != nil {
		if states, err := s.syncStateRepo.GetOrCreateByProjectIDs(projectIDs); err == nil {
			syncStates = states
		}
	}

	integrations := s.forgeIntegrationsByIDs(projects)
	resp := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		projectResp := s.toResponseWithIntegrations(&p, syncStates[p.ID], integrations)
		if count, ok := issueCounts[p.ID]; ok {
			projectResp.IssueCount = &count.Total
			projectResp.IssueWorkflowCounts = &api.IssueWorkflowCounts{
				Unset:      count.Unset,
				Todo:       count.Todo,
				InProgress: count.InProgress,
				Done:       count.Done,
			}
		}
		if latest, ok := latestVersions[p.ID]; ok {
			projectResp.LatestVersion = &LatestVersionResponse{
				Id:            latest.ID,
				VersionNumber: latest.VersionNumber,
				Status:        latest.Status,
				CreatedAt:     latest.CreatedAt.Format("2006-01-02T15:04:05Z"),
			}
		}
		resp[i] = *projectResp
	}
	return resp, total, nil
}

// OptionalStringUpdate 表达项目更新请求里可选字符串字段的三态：
// Set=false（JSON key 缺省）保留现值；Set=true 且 Value 为空（显式 null 或空串）
// 清空；Set=true 且 Value 非空替换。*string 表达不了三态——显式 null 与 key
// 缺省解引用后都是 nil，而两者语义相反（清空 vs 保留）。
type OptionalStringUpdate struct {
	Set   bool
	Value string
}

// TokenRefUpdate 是 OptionalStringUpdate 的旧名（Token 引用字段最先用它），
// 保留别名避免改动既有调用点。
type TokenRefUpdate = OptionalStringUpdate

// UpdateProjectRequest 是项目更新的 service 入参，不再别名到 api.UpdateProjectRequest：
// Token 引用与代码仓库字段需三态（见 OptionalStringUpdate），其余字段沿用生成
// 类型的指针语义；handler 的手写 updateProjectInput 负责把 RawMessage 旁路解析
// 成这里的三态。
type UpdateProjectRequest struct {
	Name                *string
	Description         *string
	RepositoryUrl       *string
	CodeRepositoryUrl   OptionalStringUpdate
	PublicGithubTokenId OptionalStringUpdate
	CodeGithubTokenId   OptionalStringUpdate
}

func (s *ProjectService) Update(id, userID string, req *UpdateProjectRequest) (*ProjectResponse, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	if name := api.Deref(req.Name); name != "" && name != project.Name {
		exists, err := s.projectRepo.ExistsByNameExcludeID(userID, name, id)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrProjectNameExists
		}
		project.Name = name
	}

	// description 按指针三态：nil 保留现值、指向空串清空、指向非空替换。
	if req.Description != nil {
		project.Description = *req.Description
	}

	if repositoryURL := api.Deref(req.RepositoryUrl); repositoryURL != "" {
		owner, repo, _, appErr := s.parseRepoBinding(repositoryURL, true)
		if appErr != nil {
			return nil, appErr
		}
		project.GithubOwner = owner
		project.GithubRepo = repo
		// 公开仓恒为 GitHub（public_integration_id 保持空列），无需改动。
	}

	// 代码仓库三态：key 缺省保留、显式空清空（回落公开仓库，集成绑定一并
	// 清）、非空替换并按新 host 重新派生集成绑定。与公开仓库相同的归一在
	// 下面应用完全部仓库字段后统一做。
	if req.CodeRepositoryUrl.Set {
		if req.CodeRepositoryUrl.Value != "" {
			owner, repo, integrationID, appErr := s.parseRepoBinding(req.CodeRepositoryUrl.Value, false)
			if appErr != nil {
				return nil, appErr
			}
			project.CodeGithubOwner = owner
			project.CodeGithubRepo = repo
			project.CodeIntegrationID = integrationID
		} else {
			project.CodeGithubOwner = ""
			project.CodeGithubRepo = ""
			project.CodeIntegrationID = ""
		}
	}

	// 归一收尾：公开与代码仓库字段都已应用完（含请求不带 code key、只改
	// 公开仓库撞上存量 code 仓库的情形），同一规则统一判一次。
	normalizeCodeRepo(project)

	// Token 引用三态：key 缺省保留、显式空清空、非空替换（须属于当前用户）。
	if req.PublicGithubTokenId.Set {
		if appErr := s.credentials.CheckTokenReference(userID, req.PublicGithubTokenId.Value); appErr != nil {
			return nil, appErr
		}
		project.PublicGitHubTokenID = req.PublicGithubTokenId.Value
	}
	if req.CodeGithubTokenId.Set {
		if appErr := s.credentials.CheckTokenReference(userID, req.CodeGithubTokenId.Value); appErr != nil {
			return nil, appErr
		}
		project.CodeGitHubTokenID = req.CodeGithubTokenId.Value
	}

	if err := s.projectRepo.Update(project); err != nil {
		if errors.Is(err, repository.ErrGitHubTokenMissing) {
			return nil, errs.ErrGitHubTokenNotFound
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

func (s *ProjectService) Delete(id, userID string) error {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}

	// 项目删除会 CASCADE 掉审查运行行；先尽力停掉远端线程并回收工作区，
	// 否则这些线程/工作区再也不会被调度发现（归档与回收都按运行行驱动）。
	if s.reviewCleaner != nil {
		s.reviewCleaner.CleanupBeforeProjectDelete(context.Background(), project.ID)
	}
	if err := s.projectRepo.Delete(id, userID); err != nil {
		return errs.ErrInternal
	}

	_ = s.storage.DeletePrefix(project.ID)
	return nil
}

// Shelve 搁置项目（INT-165）：写 shelved_at 与可选的 shelved_until（RFC3339，
// 必须晚于当前时间，否则 40001）；until 缺省表示无限期搁置。重复搁置按覆盖
// 处理（重新计时/改期）。搁置不拦截任何读写，只影响聚合面（dashboard、
// 推荐汇总、fs_project list 的默认口径）。
func (s *ProjectService) Shelve(id, userID string, req *api.ShelveProjectRequest) (*ProjectResponse, error) {
	if _, err := s.projectRepo.FindByID(id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	var until *time.Time
	if req != nil && req.Until != nil {
		parsed, err := time.Parse(time.RFC3339, *req.Until)
		if err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, "until 必须是 RFC3339 时间")
		}
		parsed = parsed.UTC()
		if !parsed.After(time.Now()) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "until 必须晚于当前时间")
		}
		until = &parsed
	}

	now := time.Now().UTC()
	if err := s.projectRepo.Shelve(id, userID, &now, until); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.shelveResponse(id, userID)
}

// Unshelve 取消搁置（INT-165）：清空 shelved_at/shelved_until 并刷新
// updated_at；幂等，未搁置的项目同样返回成功。到期自动恢复没有定时任务，
// 全部恢复动作（手动取消与到期）都表现为「字段为空，读取侧不再判搁置」。
func (s *ProjectService) Unshelve(id, userID string) (*ProjectResponse, error) {
	if err := s.projectRepo.Unshelve(id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.shelveResponse(id, userID)
}

// shelveResponse 回读更新后的行组装响应，与 Get 同一出口。
func (s *ProjectService) shelveResponse(id, userID string) (*ProjectResponse, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.toResponse(project, s.syncStateFor(project.ID)), nil
}

// GetBranches fetches all branches from the project's GitHub repository.
func (s *ProjectService) GetBranches(ctx context.Context, id, userID string) ([]BranchResponse, string, error) {
	project, err := s.projectRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrProjectNotFound
		}
		return nil, "", errs.ErrInternal
	}

	tokenBytes, appErr := s.credentials.requiredGitHubToken(project)
	if appErr != nil {
		return nil, "", appErr
	}

	// Create GitHub client and fetch branches.
	ghClient := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	branches, defaultBranch, err := ghClient.ListBranches(ctx)
	if err != nil {
		return nil, "", errs.New(50200, fmt.Sprintf("Failed to fetch branches: %v", err))
	}

	// Convert to response format
	resp := make([]BranchResponse, len(branches))
	for i, b := range branches {
		resp[i] = BranchResponse{
			Name:    b.Name,
			Sha:     b.SHA,
			Default: b.Default,
		}
	}

	return resp, defaultBranch, nil
}

// normalizeCodeRepo 归一收尾：GitHub 代码仓库与公开仓库大小写不敏感相同时
// 清空 code 字段（空 = 同公开仓库，规范存储不存冗余值）。仅对未绑定集成的
// （GitHub）代码仓生效——forgejo 代码仓与 github 公开仓的 owner/repo 相同也
// 不是同一仓库，归一会丢掉 forgejo 绑定。必须在应用完全部仓库字段之后调用，
// Create/Update 共用这一条规则，写侧不另设第二处归一判断。
func normalizeCodeRepo(p *model.Project) {
	if p.CodeIntegrationID != "" {
		return
	}
	if p.CodeGithubOwner != "" && strings.EqualFold(
		p.CodeGithubOwner+"/"+p.CodeGithubRepo, p.GithubOwner+"/"+p.GithubRepo) {
		p.CodeGithubOwner, p.CodeGithubRepo = "", ""
	}
}

// repoHostGitHub 内置 GitHub 集成的归一主机。
const repoHostGitHub = "github.com"

// parseRepositoryURL 解析仓库地址，返回归一 host（小写；URL 带端口时保留
// host:port 供集成匹配）、owner 与 repo。接受形态：
//   - owner/repo（纯 owner/repo 视为内置 GitHub）；
//   - github.com/o/r、https://github.com/o/r、http://github.com/o/r（GitHub
//     所有 scheme 一律当 github 处理，行为兼容旧实现）；
//   - host/o/r、https://host/o/r、https://host:port/o/r（自建 Forgejo 等）。
//
// host 判定：无 scheme 时首段含 `.` 或 `:` 视为 host，否则视为 owner；带
// scheme 时首段一律是 host。`.git` 后缀、尾斜杠、host 大小写不敏感的修剪
// 顺序与旧实现一致。
func parseRepositoryURL(raw string) (host, owner, repo string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", "", errors.New("仓库链接不能为空")
	}

	scheme := ""
	if idx := strings.Index(s, "://"); idx != -1 {
		scheme = strings.ToLower(s[:idx])
		s = s[idx+3:]
	}

	parts := strings.SplitN(s, "/", 3)
	first := parts[0]
	// 无 scheme 时首段含 `.` 或 `:` 才是 host（owner 名可能带点，但纯
	// owner/repo 永远不含端口分隔符）；有 scheme 的 URL 首段必是 host。
	// host 形态剥掉首段，owner/repo 形态整体保留。
	if scheme == "" && !strings.ContainsAny(first, ".:") {
		host = repoHostGitHub
	} else {
		host = strings.ToLower(first)
		if len(first) < len(s) {
			s = s[len(first)+1:] // 连 host 后的 "/" 一起剥掉
		} else {
			s = ""
		}
		// 无显式端口的非 github 主机按 scheme 补默认端口（http→80，其余→
		// 443），与集成 base_url 的归一（forgeBaseHostKey）同口径：http
		// 集成只有显式写 http:// 的仓库 URL 才匹配得上。github.com 的任何
		// scheme/默认端口写法都归一回裸主机名（= 内置 github-default）。
		if _, _, err := net.SplitHostPort(host); err != nil {
			if scheme == "http" {
				host += ":80"
			} else {
				host += ":443"
			}
		}
		if host == repoHostGitHub+":443" || host == repoHostGitHub+":80" {
			host = repoHostGitHub
		}
	}

	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	parts = strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return host, "", "", errors.New("仓库链接格式无效，应为 owner/repo 或 https://github.com/owner/repo")
	}

	owner = strings.TrimSpace(parts[0])
	repo = strings.TrimSpace(parts[1])

	if owner == "" || repo == "" {
		return host, "", "", errors.New("仓库链接格式无效，owner 和 repo 不能为空")
	}

	if !repoNameRegex.MatchString(owner) || !repoNameRegex.MatchString(repo) {
		return host, "", "", errors.New("仓库链接包含非法字符")
	}

	return host, owner, repo, nil
}

// repoHostKey 仓库 URL 主机的归一键：小写 host + 端口；无端口按 https 缺省
// 443（无 scheme 的裸形态与 https 同口径）。带端口的输入经 SplitHostPort
// 归一（顺带剥掉 IPv6 字面量的方括号，与 forgeBaseHostKey 同形态）。
func repoHostKey(host string) string {
	if h, port, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(h) + ":" + port
	}
	return strings.ToLower(host) + ":443"
}

// forgeBaseHostKey 集成 base_url 的归一键：小写 host + 端口，空端口按 scheme
// 缺省补齐（https→443、http→80）。解析失败返回空串（保存时已校验过，这里
// 只防御存量脏行参与比较）。
func forgeBaseHostKey(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		return host + ":" + port
	}
	if strings.EqualFold(u.Scheme, "http") {
		return host + ":80"
	}
	return host + ":443"
}

// parseRepoBinding 解析仓库地址并按用途派生集成绑定（INT-166）：GitHub 主机
// （或纯 owner/repo）返回空集成 id（= 内置 github-default）；其余主机必须命中
// 一条已配置 forgejo 集成并返回其 id，否则报「主机未匹配任何已配置集成」，
// 不再像旧实现那样把 host 静默解析成 owner。forPublic=true 时非 github.com
// 主机一律拒绝——公开仓的 issue 镜像与发文本期仍是 GitHub only，命中
// forgejo 集成的报错会点明这一层。
func (s *ProjectService) parseRepoBinding(raw string, forPublic bool) (owner, repo, integrationID string, appErr *errs.AppError) {
	host, owner, repo, err := parseRepositoryURL(raw)
	if err != nil {
		return "", "", "", errs.New(errs.ErrInvalidParams.Code, errs.ErrInvalidParams.Message+": "+err.Error())
	}
	if host == repoHostGitHub {
		return owner, repo, "", nil
	}
	id, found, matchErr := s.forgeIntegrationByHost(host)
	if matchErr != nil {
		return "", "", "", errs.ErrInternal
	}
	if forPublic {
		if found {
			return "", "", "", errs.New(errs.ErrInvalidParams.Code,
				errs.ErrInvalidParams.Message+": 公开仓库仅支持 GitHub，"+host+" 是已配置的 Forgejo 集成（issue 镜像与发版仍只支持 GitHub）")
		}
		return "", "", "", errRepoHostUnmatched(host)
	}
	if !found {
		return "", "", "", errRepoHostUnmatched(host)
	}
	return owner, repo, id, nil
}

// forgeIntegrationByHost 按 base_url 主机匹配已配置的 forgejo 集成（INT-166）。
// 双方都归一成「小写 host + 端口（空端口按 scheme 缺省 443/80）」后比较。
// 集成仓库未注入（部分测试）视为没有已配置集成；查询失败按错误上抛，不用
// 「未匹配」顶替真实故障。
func (s *ProjectService) forgeIntegrationByHost(host string) (id string, found bool, err error) {
	if s.forgeIntegrations == nil {
		return "", false, nil
	}
	rows, listErr := s.forgeIntegrations.List()
	if listErr != nil {
		return "", false, listErr
	}
	key := repoHostKey(host)
	for i := range rows {
		row := &rows[i]
		if row.Kind != model.ForgeIntegrationKindForgejo {
			continue
		}
		if forgeBaseHostKey(row.BaseURL) == key {
			return row.ID, true, nil
		}
	}
	return "", false, nil
}

// errRepoHostUnmatched 仓库 URL 主机未命中任何已配置集成（40001 系）。
func errRepoHostUnmatched(host string) *errs.AppError {
	return errs.New(errs.ErrInvalidParams.Code,
		errs.ErrInvalidParams.Message+": 仓库主机 "+host+" 未匹配任何已配置集成")
}

// toResponse 组装项目响应；syncState 由调用方提供——单项目路径用
// syncStateFor 现取，列表路径经 GetOrCreateByProjectIDs 批量预取后逐条传入，
// nil 表示同步状态缺省。is_shelved 按响应生成时刻判定（INT-165）：到期
// 自动恢复没有定时任务，同一行在截止前后两次读取会得到不同的 is_shelved。
func (s *ProjectService) toResponse(p *model.Project, syncState *model.IssueSyncState) *ProjectResponse {
	return s.toResponseWithIntegrations(p, syncState, nil)
}

// toResponseWithIntegrations 带预取集成行的响应组装：列表路径一次批量取回
// 全部被引用集成（避免 N+1）；integrations 传 nil 时单项目路径现场查一次。
func (s *ProjectService) toResponseWithIntegrations(p *model.Project, syncState *model.IssueSyncState, integrations map[string]model.ForgeIntegration) *ProjectResponse {
	resp := &ProjectResponse{
		Id:                  p.ID,
		Name:                p.Name,
		Description:         p.Description,
		GithubOwner:         p.GithubOwner,
		GithubRepo:          p.GithubRepo,
		CodeGithubOwner:     p.CodeGithubOwner,
		CodeGithubRepo:      p.CodeGithubRepo,
		PublicGithubTokenId: p.PublicGitHubTokenID,
		CodeGithubTokenId:   p.CodeGitHubTokenID,
		// 集成引用（INT-166）：空列映射为 null（= 内置 github-default）；写入
		// 接线由后续任务完成，本期只随响应带出。
		CodeIntegrationId:   nilIfEmpty(p.CodeIntegrationID),
		PublicIntegrationId: nilIfEmpty(p.PublicIntegrationID),
		ShelvedAt:           formatTimePtr(p.ShelvedAt),
		ShelvedUntil:        formatTimePtr(p.ShelvedUntil),
		IsShelved:           p.IsShelved(time.Now()),
		CreatedAt:           p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:           p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	resp.CodeRepoUrl = s.codeRepoUrl(p, integrations)

	if syncState != nil {
		resp.IssueSync = &api.IssueSyncState{
			Status:    syncState.Status,
			LastError: syncState.LastError,
		}
		if syncState.LastIssueUpdatedAt != nil {
			value := syncState.LastIssueUpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
			resp.IssueSync.LastIssueUpdatedAt = &value
		}
		if syncState.LastSyncedAt != nil {
			value := syncState.LastSyncedAt.UTC().Format("2006-01-02T15:04:05Z")
			resp.IssueSync.LastSyncedAt = &value
		}
		if syncState.LastSuccessfulSyncAt != nil {
			value := syncState.LastSuccessfulSyncAt.UTC().Format("2006-01-02T15:04:05Z")
			resp.IssueSync.LastSuccessfulSyncAt = &value
		}
	}

	return resp
}

// syncStateFor 单项目路径现取同步状态；repo 未挂载或读取失败时容错为 nil。
func (s *ProjectService) syncStateFor(projectID string) *model.IssueSyncState {
	if s.syncStateRepo == nil {
		return nil
	}
	state, err := s.syncStateRepo.GetOrCreate(projectID)
	if err != nil {
		return nil
	}
	return state
}

// nilIfEmpty 空串映射为 nil：集成引用列的空值（= 内置 github-default）在
// 项目响应里统一为 null，与「显式引用了某个集成」区分开。
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// codeRepoUrl 项目代码仓的页面地址（INT-166）：无代码仓（owner/repo 缺一）
// 返回 nil；空集成引用按内置 github 拼 https://github.com/{o}/{r}；forgejo
// 集成拼 {base_url}/{o}/{r}。绑定的集成行查不到（并发删除窗口 / 查询失败 /
// repo 未注入）返回 nil——不把私有 owner/repo 发到 github.com。
func (s *ProjectService) codeRepoUrl(p *model.Project, integrations map[string]model.ForgeIntegration) *string {
	owner, repo := p.CodeRepo()
	if owner == "" || repo == "" {
		return nil
	}
	if p.CodeIntegrationID == "" {
		url := "https://github.com/" + owner + "/" + repo
		return &url
	}
	row, ok := integrations[p.CodeIntegrationID]
	if !ok {
		if integrations != nil || s.forgeIntegrations == nil {
			return nil
		}
		found, err := s.forgeIntegrations.FindByID(p.CodeIntegrationID)
		if err != nil {
			return nil
		}
		row = *found
	}
	base := strings.TrimRight(strings.TrimSpace(row.BaseURL), "/")
	if base == "" {
		return nil
	}
	if row.Kind == model.ForgeIntegrationKindGitHub {
		base = "https://github.com"
	}
	url := base + "/" + owner + "/" + repo
	return &url
}

// forgeIntegrationsByIDs 批量取项目引用的集成行：列表路径给 codeRepoUrl 用，
// 一次查询避免 N+1。repo 未注入或查询失败返回空表——响应按「集成不可解析」
// 降级为 null，不拖垮列表。
func (s *ProjectService) forgeIntegrationsByIDs(projects []model.Project) map[string]model.ForgeIntegration {
	out := map[string]model.ForgeIntegration{}
	if s.forgeIntegrations == nil {
		return out
	}
	ids := make([]string, 0, len(projects))
	for i := range projects {
		if projects[i].CodeIntegrationID != "" {
			ids = append(ids, projects[i].CodeIntegrationID)
		}
	}
	rows, err := s.forgeIntegrations.ListByIDs(ids)
	if err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}
