package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ForgeIntegrationService 部署级代码仓集成（INT-166）：forgejo 行的 CRUD 与
// 连接检测，加内置 github-default 的只读投影。凭证加密落库只回 *_set 标记；
// 基址在保存时做 SSRF 校验（含 DNS 解析），连接检测按集成现建 forges client。
// github-default 不在本表存储——读侧映射 IntegrationService 的部署级 GitHub App
// 身份，写侧一律拒绝。
type ForgeIntegrationService struct {
	repo   *repository.ForgeIntegrationRepository
	github *IntegrationService
	cfg    *config.Config
	logger *zap.Logger
	// probe 连接检测入口，main 缺省接 probeForge；测试注入 fake。
	probe func(ctx context.Context, client *http.Client, apiBaseURL, token string) ForgeProbe
	// validateBaseURL 基址校验入口，main 缺省接 forges.ValidateBaseURL；测试
	// 注入假的以避开真实 DNS。
	validateBaseURL func(ctx context.Context, raw string, allowPrivateNetwork bool) (string, error)
}

// ForgeProbe 一次连接检测的结论：status 沿用 model.DeploymentDetection* 口径
// （unchecked/ok/invalid/failed），version 与 username 是检测成功带回的实例
// 版本与机器人账号名，detail 是给人看的补充说明（不含任何密钥）。
type ForgeProbe struct {
	Status   string
	Version  string
	Username string
	Detail   string
}

var (
	errForgeIntegrationNameRequired  = errs.New(40001, "集成名称必填")
	errForgeIntegrationBaseRequired  = errs.New(40001, "base_url 必填")
	errForgeIntegrationTokenRequired = errs.New(40001, "token（仓库范围 PAT）必填")
	// errForgeIntegrationBuiltin 内置集成为只读映射：修改与删除都引导回既有的
	// deployment/github-app 端点。
	errForgeIntegrationBuiltin = errs.New(40001, "内置 GitHub 集成是只读映射，请通过 /api/deployment/github-app 端点管理")
	errForgeIntegrationDecrypt = errs.New(errs.ErrInternal.Code, "集成凭证解密失败（密钥轮换或数据损坏），请在编辑集成中重新保存访问令牌后重试")
)

func NewForgeIntegrationService(repo *repository.ForgeIntegrationRepository, github *IntegrationService, cfg *config.Config, logger *zap.Logger) *ForgeIntegrationService {
	return &ForgeIntegrationService{
		repo:            repo,
		github:          github,
		cfg:             cfg,
		logger:          logger,
		probe:           probeForge,
		validateBaseURL: forges.ValidateBaseURL,
	}
}

// SetProbe 注入连接检测函数（测试用）。
func (s *ForgeIntegrationService) SetProbe(fn func(ctx context.Context, client *http.Client, apiBaseURL, token string) ForgeProbe) {
	s.probe = fn
}

// SetValidateBaseURL 注入基址校验函数（测试用，避开真实 DNS）。
func (s *ForgeIntegrationService) SetValidateBaseURL(fn func(ctx context.Context, raw string, allowPrivateNetwork bool) (string, error)) {
	s.validateBaseURL = fn
}

// unavailableTrue 内置占位条目的不可用标记（取地址用的包级变量）。
var unavailableTrue = true

// List GET /api/deployment/integrations：首条恒为内置 github-default，后接
// forgejo 行（按创建顺序）。内置投影失败（GitHub App 部署设置行解密异常等
// 与 forgejo 无关的故障）不静默少行也不拖垮整表：列一条
// unavailable=true 的占位条目并记日志——管理员能区分「没有内置集成」与
// 「App 密文解不开」。
func (s *ForgeIntegrationService) List(ctx context.Context) ([]api.ForgeIntegration, error) {
	builtin, err := s.builtinProjection(ctx)
	if err != nil {
		s.logger.Error("内置 github-default 集成投影失败，列表降级为不可用占位", zap.Error(err))
	}
	rows, err := s.repo.List()
	if err != nil {
		s.logger.Error("读取代码集成列表失败", zap.Error(err))
		return nil, errs.ErrInternal
	}
	out := make([]api.ForgeIntegration, 0, len(rows)+1)
	if builtin != nil {
		out = append(out, *builtin)
	} else {
		// 投影失败不静默少行：占位条目标记 unavailable——管理员要能区分
		// 「没有内置集成」与「App 密文解不开」，凭证状态一律不可信。
		out = append(out, api.ForgeIntegration{
			Id:          model.ForgeIntegrationBuiltinGitHubID,
			Kind:        api.Github,
			Name:        "GitHub（内置）",
			BaseUrl:     githubWebBase(s.cfg),
			Builtin:     true,
			Unavailable: &unavailableTrue,
		})
	}
	for i := range rows {
		out = append(out, *projectForgeRow(&rows[i]))
	}
	return out, nil
}

// Get GET /api/deployment/integrations/:id
func (s *ForgeIntegrationService) Get(ctx context.Context, id string) (*api.ForgeIntegration, error) {
	if id == model.ForgeIntegrationBuiltinGitHubID {
		return s.builtinProjection(ctx)
	}
	row, err := s.repo.FindByID(id)
	if err != nil {
		return nil, s.notFound(err)
	}
	return projectForgeRow(row), nil
}

