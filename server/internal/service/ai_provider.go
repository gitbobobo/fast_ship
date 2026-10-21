package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/catalog"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AIProviderService 是 AI 提供商（INT-133）的应用入口：按用户隔离的 CRUD、
// 测试连接与内置目录读取。密钥只在 provider 行存一份；机器人经 provider_id
// 引用，轮换密钥后所有引用它的机器人在下一个 run 生效。
type AIProviderService struct {
	db     *gorm.DB
	cfg    *config.Config
	logger *zap.Logger

	providers *repository.AIProviderRepository
	bots      *repository.BotRepository
}

func NewAIProviderService(db *gorm.DB, cfg *config.Config, logger *zap.Logger) *AIProviderService {
	return &AIProviderService{
		db:        db,
		cfg:       cfg,
		logger:    logger,
		providers: repository.NewAIProviderRepository(db),
		bots:      repository.NewBotRepository(db),
	}
}

// ---------- 响应类型 ----------

// AIProviderResponse 管理面响应；key 全链路脱敏，只回末四位。BotCount 是当前
// 引用该提供商的机器人数量（删除前置检查与展示共用同一口径）。
type AIProviderResponse struct {
	ID          string `json:"id"`
	CatalogID   string `json:"catalog_id"`
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	APIKeyLast4 string `json:"api_key_last4"`
	BotCount    int64  `json:"bot_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// CatalogProviderModel / CatalogProvider 是内置目录端点的投影，与 catalog 包
// 的存储形状一一对应（models.dev 子集）。
type CatalogProviderModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextWindow   int64  `json:"context_window"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	Reasoning       bool   `json:"reasoning"`
}

type CatalogProvider struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	API    string                 `json:"api"`
	Models []CatalogProviderModel `json:"models"`
}

type CreateAIProviderRequest struct {
	CatalogID string `json:"catalog_id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
}

// UpdateAIProviderRequest 指针字段表达「未提供保留」；api_key 空串拒绝。
// catalog_id 不可改（更换目录归属等于换提供商，应新建一条）。
type UpdateAIProviderRequest struct {
	Name    *string `json:"name,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
}

// ProviderTestResult 是测试连接的响应（TESTRESULT）：checks 与机器人测试连接
// 同一检查项（streaming / tool_call），models 是 /models 列表尽力拉取的模型
// id（拉取失败不填充，不影响检查结果）。
type ProviderTestResult struct {
	Ok     bool                    `json:"ok"`
	Checks []ConnectionCheckResult `json:"checks"`
	Models []string                `json:"models,omitempty"`
}

type TestAIProviderRequest struct {
	CatalogID *string `json:"catalog_id,omitempty"`
	BaseURL   *string `json:"base_url,omitempty"`
	APIKey    *string `json:"api_key"`
	Model     *string `json:"model,omitempty"`
	// ProviderID 可选：指向一条已存在的 provider 时校验归属，探测成功后清除
	// 该 provider 的定时任务熔断（INT-137）。探测参数仍只来自请求体。
	ProviderID *string `json:"provider_id,omitempty"`
}

// ---------- 目录 ----------

// Catalog 返回内置 models.dev 目录（发版时随二进制刷新，运行时不联网）。
func (s *AIProviderService) Catalog() []CatalogProvider {
	providers := catalog.Catalog()
	out := make([]CatalogProvider, 0, len(providers))
	for _, p := range providers {
		models := make([]CatalogProviderModel, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, CatalogProviderModel{
				ID:              m.ID,
				Name:            m.Name,
				ContextWindow:   m.ContextWindow,
				MaxOutputTokens: m.MaxOutputTokens,
				Reasoning:       m.Reasoning,
			})
		}
		out = append(out, CatalogProvider{ID: p.ID, Name: p.Name, API: p.API, Models: models})
	}
	return out
}

// ---------- 管理 CRUD ----------

