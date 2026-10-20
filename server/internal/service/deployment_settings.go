package service

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// PublicURLSource 部署 public_url 的统一读取入口（INT-129）：integration 与
// 审查派发（INT-135，1c 阶段接入）经它取值，main 统一注入
// DeploymentSettingsService；测试与既有构造路径不注入时由各 service 的
// 默认实现（只读 config）兜底。
type PublicURLSource interface {
	PublicURL(ctx context.Context) string
}

// public_url 当前生效来源，与 api.DeploymentSettingsPublicUrlSource 一致。
const (
	publicURLSourceDatabase = "database"
	publicURLSourceConfig   = "config"
	publicURLSourceNone     = "none"
)

// errPublicURLInvalid PUT 请求里的 public_url 不合法（40001）。
var errPublicURLInvalid = errs.New(errs.ErrInvalidParams.Code,
	"public_url 必须是 http(s) 绝对地址，host 非空且不带 query 或 fragment")

// DeploymentSettingsService 部署级设置（INT-129）：public_url 的网页配置与
// 唯一读取入口。解析优先级：数据库非空值 > cfg.Server.PublicURL > 空串。
type DeploymentSettingsService struct {
	repo *repository.DeploymentSettingsRepository
	cfg  *config.Config
}

func NewDeploymentSettingsService(repo *repository.DeploymentSettingsRepository, cfg *config.Config) *DeploymentSettingsService {
	return &DeploymentSettingsService{repo: repo, cfg: cfg}
}

// configPublicURLSource PublicURLSource 的缺省实现：只读 config.yaml，供三个
// 既有 service 在未被 main 注入时保持原行为（既有测试与构造调用不变）。
type configPublicURLSource struct {
	cfg *config.Config
}

func (s configPublicURLSource) PublicURL(ctx context.Context) string {
	return normalizePublicURL(s.cfg.Server.PublicURL)
}

// PublicURL 唯一读取入口：数据库非空值 > config > 空串；结果 trim 并去尾部 `/`。
// 数据库读失败静默回落 config——取不到网页配置不应让派发/投影整体失败。
func (s *DeploymentSettingsService) PublicURL(ctx context.Context) string {
	if row, err := s.repo.Get(); err == nil {
		if v := normalizePublicURL(row.PublicURL); v != "" {
			return v
		}
	}
	return normalizePublicURL(s.cfg.Server.PublicURL)
}

// Get GET /api/deployment/settings 的投影：当前生效值 + 来源。
func (s *DeploymentSettingsService) Get(ctx context.Context) (*api.DeploymentSettings, error) {
	publicURL := ""
	source := publicURLSourceNone
	var updatedAt *string

	if row, err := s.repo.Get(); err == nil {
		if v := normalizePublicURL(row.PublicURL); v != "" {
			publicURL = v
			source = publicURLSourceDatabase
		}
		updated := api.JSONTime(row.UpdatedAt)
		updatedAt = &updated
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	if source == publicURLSourceNone {
		if v := normalizePublicURL(s.cfg.Server.PublicURL); v != "" {
			publicURL = v
			source = publicURLSourceConfig
		}
	}

	return &api.DeploymentSettings{
		PublicUrl:       publicURL,
		PublicUrlSource: api.DeploymentSettingsPublicUrlSource(source),
		UpdatedAt:       updatedAt,
	}, nil
}

// Update PUT /api/deployment/settings：校验 + 落库 + 回读。空串表示清空数据库
// 值回落 config；其余必须是 http(s) 绝对 URL、host 非空、不带 query/fragment，
// 末尾 `/` 自动去掉。
func (s *DeploymentSettingsService) Update(ctx context.Context, publicURL string) (*api.DeploymentSettings, error) {
	normalized, err := validatePublicURLInput(publicURL)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Upsert(map[string]any{"public_url": normalized}); err != nil {
		return nil, errs.ErrInternal
	}
	return s.Get(ctx)
}

// validatePublicInput 校验并归一请求里的 public_url：空串合法（清空）；
// 否则必须能解析为 http(s) 绝对 URL，host 非空，不能带 query 或 fragment；
// 结果去掉尾部 `/`。
func validatePublicURLInput(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" ||
		u.RawQuery != "" ||
		u.Fragment != "" {
		return "", errPublicURLInvalid
	}
	return strings.TrimRight(raw, "/"), nil
}

// normalizePublicURL 读侧归一：trim、去尾部 `/`。空串原样返回。
func normalizePublicURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