// Create POST /api/deployment/integrations：本期 kind 固定 forgejo（请求不设
// kind 字段——内置 GitHub App 走 deployment/github-app 端点）。
func (s *ForgeIntegrationService) Create(ctx context.Context, req *api.CreateForgeIntegrationRequest) (*api.ForgeIntegration, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errForgeIntegrationNameRequired
	}
	if strings.TrimSpace(req.BaseUrl) == "" {
		return nil, errForgeIntegrationBaseRequired
	}
	allowPrivate := req.AllowPrivateNetwork != nil && *req.AllowPrivateNetwork
	if strings.TrimSpace(req.Token) == "" {
		return nil, errForgeIntegrationTokenRequired
	}

	// 合并成最终值后统一校验：base 与 api 各校验一次（含保存时 DNS），口径与
	// Update 完全一致。
	base, apiBase, err := s.resolveURLs(ctx, "", strings.TrimSpace(req.BaseUrl),
		"", derefString(req.ApiBaseUrl), false, allowPrivate, true, true)
	if err != nil {
		return nil, err
	}
	// 同一归一主机只允许一条集成：同实例换 scheme/端口写法再来一条，凭证与
	// 项目绑定会互相踩。
	if err := s.ensureHostAvailable(base, ""); err != nil {
		return nil, err
	}
	caCert := ""
	if req.CaCertPem != nil && strings.TrimSpace(*req.CaCertPem) != "" {
		if err := forges.ValidateCACertPEM(*req.CaCertPem); err != nil {
			return nil, paramError(err)
		}
		caCert = *req.CaCertPem
	}

	encKey := []byte(s.cfg.Encryption.Key)
	tokenEnc, err := crypto.Encrypt([]byte(strings.TrimSpace(req.Token)), encKey)
	if err != nil {
		s.logger.Error("加密集成 token 失败", zap.Error(err))
		return nil, errs.ErrInternal
	}
	webhookSecret := strings.TrimSpace(derefString(req.WebhookSecret))
	var webhookEnc []byte
	if webhookSecret != "" {
		webhookEnc, err = crypto.Encrypt([]byte(webhookSecret), encKey)
		if err != nil {
			s.logger.Error("加密集成 webhook 密钥失败", zap.Error(err))
			return nil, errs.ErrInternal
		}
	}

	row := model.ForgeIntegration{
		ID:                     uuid.NewString(),
		Kind:                   model.ForgeIntegrationKindForgejo,
		Name:                   name,
		BaseURL:                base,
		APIBaseURL:             apiBase,
		HostKey:                forges.HostKey(base),
		TokenEncrypted:         tokenEnc,
		WebhookSecretEncrypted: webhookEnc,
		CACertPEM:              caCert,
		AllowPrivateNetwork:    allowPrivate,
	}
	if err := s.repo.Create(&row); err != nil {
		if errors.Is(err, repository.ErrForgeHostTaken) {
			// 唯一索引兜底触发（应用层查重后的并发窗口），折回同类 40001。
			return nil, s.hostTakenErr(row.HostKey)
		}
		s.logger.Error("保存代码集成失败", zap.Error(err))
		return nil, errs.ErrInternal
	}
	return projectForgeRow(&row), nil
}