func (s *AIProviderService) List(userID string) ([]AIProviderResponse, error) {
	rows, err := s.providers.ListByUser(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	counts, err := s.providers.CountBotsByProviderIDs(ids)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]AIProviderResponse, 0, len(rows))
	for i := range rows {
		out = append(out, s.responseOf(&rows[i], counts[rows[i].ID]))
	}
	return out, nil
}

func (s *AIProviderService) Get(userID, id string) (*AIProviderResponse, error) {
	p, err := s.providers.FindByIDAndUser(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrAIProviderNotFound
		}
		return nil, errs.ErrInternal
	}
	counts, err := s.providers.CountBotsByProviderIDs([]string{p.ID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.responseOf(p, counts[p.ID])
	return &resp, nil
}

// Create 落一条 provider：目录提供商的 base_url 由目录决定（请求里的忽略），
// custom 必须给 http/https 地址；name 缺省用目录名（custom 用「自定义」）。
func (s *AIProviderService) Create(userID string, req *CreateAIProviderRequest) (*AIProviderResponse, error) {
	catalogID := strings.TrimSpace(req.CatalogID)
	if catalogID == "" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "catalog_id 必填")
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "api_key 必填")
	}

	baseURL := ""
	defaultName := ""
	if catalogID == catalog.CustomCatalogID {
		baseURL = catalog.NormalizeBaseURL(req.BaseURL)
		if !providerValidBaseURL(baseURL) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "base_url 必须是 http(s) 地址")
		}
		defaultName = "自定义"
	} else {
		resolved, ok := catalog.ResolveBaseURL(catalogID)
		if !ok {
			return nil, errs.New(errs.ErrInvalidParams.Code, "catalog_id 不在内置目录中")
		}
		baseURL = resolved
		if p, ok := catalog.FindProvider(catalogID); ok {
			defaultName = p.Name
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultName
	}

	cipher, last4, err := s.encryptAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	p := &model.AIProvider{
		ID:              uuid.NewString(),
		UserID:          userID,
		CatalogID:       catalogID,
		Name:            name,
		BaseURL:         baseURL,
		APIKeyEncrypted: cipher,
		APIKeyLast4:     last4,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.providers.Create(p); err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.responseOf(p, 0)
	return &resp, nil
}

// Update 指针字段表达「未提供保留」；key 缺省保留，空串拒绝。目录提供商的
// base_url 由目录决定，请求里的 base_url 忽略；custom 才允许改地址。
func (s *AIProviderService) Update(userID, id string, req *UpdateAIProviderRequest) (*AIProviderResponse, error) {
	// key 加密在事务外完成：加密不打库，事务闭包内不引入非事务调用。
	var newCipher []byte
	var newLast4 string
	if req.APIKey != nil {
		apiKey := strings.TrimSpace(*req.APIKey)
		if apiKey == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "api_key 不能为空串；不提供该字段即保留原 key")
		}
		cipher, last4, err := s.encryptAPIKey(apiKey)
		if err != nil {
			return nil, err
		}
		newCipher, newLast4 = cipher, last4
	}

	var updated *model.AIProvider
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewAIProviderRepository(tx)
		p, err := repo.FindByIDAndUser(id, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrAIProviderNotFound
			}
			return errs.ErrInternal
		}
		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				return errs.New(errs.ErrInvalidParams.Code, "name 不能为空")
			}
			p.Name = name
		}
		if req.BaseURL != nil && p.CatalogID == catalog.CustomCatalogID {
			baseURL := catalog.NormalizeBaseURL(*req.BaseURL)
			if !providerValidBaseURL(baseURL) {
				return errs.New(errs.ErrInvalidParams.Code, "base_url 必须是 http(s) 地址")
			}
			p.BaseURL = baseURL
		}
		if req.APIKey != nil {
			p.APIKeyEncrypted = newCipher
			p.APIKeyLast4 = newLast4
		}
		p.UpdatedAt = time.Now().UTC()
		if err := repo.UpdateConfigTx(tx, p, req.APIKey != nil); err != nil {
			return errs.ErrInternal
		}
		// 改了凭证或地址即视为恢复动作（INT-137）：与更新同一事务清除定时
		// 任务熔断，下一次到点正常派发。
		if req.APIKey != nil || req.BaseURL != nil {
			if err := repo.ClearCircuitTx(tx, id); err != nil {
				return errs.ErrInternal
			}
		}
		updated = p
		return nil
	})
	if txErr != nil {
		var appErr *errs.AppError
		if errors.As(txErr, &appErr) {
			return nil, txErr
		}
		return nil, errs.ErrInternal
	}
	counts, err := s.providers.CountBotsByProviderIDs([]string{updated.ID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.responseOf(updated, counts[updated.ID])
	return &resp, nil
}

// Delete 被机器人引用时返回冲突：先显式处理，避免级联出静默失联的机器人。
// 引用检查与删除在同一事务内（生产库单连接天然串行）。
func (s *AIProviderService) Delete(userID, id string) error {
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewAIProviderRepository(tx)
		if _, err := repo.FindByIDAndUser(id, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrAIProviderNotFound
			}
			return errs.ErrInternal
		}
		counts, err := repo.CountBotsByProviderIDs([]string{id})
		if err != nil {
			return errs.ErrInternal
		}
		if counts[id] > 0 {
			return errs.ErrAIProviderInUse
		}
		if err := repo.Delete(id); err != nil {
			return errs.ErrInternal
		}
		return nil
	})
	return txErr
}