// Update PUT /api/deployment/integrations/:id：部分更新。url / allow_private_
// network 变更按最终值重新校验（flag 关闭会按公网口径复核现有基址）；token
// 缺省/null = 保留、空串 = 40001（必需凭证不可清空）；webhook_secret /
// ca_cert_pem 空串 = 清空；api_base_url 空串 = 重置为 {base_url}/api/v1
// 推导。base_url 归一主机变更
// 等同迁移全部项目绑定，仍有引用时 40935 拒绝（同 Delete 口径），同主机的
// 写法级改写（尾斜杠、默认端口）不触发。
func (s *ForgeIntegrationService) Update(ctx context.Context, id string, req *api.UpdateForgeIntegrationRequest) (*api.ForgeIntegration, error) {
	if id == model.ForgeIntegrationBuiltinGitHubID {
		return nil, errForgeIntegrationBuiltin
	}
	row, err := s.repo.FindByID(id)
	if err != nil {
		return nil, s.notFound(err)
	}

	fields := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, errForgeIntegrationNameRequired
		}
		row.Name = name
		fields["name"] = name
	}

	// 开关变更先记下「要变更」这个事实（不落 row——校验失败时既不写库也不改
	// 响应投影），等基址按新开关复核通过后再应用。
	flagChanged := req.AllowPrivateNetwork != nil && *req.AllowPrivateNetwork != row.AllowPrivateNetwork
	allowPrivate := row.AllowPrivateNetwork
	if req.AllowPrivateNetwork != nil {
		allowPrivate = *req.AllowPrivateNetwork
	}

	// api_base_url 三态：null 保留、空串重置为推导、其余替换；base_url
	// 只有两态（null 保留、非空替换——base 无默认可重置，空串走
	// errForgeIntegrationBaseRequired 40001）。
	// 只在「值真的变化」或「开关收紧」时才做保存时 DNS 校验：无关更新
	// （改名、只开内网放行）不碰网络，存量基址 DNS 失效不该连坐无关
	// 字段更新——改 api 到可达端点、开内网放行正是失效期的自救路径。
	baseChanged := req.BaseUrl != nil && strings.TrimSpace(*req.BaseUrl) != strings.TrimSpace(row.BaseURL)
	apiChanged := req.ApiBaseUrl != nil && strings.TrimSpace(*req.ApiBaseUrl) != strings.TrimSpace(row.APIBaseURL)
	hostMigrating := false
	if baseChanged || apiChanged || flagChanged {
		reqBase := row.BaseURL
		if baseChanged {
			reqBase = strings.TrimSpace(*req.BaseUrl)
			if reqBase == "" {
				return nil, errForgeIntegrationBaseRequired
			}
		}
		reqAPI := row.APIBaseURL
		if apiChanged {
			reqAPI = strings.TrimSpace(*req.ApiBaseUrl)
		}
		tightened := flagChanged && !allowPrivate
		base, apiBase, err := s.resolveURLs(ctx, row.BaseURL, reqBase, row.APIBaseURL, reqAPI,
			apiChanged && reqAPI == "", allowPrivate, baseChanged || tightened, apiChanged || tightened)
		if err != nil {
			// 单独关开关（不改基址）必然撞公网口径复核：把「先换公网 https
			// 基址、再关内网放行」的操作顺序写进消息，不然管理员只看到基址
			// 校验原文，悟不出要在同一次请求里先改基址。只对私网/协议类
			// 哨兵追加并按失败字段分开措辞——钉死的 api_base_url 挂了别
			// 把管理员引向只改 base_url。
			if tightened {
				var fe *urlFieldErr
				if errors.As(err, &fe) &&
					(errors.Is(fe.cause, forges.ErrPrivateAddress) ||
						errors.Is(fe.cause, forges.ErrSchemeNotAllowed)) {
					hint := "；请先将站点地址改用公网 https 再关闭内网放行"
					if fe.field == "api_base_url" {
						hint = "；请先将 API 基址改用公网 https（或清空回到跟随站点推导）再关闭内网放行"
					}
					return nil, errs.New(fe.appErr.Code, fe.appErr.Message+hint)
				}
			}
			return nil, err
		}
		// 只在基址真的变化时查重：改 api_base_url 或翻转开关不动 base。
		if baseChanged {
			if err := s.ensureHostAvailable(base, row.ID); err != nil {
				return nil, err
			}
		}
		// 归一主机变更等同迁移全部项目绑定：绑定按集成 id 持久化，改主机后
		// 同 owner/repo 在新实例上可能是完全不同的仓库——存在引用时与删除
		// 同口径拒绝（写入走 UpdateColumnsIfUnreferenced），让管理员先解绑
		// 或走「新建集成 + 项目逐个改绑」的显式迁移路径。判定用归一 host_key
		// 而非字符串：尾斜杠、scheme 默认端口这类同主机改写不触发拦截。
		hostMigrating = baseChanged && forges.HostKey(base) != row.HostKey
		row.BaseURL = base
		row.APIBaseURL = apiBase
		// host_key 与 base_url 同步落库：同归一主机的部分唯一索引在这里兜
		// ensureHostAvailable 之后的并发窗口。
		row.HostKey = forges.HostKey(base)
		fields["base_url"] = base
		fields["api_base_url"] = apiBase
		fields["host_key"] = row.HostKey
	}
	if flagChanged {
		row.AllowPrivateNetwork = allowPrivate
		fields["allow_private_network"] = allowPrivate
	}

	encKey := []byte(s.cfg.Encryption.Key)
	if req.Token != nil {
		token := strings.TrimSpace(*req.Token)
		// token 是必需凭证，不可清空：显式空串/纯空白视为「想改没改成」——
		// 粘贴空白的轮换场景若静默保留旧值，管理员会误以为凭证已轮换。
		// 保留请省略字段（缺省/null 语义不变）。
		if token == "" {
			return nil, paramError(errors.New("token 不支持清空；保留现有 token 请省略该字段"))
		}
		tokenEnc, err := crypto.Encrypt([]byte(token), encKey)
		if err != nil {
			s.logger.Error("加密集成 token 失败", zap.Error(err))
			return nil, errs.ErrInternal
		}
		fields["token_encrypted"] = tokenEnc
	}
	if req.WebhookSecret != nil {
		if secret := strings.TrimSpace(*req.WebhookSecret); secret == "" {
			// 空串 = 清空已存密钥（与 ca_cert_pem 同口径）：设错或要撤掉
			// 验签时不再只能替换或删集成重建。缺省/null 仍是保留。
			fields["webhook_secret_encrypted"] = ""
		} else {
			secretEnc, err := crypto.Encrypt([]byte(secret), encKey)
			if err != nil {
				s.logger.Error("加密集成 webhook 密钥失败", zap.Error(err))
				return nil, errs.ErrInternal
			}
			fields["webhook_secret_encrypted"] = secretEnc
		}
	}
	if req.CaCertPem != nil {
		// 空串 = 清空（ca_cert_pem 支持清空）；其余替换并校验 PEM。
		if strings.TrimSpace(*req.CaCertPem) == "" {
			row.CACertPEM = ""
			fields["ca_cert_pem"] = ""
		} else {
			if err := forges.ValidateCACertPEM(*req.CaCertPem); err != nil {
				return nil, paramError(err)
			}
			row.CACertPEM = *req.CaCertPem
			fields["ca_cert_pem"] = *req.CaCertPem
		}
	}

	if len(fields) > 0 {
		var updateErr error
		if hostMigrating {
			affected, err := s.repo.UpdateColumnsIfUnreferenced(id, fields)
			if err == nil && affected == 0 {
				return nil, s.hostMigrationInUseErr(id)
			}
			updateErr = err
		} else {
			updateErr = s.repo.UpdateColumns(id, fields)
		}
		if updateErr != nil {
			if errors.Is(updateErr, gorm.ErrRecordNotFound) {
				return nil, errs.ErrForgeIntegrationNotFound
			}
			if errors.Is(updateErr, repository.ErrForgeHostTaken) {
				// 唯一索引兜底触发（改基址的并发窗口），折回同类 40001。
				return nil, s.hostTakenErr(row.HostKey)
			}
			s.logger.Error("更新代码集成失败", zap.Error(updateErr))
			return nil, errs.ErrInternal
		}
		// updated_at 由 UpdateColumns 写库，回读保证响应带着新值。
		if row, err = s.repo.FindByID(id); err != nil {
			return nil, s.notFound(err)
		}
	}
	return projectForgeRow(row), nil
}

// Delete DELETE /api/deployment/integrations/:id：内置拒绝；被项目引用拒绝并
// 在消息里列出引用项目名。删除提交点原子：引用判定写进同一条 DELETE
// （DeleteIfUnreferenced），「先查后删」之间项目并发绑定成功不会留下悬空
// 引用——残余窗口只剩服务层之上的读取面（见 docs/internal/forge-integrations.md）。
func (s *ForgeIntegrationService) Delete(ctx context.Context, id string) error {
	if id == model.ForgeIntegrationBuiltinGitHubID {
		return errForgeIntegrationBuiltin
	}
	affected, err := s.repo.DeleteIfUnreferenced(id)
	if err != nil {
		s.logger.Error("删除代码集成失败", zap.Error(err))
		return errs.ErrInternal
	}
	if affected > 0 {
		return nil
	}
	// 0 行：行不存在报 40435；行还在说明被引用，列出引用项目名。
	if _, err := s.repo.FindByID(id); err != nil {
		return s.notFound(err)
	}
	refs, err := s.repo.ReferencingProjects(id)
	if err != nil {
		s.logger.Error("统计集成引用项目失败", zap.Error(err))
		return errs.ErrInternal
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	if len(names) == 0 {
		// 竞态窗口：删 0 行但行还在时引用已消失，消息别拖一条裸「：」。
		return errs.ErrForgeIntegrationInUse
	}
	return errs.New(errs.ErrForgeIntegrationInUse.Code,
		errs.ErrForgeIntegrationInUse.Message+"："+strings.Join(names, "、"))
}

// hostMigrationInUseErr 归一主机变更仍被项目引用时的拒绝：UpdateColumnsIf-
// Unreferenced 写 0 行时先确认行还在（并发删除按 40435），再列引用项目名——
// 与 Delete 同口径，指明先解绑或新建集成逐个改绑。
func (s *ForgeIntegrationService) hostMigrationInUseErr(id string) error {
	if _, err := s.repo.FindByID(id); err != nil {
		return s.notFound(err)
	}
	refs, err := s.repo.ReferencingProjects(id)
	if err != nil {
		s.logger.Error("统计集成引用项目失败", zap.Error(err))
		return errs.ErrInternal
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	if len(names) == 0 {
		return errs.ErrForgeIntegrationInUse
	}
	return errs.New(errs.ErrForgeIntegrationInUse.Code,
		"该集成仍被项目引用，不能更改站点主机（改主机等同迁移全部绑定，请先解除项目绑定或新建集成后逐个改绑）："+strings.Join(names, "、"))
}

// Test POST /api/deployment/integrations/:id/test：解密 token 后按集成现建
// forges client，调 GET version 与 GET user。结论只随响应返回、不落库。
func (s *ForgeIntegrationService) Test(ctx context.Context, id string) (*api.ForgeIntegrationTest, error) {
	if id == model.ForgeIntegrationBuiltinGitHubID {
		return nil, errs.New(40001, "内置 GitHub 集成请使用 POST /api/deployment/github-app/test 检测")
	}
	row, err := s.repo.FindByID(id)
	if err != nil {
		return nil, s.notFound(err)
	}
	token, err := crypto.Decrypt(row.TokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, errForgeIntegrationDecrypt
	}
	apiBase := forgeAPIBaseURL(row)
	client, err := forges.NewClient(forges.Config{
		AllowedHosts:        forges.AllowedHosts(row.BaseURL, apiBase),
		CACertPEM:           row.CACertPEM,
		AllowPrivateNetwork: row.AllowPrivateNetwork,
		AuthPathPrefix:      apiPathPrefix(apiBase),
	})
	if err != nil {
		s.logger.Error("构建集成 client 失败", zap.Error(err))
		return nil, errs.ErrInternal
	}
	probe := s.probe(ctx, client, apiBase, string(token))
	return &api.ForgeIntegrationTest{
		Status:   api.ForgeIntegrationTestStatus(probe.Status),
		Version:  probe.Version,
		Username: probe.Username,
		Detail:   probe.Detail,
	}, nil
}

// builtinProjection 内置 github-default 的只读投影：映射部署级 GitHub App 当前
// 生效身份（部署设置行 > config 兜底）。解密失败 50000 直接透出，与
// IntegrationService 同口径、不回落 config。
func (s *ForgeIntegrationService) builtinProjection(ctx context.Context) (*api.ForgeIntegration, error) {
	appCfg, ok, err := s.github.Current(ctx)
	if err != nil {
		return nil, err
	}
	apiBase := strings.TrimSpace(s.cfg.GitHubApp.APIBaseURL)
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &api.ForgeIntegration{
		Id:                  model.ForgeIntegrationBuiltinGitHubID,
		Kind:                api.Github,
		Name:                "GitHub（内置）",
		BaseUrl:             githubWebBase(s.cfg),
		ApiBaseUrl:          apiBase,
		ApiBaseUrlDerived:   false,
		TokenSet:            ok,
		WebhookSecretSet:    strings.TrimSpace(appCfg.WebhookSecret) != "",
		HasCaCert:           false,
		AllowPrivateNetwork: false,
		Builtin:             true,
	}, nil
}

// resolveURLs 校验并归一 base / api 两个基址：reqBase 为空回落 currentBase
// （Update 的部分更新语义）；allow_private_network 控制保存时 DNS 校验的
// 口径（公网/私网，两口径都逐 IP 校验）。apiReset=true 表示 Update 显式传了
// 空串，解除显式钉死回到推导。baseRevalidate/apiRevalidate=false 时跳过
// 对应基址的保存时复验（存量值是保存时验过的归一值，原样用回）——基址未
// 变更且开关未收紧的更新（只改 api_base_url、只开内网放行）不该被实例
// DNS 失效连坐 40001；收窄开关（公网口径复核）与基址变更仍必须全量校验。
//
// 返回的 apiStore 是存储值不是展开值：空串 = 「按 {base_url}/api/v1 推导」
// （读取侧 forgeAPIBaseURL 展开），非空 = 管理员显式钉死的端点。意图存进
// 列里，改 base_url 时推导行自然跟随、显式行自然钉住，不再靠字符串相等性
// 去猜当初是推导还是显式。任何校验失败都折成 40001。
func (s *ForgeIntegrationService) resolveURLs(ctx context.Context, currentBase, reqBase, currentAPI, reqAPI string, apiReset, allowPrivate, baseRevalidate, apiRevalidate bool) (base, apiStore string, err error) {
	baseInput := reqBase
	if baseInput == "" {
		baseInput = currentBase
	}
	if baseRevalidate {
		base, err = s.validateBaseURL(ctx, baseInput, allowPrivate)
		if err != nil {
			return "", "", &urlFieldErr{field: "base_url", cause: err, appErr: paramError(err)}
		}
		// base_url 只收站点根：审查链接与 code_repo_url 都按 {base_url}/{o}/{r}
		// 拼接，带路径的基址会拼出错误 URL。api_base_url 不受限（可带 /api/v1）。
		if err := requireBaseRoot(base); err != nil {
			return "", "", err
		}
		// github.com 与本部署 GitHub Web 主机（GHE）的仓库 URL 永远先归内置
		// github-default（parseRepoBinding 的主机判定次序）——指向这两类主机的
		// Forgejo 集成可保存却永远绑不上，保存期直接拒绝而不是留一条死配置。
		if err := s.rejectDeadForgeHost(base); err != nil {
			return "", "", err
		}
	} else {
		// 存量值原样用回（保存时已过完整校验；去尾斜杠保持与归一值同形态）。
		base = strings.TrimRight(strings.TrimSpace(baseInput), "/")
	}
	apiInput := strings.TrimSpace(reqAPI)
	if apiInput == "" {
		apiInput = strings.TrimSpace(currentAPI)
	}
	if apiReset || apiInput == "" {
		// 推导（Create 省略 / Update 显式重置 / 存量空列）：存空串，读取侧
		// 按当前 base 展开——改 base_url 自动跟随。
		return base, "", nil
	}
	// 回显守卫：存量是推导行（currentAPI 空）而客户端把「生效值」原样
	// 提交——编辑框预填的就是展开值，回显不等于显式钉死，保持推导。生效
	// 值要和新、旧两个 base 的推导值都比：同一次 Update 改 base_url 时，
	// GET→改 base→PUT 的调用方回写的是「旧 base 的展开值」，只比新推导值
	// 会把它当显式钉死在旧主机上。真要把推导值钉成显式：先行已显式
	// （currentAPI 非空）或提交值与两个推导值都不等价。
	//
	// 判定只做 URLKey 纯解析等价比较（显式 :443、尾斜杠、大小写/DNS 尾点
	// 拼写差归一成同键），不走 validateBaseURL：回显值主机已下线时 DNS
	// 校验会把「保持推导」的 Update 整体 40001——base 迁移场景正是旧
	// 主机退役后才改 base_url。不命中才落到完整校验。
	//
	// 先过一道纯解析形状检查：URLKey 不含 query/fragment/userinfo，
	// 「推导值+多余成分」的提交（?x=1、u:p@、#f、尾裸 ?）形状不合法就
	// 不短路——落回完整校验报 40001，而不是被抹平成推导静默放行。
	shapeOK := false
	if u, perr := url.Parse(apiInput); perr == nil && u.Host != "" {
		shapeOK = u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
	}
	if shapeOK && strings.TrimSpace(currentAPI) == "" &&
		(forges.URLKey(apiInput) == forges.URLKey(forges.DefaultAPIBaseURL(base)) ||
			(currentBase != "" && forges.URLKey(apiInput) == forges.URLKey(forges.DefaultAPIBaseURL(currentBase)))) {
		return base, "", nil
	}
	if !apiRevalidate {
		// 与 base 同口径：存量钉死值保存时已验过，未变更且开关未收紧时
		// 不复验——api 主机 DNS 失效同样不该连坐「只改 base」的迁移路径。
		return base, strings.TrimRight(apiInput, "/"), nil
	}
	apiStore, err = s.validateBaseURL(ctx, apiInput, allowPrivate)
	if err != nil {
		return "", "", &urlFieldErr{field: "api_base_url", cause: err, appErr: paramError(err)}
	}
	return base, apiStore, nil
}

// paramError 把校验层的具体原因折成 40001（消息原文透出，不含密钥）。
func paramError(err error) *errs.AppError {
	return errs.New(40001, err.Error())
}

// urlFieldErr 标记 resolveURLs 里哪个基址校验失败并保留原始哨兵：Update
// 关闭内网放行触发复核时，需要区分「站点地址」与「API 基址」给字段级操作
// 提示，且只对私网/协议类安全哨兵追加（DNS 抖动不算「先换公网」能解决的）。
type urlFieldErr struct {
	field  string
	cause  error
	appErr *errs.AppError
}

func (e *urlFieldErr) Error() string { return e.appErr.Error() }

// Unwrap 双链：errors.As(*errs.AppError) 让 HandleAppError 照常按 40001
// 出响应，errors.Is(forges 哨兵) 让 Update 判定该不该追加操作提示。
func (e *urlFieldErr) Unwrap() []error { return []error{e.cause, e.appErr} }

// errForgeHostTaken 同一归一主机已被其他集成占用（40001）。
func errForgeHostTaken(name string) *errs.AppError {
	return errs.New(errs.ErrInvalidParams.Code,
		"该主机已被集成 "+name+" 占用，同一 Forgejo 主机只能配置一条集成")
}

// hostTakenErr 数据库唯一索引兜底触发（应用层查重通过后同主机被并发抢注）时
// 的 40001：尽力点出占用集成的名字（应用层查重没看见的行是竞态对手方刚提交
// 的，按归一键现查一次），查不到就用不带名字的泛化文案，与应用层查重同口径。
func (s *ForgeIntegrationService) hostTakenErr(key string) *errs.AppError {
	if key != "" {
		if _, name, found, err := s.repo.FindByHostKey(key, ""); err == nil && found {
			return errForgeHostTaken(name)
		}
	}
	return errs.New(errs.ErrInvalidParams.Code,
		"该主机已被其他集成占用，同一 Forgejo 主机只能配置一条集成")
}

// errForgeBaseRootRequired base_url 只收站点根（40001）。
var errForgeBaseRootRequired = errs.New(errs.ErrInvalidParams.Code,
	"base_url 必须是站点根地址（不带路径）")

// errForgeDeadHost base_url 指向 GitHub 主机（40001）：配了也绑不上的死配置。
var errForgeDeadHost = errs.New(errs.ErrInvalidParams.Code,
	"base_url 指向 GitHub 主机——github.com 与本部署 GitHub 实例的仓库归内置 GitHub 集成，Forgejo 集成不会命中")

// requireBaseRoot base_url 形状收口：空路径与 "/" 都算站点根。
func requireBaseRoot(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err == nil && u.Path != "" && u.Path != "/" {
		return errForgeBaseRootRequired
	}
	return nil
}

// rejectDeadForgeHost 拒绝永远绑不上的死主机：parseRepoBinding 对
// github.com（含 :80/:443 归一形态）与本部署 GitHub Web 主机一律先归内置
// github-default，指向它们的 Forgejo 集成不会被任何仓库 URL 命中。
func (s *ForgeIntegrationService) rejectDeadForgeHost(base string) error {
	key := forges.HostKey(base)
	if key == "" {
		return nil
	}
	if key == forges.HostKey(githubWebBase(s.cfg)) {
		return errForgeDeadHost
	}
	// github.com 属主机按主机名（不管端口）全拒：github.com:8443、
	// api./gist. 等子域的归一键与 github.com:443 不等，能逃过按键比较，
	// 但它们是 GitHub 基础设施不是 Forgejo 实例，且仓库 URL 的 github.com
	// 归内置判定在集成匹配之前——这类集成永远绑不上。
	if h, _, err := net.SplitHostPort(key); err == nil &&
		(h == "github.com" || strings.HasSuffix(h, ".github.com")) {
		return errForgeDeadHost
	}
	return nil
}

// ensureHostAvailable 新基址的归一主机（小写 host + scheme 缺省端口）不得与
// 其他 forgejo 集成重复。excludeID 传被更新行的 id（自身不与自己冲突），空串
// 表示新建。存量脏行解析不出主机键的不参与比较。
func (s *ForgeIntegrationService) ensureHostAvailable(baseURL, excludeID string) error {
	key := forgeBaseHostKey(baseURL)
	if key == "" {
		return nil
	}
	_, name, found, err := s.repo.FindByHostKey(key, excludeID)
	if err != nil {
		s.logger.Error("查询集成主机占用失败", zap.Error(err))
		return errs.ErrInternal
	}
	if found {
		return errForgeHostTaken(name)
	}
	return nil
}

// notFound 行不存在时报 40435；查询本身失败按 50000。
func (s *ForgeIntegrationService) notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.ErrForgeIntegrationNotFound
	}
	s.logger.Error("读取代码集成失败", zap.Error(err))
	return errs.ErrInternal
}