func (s *AIProviderService) responseOf(p *model.AIProvider, botCount int64) AIProviderResponse {
	return AIProviderResponse{
		ID:          p.ID,
		CatalogID:   p.CatalogID,
		Name:        p.Name,
		BaseURL:     p.BaseURL,
		APIKeyLast4: p.APIKeyLast4,
		BotCount:    botCount,
		CreatedAt:   p.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ---------- 测试连接 ----------

// Test 只做最小模型探测（streaming / tool_call，与机器人测试连接同一检查项），
// 不写入主线、不落任何配置。目录提供商的地址由目录决定；model 缺省用目录里
// 第一个模型，custom 必须显式给 model。探测不涉及任何用户数据，端点仅以 JWT
// 做准入。
//
// 请求带 provider_id 时先校验归属（非本用户即 404），探测成功则清除该
// provider 的定时任务熔断（INT-137）；不给 provider_id 保持纯探测。
func (s *AIProviderService) Test(userID string, req *TestAIProviderRequest) (*ProviderTestResult, error) {
	providerID := strings.TrimSpace(derefOr(req.ProviderID))
	if providerID != "" {
		if _, err := s.providers.FindByIDAndUser(providerID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrAIProviderNotFound
			}
			return nil, errs.ErrInternal
		}
	}
	apiKey := strings.TrimSpace(derefOr(req.APIKey))
	if apiKey == "" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "api_key 必填")
	}
	catalogID := strings.TrimSpace(derefOr(req.CatalogID))
	baseURL := ""
	if catalogID != "" && catalogID != catalog.CustomCatalogID {
		resolved, ok := catalog.ResolveBaseURL(catalogID)
		if !ok {
			return nil, errs.New(errs.ErrInvalidParams.Code, "catalog_id 不在内置目录中")
		}
		baseURL = resolved
	} else {
		baseURL = catalog.NormalizeBaseURL(derefOr(req.BaseURL))
		if !providerValidBaseURL(baseURL) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "base_url 必须是 http(s) 地址")
		}
	}

	modelName := strings.TrimSpace(derefOr(req.Model))
	if modelName == "" {
		if catalogID == "" || catalogID == catalog.CustomCatalogID {
			return nil, errs.New(errs.ErrInvalidParams.Code, "自定义提供商必须提供 model")
		}
		p, ok := catalog.FindProvider(catalogID)
		if !ok || len(p.Models) == 0 {
			return nil, errs.New(errs.ErrInvalidParams.Code, "目录中该提供商没有可用模型，请显式提供 model")
		}
		modelName = p.Models[0].ID
	}

	checks := make([]ConnectionCheckResult, 0, 2)
	// 三项探测相互独立且都是网络往返：并行发起，总耗时受单次探测超时（
	// botProbeTimeout 30s）约束，而不是串行相加——web 侧对测试连接的总超时
	// 是 35s，串行会先被客户端 abort、拿不到逐项结果（Codex 复审 P2）。
	var streamOK, toolOK bool
	var streamDetail, toolDetail string
	var models []string
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		streamOK, streamDetail = botProbeStreaming(baseURL, modelName, apiKey)
	}()
	go func() {
		defer wg.Done()
		toolOK, toolDetail = botProbeToolCall(baseURL, modelName, apiKey)
	}()
	go func() {
		defer wg.Done()
		// models 列表尽力拉取：可达性已由上面两项检查给出，这里失败不追加
		// 检查项。
		models = providerProbeModelIDs(baseURL, apiKey)
	}()
	wg.Wait()
	checks = append(checks, ConnectionCheckResult{Name: "streaming", Passed: streamOK, Detail: streamDetail})
	checks = append(checks, ConnectionCheckResult{Name: "tool_call", Passed: toolOK, Detail: toolDetail})

	result := &ProviderTestResult{Ok: streamOK && toolOK, Checks: checks, Models: models}
	if result.Ok && providerID != "" {
		// 用户测通了凭证：恢复该 provider 的定时任务调度（清除熔断）。但只有
		// 探测用的密钥与库存一致时才清——测的是新密钥（尚未保存）说明用户
		// 正在排障，此时清掉熔断会让旧密钥的定时任务立刻恢复打上游；保存
		// 路径（Update 提供 api_key）自会清除。清除失败只记日志，不回滚
		// 已成功的探测结果。
		matches, err := s.apiKeyMatchesStored(providerID, apiKey)
		if err != nil {
			s.logger.Warn("比对 provider 库存密钥失败", zap.String("provider_id", providerID), zap.Error(err))
		} else if matches {
			if err := s.providers.ClearCircuit(providerID); err != nil {
				s.logger.Warn("清除 provider 熔断失败", zap.String("provider_id", providerID), zap.Error(err))
			}
		}
	}
	return result, nil
}

// providerProbeModelIDs 尽力拉取 {baseURL}/models 的模型 id 列表，仅用于测试
// 连接响应；任何失败静默返回 nil（复用机器人窗口探测的同一解析，独立超时）。
func providerProbeModelIDs(baseURL, apiKey string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), botWindowDetectTimeout)
	defer cancel()
	det, err := botDetectModelWindows(ctx, baseURL, apiKey, "")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(det.Entries))
	for _, e := range det.Entries {
		if e.Model != "" {
			out = append(out, e.Model)
		}
	}
	return out
}

// providerValidBaseURL 允许 http/https（自托管、内网地址都允许），不追加 /v1。
func providerValidBaseURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// ---------- 密钥 ----------

// apiKeyMatchesStored 报告明文 key 是否与库存密文解密后一致：测试连接仅在
// 密钥与库存一致时才清除熔断（INT-137）。provider 不存在返回 false 与 NotFound。
func (s *AIProviderService) apiKeyMatchesStored(providerID, plain string) (bool, error) {
	p, err := s.providers.GetByID(providerID)
	if err != nil {
		return false, err
	}
	stored, err := crypto.Decrypt(p.APIKeyEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return false, err
	}
	return string(stored) == plain, nil
}

func (s *AIProviderService) encryptAPIKey(plain string) ([]byte, string, error) {
	cipher, err := crypto.Encrypt([]byte(plain), []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, "", errs.ErrInternal
	}
	return cipher, botKeyLast4(plain), nil
}

func derefOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