// forgeAPIBaseURL 行的 API 基址：空列（历史行）按 {base_url}/api/v1 推导。
func forgeAPIBaseURL(row *model.ForgeIntegration) string {
	if api := strings.TrimSpace(row.APIBaseURL); api != "" {
		return api
	}
	return forges.DefaultAPIBaseURL(row.BaseURL)
}

// projectForgeRow forgejo 行的响应投影：凭证本体永不回显，只有 *_set 标记。
func projectForgeRow(row *model.ForgeIntegration) *api.ForgeIntegration {
	created, updated := row.CreatedAt, row.UpdatedAt
	return &api.ForgeIntegration{
		Id:         row.ID,
		Kind:       api.Forgejo,
		Name:       row.Name,
		BaseUrl:    row.BaseURL,
		ApiBaseUrl: forgeAPIBaseURL(row),
		// 存储空串 = 跟随 base_url 推导；响应里区分推导与钉死，编辑端才能
		// 提示「原样提交仍是推导」而不是把展开值当已钉死值回显。
		ApiBaseUrlDerived:   strings.TrimSpace(row.APIBaseURL) == "",
		TokenSet:            len(row.TokenEncrypted) > 0,
		WebhookSecretSet:    len(row.WebhookSecretEncrypted) > 0,
		HasCaCert:           strings.TrimSpace(row.CACertPEM) != "",
		AllowPrivateNetwork: row.AllowPrivateNetwork,
		Builtin:             false,
		CreatedAt:           api.JSONTimePtr(&created),
		UpdatedAt:           api.JSONTimePtr(&updated),
	}
}

// forgeVersionResponse / forgeUserResponse Forgejo REST 的最小读取面。
type forgeVersionResponse struct {
	Version string `json:"version"`
}

type forgeUserResponse struct {
	Login string `json:"login"`
}

// probeForge 连接检测的缺省实现：GET {api}/version 与 GET {api}/user（PAT 走
// Forgejo 惯例的 Authorization: token 头）。401/403 是 invalid（token 被拒）；
// 网络错误与其他状态码是 failed；两者都成功才是 ok。200 但响应不是预期 JSON
// 形态同样按 failed 处理——body 原样回显会把实例的错误页（最大 1MB）泄漏进
// 结论，一律不透传。
func probeForge(ctx context.Context, client *http.Client, apiBaseURL, token string) ForgeProbe {
	apiBase := strings.TrimRight(apiBaseURL, "/")

	status, body, err := forgeAPIGet(ctx, client, apiBase+"/version", token)
	version := ""
	var scopeNotes []string
	versionOK := false
	switch {
	case err != nil:
		return ForgeProbe{Status: model.DeploymentDetectionFailed, Detail: probeErrDetail(err)}
	case status == http.StatusOK:
		var parsed forgeVersionResponse
		if json.Unmarshal(body, &parsed) != nil || strings.TrimSpace(parsed.Version) == "" {
			return ForgeProbe{Status: model.DeploymentDetectionFailed,
				Detail: "version 响应不是预期的 Forgejo API 格式，请确认实例地址与 API 版本"}
		}
		version = truncateProbeField(strings.TrimSpace(parsed.Version))
		versionOK = true
	case status == http.StatusUnauthorized:
		return ForgeProbe{Status: model.DeploymentDetectionInvalid,
			Detail: fmt.Sprintf("token 被实例拒绝（HTTP %d），请检查机器人 PAT", status)}
	case status == http.StatusForbidden:
		// 已认证但实例把 /version 也按 scope 拦：与 /user 同口径——403
		// 是缺权限不是 token 被拒，仓库只读 PAT 不该误报 invalid。
		scopeNotes = append(scopeNotes, "实例版本不可读")
	default:
		return ForgeProbe{Status: model.DeploymentDetectionFailed,
			Detail: fmt.Sprintf("实例返回 HTTP %d（GET version）", status)}
	}

	status, body, err = forgeAPIGet(ctx, client, apiBase+"/user", token)
	username := ""
	userOK := false
	switch {
	case err != nil:
		return ForgeProbe{Status: model.DeploymentDetectionFailed, Detail: probeErrDetail(err)}
	case status == http.StatusOK:
		var parsed forgeUserResponse
		if json.Unmarshal(body, &parsed) != nil || strings.TrimSpace(parsed.Login) == "" {
			return ForgeProbe{Status: model.DeploymentDetectionFailed,
				Detail: "user 响应不是预期的 Forgejo API 格式，请确认 token 具有读取账号信息的权限"}
		}
		username = truncateProbeField(strings.TrimSpace(parsed.Login))
		userOK = true
	case status == http.StatusUnauthorized:
		return ForgeProbe{Status: model.DeploymentDetectionInvalid,
			Detail: fmt.Sprintf("token 被实例拒绝（HTTP %d），请检查机器人 PAT", status)}
	case status == http.StatusForbidden:
		// 已认证但缺 read:user scope——仓库只读 PAT 在 /user 拿 403，
		// token 本身有效，不误报 invalid；401 才是真被拒。
		scopeNotes = append(scopeNotes, "账号信息不可读（缺 read:user 权限）")
	default:
		return ForgeProbe{Status: model.DeploymentDetectionFailed,
			Detail: fmt.Sprintf("实例返回 HTTP %d（GET user）", status)}
	}

	if !versionOK && !userOK {
		// 两端点都 403：可能 token 对 API 完全无读权限，也可能是 WAF/反代
		// 按来源整体拦——报「连接正常」会误导管理员以为实例与凭证可用。
		return ForgeProbe{Status: model.DeploymentDetectionFailed,
			Detail: "version 与 user 端点均被拒绝（HTTP 403），请确认 token 权限与实例访问策略"}
	}
	detail := ""
	if len(scopeNotes) > 0 {
		detail = "token 权限受限：" + strings.Join(scopeNotes, "；") + "（仅仓库场景可用）"
	}
	return ForgeProbe{
		Status:   model.DeploymentDetectionOK,
		Version:  version,
		Username: username,
		Detail:   detail,
	}
}

// probeErrDetail 网络层错误的 detail：SSRF 防护主动拦截（拨号私网判定、
// 重定向政策）与真实断网分开——前者该改集成配置或跳转目标而不是查网络，
// 文案透出策略哨兵的人话描述（哨兵是常量，不含请求细节与凭证）。其余错误
// 按类别透出内层原因（url.Error 的 Err 不含请求 URL 与凭证，主机本就是
// 管理员自己配置的）：自签 TLS、DNS、拒绝、超时的修法各不同，折叠成一句
// 「网络错误」会让连接检测失去排障能力。
func probeErrDetail(err error) string {
	for _, sentinel := range []error{
		forges.ErrPrivateAddress, forges.ErrCrossHostRedirect,
		forges.ErrSchemeNotAllowed, forges.ErrSchemeDowngrade,
	} {
		if errors.Is(err, sentinel) {
			return "地址或重定向被安全策略拒绝：" + sentinel.Error()
		}
	}
	inner := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		inner = urlErr.Err
	}
	var certInvalid x509.CertificateInvalidError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recErr tls.RecordHeaderError
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(inner, &certInvalid), errors.As(inner, &unknownAuth),
		errors.As(inner, &hostErr):
		return "TLS 证书校验失败：" + inner.Error() + "（自签名实例请配置自定义 CA 证书）"
	case errors.As(inner, &recErr):
		return "TLS 握手失败，对端未按 TLS 应答（http 端口配了 https？）：" + inner.Error()
	case errors.As(inner, &dnsErr):
		return "DNS 解析失败：" + inner.Error()
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "连接超时，请检查实例地址与网络可达性"
	case errors.As(inner, &opErr):
		return "连接失败：" + inner.Error()
	}
	return "网络错误，未能连接 Forgejo：" + inner.Error()
}

// truncateProbeField 防御性截断：实例版本字段理论上很短，超长按 64 个字符
// 截断（按 rune，不切坏多字节字符）。
func truncateProbeField(s string) string {
	runes := []rune(s)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return s
}

// apiPathPrefix 取 API 基址的路径段作凭证作用域前缀（/api/v1 惯例）。根
// 路径（显式把 api_base_url 钉在站点根）返回 "/"——整个主机即 API 作用域，
// 同主机跳转一律保留 Authorization；与「解析失败返回空串、不启用路径判定」
// 区分开，语义上不把「钉死在根」误读成「没配作用域」。
func apiPathPrefix(apiBase string) string {
	u, err := url.Parse(apiBase)
	if err != nil || u == nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return "/"
	}
	return p
}

// forgeAPIGet 发起带 PAT 的 GET，返回状态码与响应体；网络层失败（含响应体
// 读取中断）返回 err。凭证作用域由 client 的 Config.AuthPathPrefix 统一
// 把关：同主机跳转清理后路径仍在 API 前缀下才保留 Authorization，跳出或
// 含歧义段的跳转剥掉——PAT 不落进实例上非 API 端点的访问日志。
func forgeAPIGet(ctx context.Context, client *http.Client, url, token string) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	// Forgejo/Gitea 的 PAT 认证头惯例。
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	// 响应体只用于 version/user 两个字段，读满 1MB 就不再扩大。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}
