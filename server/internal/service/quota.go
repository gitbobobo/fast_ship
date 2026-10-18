package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/quota"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// QuotaService 管理订阅额度账号（INT-109）：凭据校验与加密、CRUD、探测与手动刷新、
// 抓取结果落库。取数本身在 pkg/quota 的 fetcher 里，经 registry 注入点交给测试替换；
// 定时调度在 quota_scheduler.go。
//
// 凭据整组以 JSON 加密进 credentials_encrypted（AES-256-GCM），credential_hint 只存
// 末 4 位；响应、日志与错误消息中绝不出现明文。单账号的并发抓取（手动刷新与调度
// 可能撞上）由 fetchLocks 按账号串行化。
type QuotaService struct {
	repo     *repository.QuotaAccountRepository
	cfg      *config.Config
	logger   *zap.Logger
	registry func() []quota.Provider
	client   *http.Client
	now      func() time.Time

	fetchLocks sync.Map // account ID → *sync.Mutex，串行化同一账号的抓取

	// 设备授权会话表（INT-117）：只存内存，进程重启即丢，之后 GET 返回 40431。
	// 上游设备密钥与兑换到的凭据只存在会话对象里，响应与日志都不出现。
	deviceMu       sync.Mutex
	deviceSessions map[string]*quotaDeviceSession

	// kick 让后台调度器在抓取结果落库后重算等待：HTTP 侧的新建/换凭据/
	// 手动刷新都会写更早的 next_retry_at，而调度器可能正睡一个更长的定时器。
	// main 在启动调度前注入；测试不设时为 nil（无操作）。
	kick func()
}

// SetKick 注入调度器唤醒函数，在启动调度前调用一次。
func (s *QuotaService) SetKick(kick func()) { s.kick = kick }

func (s *QuotaService) kickSchedule() {
	if s.kick != nil {
		s.kick()
	}
}

// NewQuotaService 装配额度服务。registry 为 nil 时用真实供应商注册表
// （quota.Registry(cfg.Quota.BaseURLs)）；测试注入假 registry。
func NewQuotaService(repo *repository.QuotaAccountRepository, cfg *config.Config, logger *zap.Logger, registry func() []quota.Provider) *QuotaService {
	if registry == nil {
		registry = func() []quota.Provider { return quota.Registry(cfg.Quota.BaseURLs) }
	}
	return &QuotaService{
		repo:           repo,
		cfg:            cfg,
		logger:         logger,
		registry:       registry,
		client:         &http.Client{},
		now:            time.Now,
		deviceSessions: map[string]*quotaDeviceSession{},
	}
}

func (s *QuotaService) providerByID(id string) (quota.Provider, bool) {
	for _, p := range s.registry() {
		if p.ID == id {
			return p, true
		}
	}
	return quota.Provider{}, false
}

// validateCredentials 按供应商元数据校验并清洗凭据：必填字段缺失或空白 → 40031；
// 供应商自带的 Validate 钩子（如 OpenDesign 拦模型面密钥）随后执行，返回非空
// 消息也 40031。返回只含已声明字段、各值已 trim 的凭据组（多余字段不存）。
func (s *QuotaService) validateCredentials(provider quota.Provider, raw map[string]string) (quota.Credentials, *errs.AppError) {
	creds := make(quota.Credentials, len(provider.CredentialFields))
	for _, field := range provider.CredentialFields {
		value := strings.TrimSpace(raw[field.Key])
		if field.Required && value == "" {
			return nil, errs.New(errs.ErrQuotaCredentialsInvalid.Code,
				fmt.Sprintf("凭据字段缺失或为空：%s", field.Label))
		}
		creds[field.Key] = value
	}
	if provider.Validate != nil {
		if message := provider.Validate(creds); message != "" {
			return nil, errs.New(errs.ErrQuotaCredentialsInvalid.Code, message)
		}
	}
	return creds, nil
}

// validQuotaLabel 清洗并校验账号标签：trim 后为空 → 40001（binding 的 min=1 按
// 字符数计，纯空白能漏过；空白标题会让卡片标题与 aria-label 变空）。返回 trim 后的值。
func validQuotaLabel(raw string) (string, *errs.AppError) {
	label := strings.TrimSpace(raw)
	if label == "" {
		return "", errs.New(errs.ErrInvalidParams.Code, "标签不能为空白")
	}
	return label, nil
}

// credentialHintMinReveal 是回显末 4 位所需的最小凭据长度。更短的凭据整段打码：
// 末 4 位等于全文或大半个文，而 credential_hint 会随账号列表返回（含 API Key
// 只读路径），回显即等于泄漏明文。
const credentialHintMinReveal = 8

// credentialHintMasked 是短凭据的固定遮罩，不含原文字符也不透露长度。
const credentialHintMasked = "…••••"

// credentialHint 取首个必填密文字段的末 4 位（无则首个密文字段、再退化首个字段），
// 形如 …ab12；凭据不足 credentialHintMinReveal 位时整段打码。
func credentialHint(provider quota.Provider, creds quota.Credentials) string {
	key := ""
	for _, field := range provider.CredentialFields {
		if field.Secret && field.Required {
			key = field.Key
			break
		}
	}
	if key == "" {
		for _, field := range provider.CredentialFields {
			if field.Secret {
				key = field.Key
				break
			}
		}
	}
	if key == "" && len(provider.CredentialFields) > 0 {
		key = provider.CredentialFields[0].Key
	}
	value := []rune(creds[key])
	if len(value) < credentialHintMinReveal {
		return credentialHintMasked
	}
	return "…" + string(value[len(value)-4:])
}

func (s *QuotaService) encryptCredentials(creds quota.Credentials) ([]byte, *errs.AppError) {
	raw, err := json.Marshal(creds)
	if err != nil {
		return nil, errs.ErrInternal
	}
	encrypted, err := crypto.Encrypt(raw, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, errs.ErrInternal
	}
	return encrypted, nil
}

func (s *QuotaService) decryptCredentials(account *model.QuotaAccount) (quota.Credentials, error) {
	raw, err := crypto.Decrypt(account.CredentialsEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, err
	}
	var creds quota.Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// ListProviders 返回供应商元数据（固定顺序），供前端动态渲染表单。
func (s *QuotaService) ListProviders() ([]api.QuotaProvider, error) {
	providers := s.registry()
	out := make([]api.QuotaProvider, 0, len(providers))
	for _, p := range providers {
		fields := make([]api.QuotaCredentialField, 0, len(p.CredentialFields))
		for _, f := range p.CredentialFields {
			fields = append(fields, api.QuotaCredentialField{
				Key: f.Key, Label: f.Label, Secret: f.Secret,
				Required: f.Required, Placeholder: f.Placeholder, Help: f.Help,
			})
		}
		instructions := p.Instructions
		if instructions == nil {
			instructions = []string{}
		}
		out = append(out, api.QuotaProvider{
			Id: p.ID, Name: p.Name, PlanHint: p.PlanHint,
			CredentialFields: fields, Instructions: instructions, Warning: p.Warning,
			DeviceAuthorization: p.DeviceAuth != nil,
		})
	}
	return out, nil
}

func (s *QuotaService) List(userID string) ([]api.QuotaAccount, error) {
	accounts, err := s.repo.ListByUserID(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	result := make([]api.QuotaAccount, 0, len(accounts))
	for i := range accounts {
		result = append(result, s.toAccountResponse(&accounts[i]))
	}
	return result, nil
}

// Create 校验并保存新账号（初始 pending），随后同步抓取一次——抓取失败仍返回
// 200，结果落在 status。同用户重复添加相同凭据允许（不存明文指纹，无从去重）。
func (s *QuotaService) Create(userID string, req *CreateQuotaAccountRequest) (*api.QuotaAccount, error) {
	id := uuid.NewString()
	if appErr := s.createWithID(userID, id, req); appErr != nil {
		return nil, appErr
	}
	s.FetchAccountNow(context.Background(), id)
	return s.respond(id, userID)
}

// createWithID 只做「校验 + 加密 + 插入」，供普通建号与设备授权的自动建号共用。
// 设备授权路径用它以预分配的 accountID 幂等重试：插入命中已存在的同 id 行
// 视为成功（前次重试可能已落库但会话未推进），不产生第二个账号。
func (s *QuotaService) createWithID(userID, accountID string, req *CreateQuotaAccountRequest) *errs.AppError {
	provider, appErr := s.requireProvider(req.Provider)
	if appErr != nil {
		return appErr
	}
	label, appErr := validQuotaLabel(req.Label)
	if appErr != nil {
		return appErr
	}
	creds, appErr := s.validateCredentials(provider, req.Credentials)
	if appErr != nil {
		return appErr
	}
	encrypted, appErr := s.encryptCredentials(creds)
	if appErr != nil {
		return appErr
	}
	account := &model.QuotaAccount{
		ID:             accountID,
		UserID:         userID,
		Provider:       provider.ID,
		Label:          label,
		CredentialHint: credentialHint(provider, creds),
		Status:         model.QuotaStatusPending,
		SnapshotJSON:   "",
	}
	account.CredentialsEncrypted = encrypted
	if err := s.repo.Create(account); err != nil {
		if _, findErr := s.repo.FindByIDAny(accountID); findErr == nil {
			return nil
		}
		return errs.ErrInternal
	}
	return nil
}

// Update 改标签或整组替换凭据。改标签不触发抓取；替换凭据把状态打回 pending、
// 清退避，并同步重抓一次（旧快照保留到新快照写入）。
func (s *QuotaService) Update(userID, id string, req *UpdateQuotaAccountRequest) (*api.QuotaAccount, error) {
	account, err := s.repo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrQuotaAccountNotFound
		}
		return nil, errs.ErrInternal
	}

	// 显式传入的 label 先做校验再产生副作用——凭据替换会触发抓取，
	// 不能等改完才报 40001。
	var label *string
	if req.Label != nil {
		trimmed, appErr := validQuotaLabel(*req.Label)
		if appErr != nil {
			return nil, appErr
		}
		label = &trimmed
	}

	if req.Credentials != nil {
		provider, appErr := s.requireProvider(account.Provider)
		if appErr != nil {
			return nil, appErr
		}
		creds, appErr := s.validateCredentials(provider, *req.Credentials)
		if appErr != nil {
			return nil, appErr
		}
		encrypted, appErr := s.encryptCredentials(creds)
		if appErr != nil {
			return nil, appErr
		}
		if err := s.repo.UpdateCredentials(id, userID, encrypted, credentialHint(provider, creds)); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrQuotaAccountNotFound
			}
			return nil, errs.ErrInternal
		}
		// 换凭据触发的同步抓取刻意不受手动刷新冷却约束：用户主动换凭据
		// 就应当立即验证新凭据，而不是被上次抓取的冷却挡住。
		s.FetchAccountNow(context.Background(), id)
	}
	if label != nil {
		if err := s.repo.UpdateLabel(id, userID, *label); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrQuotaAccountNotFound
			}
			return nil, errs.ErrInternal
		}
	}
	return s.respond(id, userID)
}

func (s *QuotaService) Delete(userID, id string) error {
	if err := s.repo.Delete(id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrQuotaAccountNotFound
		}
		return errs.ErrInternal
	}
	// 连带清掉该账号的抓取锁，fetchLocks 不随账号数增长。
	s.fetchLocks.LoadAndDelete(id)
	return nil
}

// Refresh 立即抓一次并落库。距上次成功抓取在冷却内返回 42900（message 含等待秒数）；
// 抓取结果（成败）落在账号 status 上，HTTP 仍为 200。
func (s *QuotaService) Refresh(userID, id string) (*api.QuotaAccount, error) {
	if _, err := s.repo.FindByID(id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrQuotaAccountNotFound
		}
		return nil, errs.ErrInternal
	}

	mu := s.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	// 锁内重读：等锁期间另一次抓取可能已完成，冷却按最新数据时间判。
	account, err := s.repo.FindByIDAny(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrQuotaAccountNotFound
		}
		return nil, errs.ErrInternal
	}
	if account.LastFetchedAt != nil {
		cooldown := s.cfg.Quota.ManualRefreshCooldown()
		if remaining := cooldown - s.now().UTC().Sub(*account.LastFetchedAt); remaining > 0 {
			secs := int(remaining.Seconds() + 0.5)
			return nil, errs.New(errs.ErrQuotaRefreshTooOften.Code,
				fmt.Sprintf("刷新过于频繁，请在 %d 秒后重试", secs))
		}
	}

	s.fetchLocked(context.Background(), account)
	return s.respond(id, userID)
}

// Probe 用一组凭据试抓一次，不落库、不改任何账号状态（POST /api/quota/test）。
// 结果（含失败）走 QuotaProbe 返回，HTTP 恒 200；只有校验错误（40030/40031）走错误码。
func (s *QuotaService) Probe(req *TestQuotaCredentialsRequest) (*api.QuotaProbe, error) {
	provider, appErr := s.requireProvider(req.Provider)
	if appErr != nil {
		return nil, appErr
	}
	creds, appErr := s.validateCredentials(provider, req.Credentials)
	if appErr != nil {
		return nil, appErr
	}
	probe := &api.QuotaProbe{Windows: []api.QuotaWindow{}}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Quota.RequestTimeout())
	defer cancel()
	snapshot, err := provider.Fetch(ctx, s.client, provider.DefaultBaseURL, creds)
	if err != nil {
		var fetchErr *quota.FetchError
		probe.Status = api.QuotaProbeStatusProviderError
		if errors.As(err, &fetchErr) {
			if fetchErr.Kind == quota.ErrKindCredential {
				probe.Status = api.QuotaProbeStatusCredentialInvalid
			}
			probe.Error = fetchErr.Message
			if fetchErr.StatusCode != 0 {
				probe.Detail = fmt.Sprintf("HTTP %d", fetchErr.StatusCode)
			}
		} else {
			probe.Error = "抓取失败，请稍后重试"
			s.logger.Warn("quota probe failed with unexpected error", zap.String("provider", provider.ID), zap.Error(err))
		}
		return probe, nil
	}
	probe.Status = api.QuotaProbeStatusOk
	probe.PlanName = snapshot.PlanName
	probe.Note = snapshot.Note
	probe.Windows = snapshotWindows(snapshot)
	probe.Balance = snapshotBalance(snapshot)
	return probe, nil
}

// FetchAccountNow 供调度器按账号 id 抓一次（内部串行化）。行已删除时静默跳过。
func (s *QuotaService) FetchAccountNow(ctx context.Context, accountID string) {
	mu := s.lockFor(accountID)
	mu.Lock()
	defer mu.Unlock()
	account, err := s.repo.FindByIDAny(accountID)
	if err != nil {
		return
	}
	s.fetchLocked(ctx, account)
}

// fetchLocked 执行一次抓取并落库；调用方必须已持有该账号的抓取锁。
// 成功写快照与调度清零（窗口临近重置时 next_retry_at 提前到 reset+30s）；
// 失败只写 status/last_error/failure_count/next_retry_at，保留上次成功快照。
func (s *QuotaService) fetchLocked(ctx context.Context, account *model.QuotaAccount) {
	// 无论成功还是失败都写了 next_retry_at，结束后让调度器按新值重算等待。
	defer s.kickSchedule()
	now := s.now().UTC()
	provider, ok := s.providerByID(account.Provider)
	if !ok {
		s.storeFailure(account, model.QuotaStatusProviderError, "供应商配置已失效，请删除后重新添加", now)
		return
	}
	creds, err := s.decryptCredentials(account)
	if err != nil {
		s.logger.Error("decrypt quota credentials failed",
			zap.String("account_id", account.ID), zap.Error(err))
		s.storeFailure(account, model.QuotaStatusProviderError, "凭据解密失败，请更新凭据", now)
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, s.cfg.Quota.RequestTimeout())
	defer cancel()
	snapshot, err := provider.Fetch(fetchCtx, s.client, provider.DefaultBaseURL, creds)
	if err != nil {
		var fetchErr *quota.FetchError
		status := model.QuotaStatusProviderError
		message := "抓取失败，请稍后重试"
		if errors.As(err, &fetchErr) {
			message = fetchErr.Message
			if fetchErr.Kind == quota.ErrKindCredential {
				status = model.QuotaStatusCredentialInvalid
			}
		} else {
			s.logger.Warn("quota fetch failed with unexpected error",
				zap.String("account_id", account.ID), zap.String("provider", account.Provider), zap.Error(err))
		}
		s.storeFailure(account, status, message, now)
		return
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		s.logger.Error("marshal quota snapshot failed", zap.String("account_id", account.ID), zap.Error(err))
		s.storeFailure(account, model.QuotaStatusProviderError, "快照数据异常，请稍后重试", now)
		return
	}
	fetchedAt := snapshot.FetchedAt.UTC()
	// 数据时间夹到「至今」：fetcher 时钟偏快产出的未来值会把手动刷新
	// 冷却锁死（now - last_fetched_at 恒负 → 永远 42900），且每次成功
	// 都用同一个未来值覆盖不会自愈；零值回退当前时间。
	if fetchedAt.IsZero() || fetchedAt.After(now) {
		fetchedAt = now
	}
	nextRetry := nextRetryAfterSuccess(now, &snapshot, s.cfg.Quota.Interval())
	if err := s.repo.SaveFetchSuccess(account.ID, string(raw), snapshot.PlanName, fetchedAt, &nextRetry); err != nil {
		s.logger.Error("save quota fetch result failed", zap.String("account_id", account.ID), zap.Error(err))
	}
}

// storeFailure 写失败四件套与指数退避：next_retry_at = now + min(2^n 分钟, 30 分钟)。
func (s *QuotaService) storeFailure(account *model.QuotaAccount, status, message string, now time.Time) {
	n := account.FailureCount + 1
	minutes := 30
	if n < 5 {
		minutes = 1 << n
	}
	next := now.Add(time.Duration(minutes) * time.Minute)
	if err := s.repo.SaveFetchFailure(account.ID, status, message, n, next); err != nil {
		s.logger.Error("save quota fetch failure failed", zap.String("account_id", account.ID), zap.Error(err))
	}
}

// nextRetryAfterSuccess 算成功抓取后的下一次抓取时间：min(now + interval,
// 开窗 reset_at + 30s)。必须落绝对时间——NULL 只保留给「从未抓取过」的账号，
// 否则 ListDue 把 NULL 当立即到期，任何一个失败账号的退避唤醒都会把所有
// 健康账号一起重抓（供应商请求频率被拉到失败账号的退避节奏上）。
func nextRetryAfterSuccess(now time.Time, snapshot *quota.Snapshot, interval time.Duration) time.Time {
	next := now.Add(interval)
	for _, w := range snapshot.Windows {
		if w.ResetAt == nil {
			continue
		}
		reset := w.ResetAt.UTC()
		if !reset.After(now) || reset.After(next) {
			continue
		}
		if t := reset.Add(30 * time.Second); t.Before(next) {
			next = t
		}
	}
	return next
}

func (s *QuotaService) lockFor(accountID string) *sync.Mutex {
	mu, _ := s.fetchLocks.LoadOrStore(accountID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (s *QuotaService) requireProvider(id string) (quota.Provider, *errs.AppError) {
	provider, ok := s.providerByID(id)
	if !ok {
		return quota.Provider{}, errs.New(errs.ErrQuotaProviderUnknown.Code,
			fmt.Sprintf("未知的额度供应商：%s", id))
	}
	return provider, nil
}

// respond 重读行并投影为响应类型（抓取后字段已被 fetchLocked 更新）。
func (s *QuotaService) respond(id, userID string) (*api.QuotaAccount, error) {
	account, err := s.repo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrQuotaAccountNotFound
		}
		return nil, errs.ErrInternal
	}
	resp := s.toAccountResponse(account)
	return &resp, nil
}

// toAccountResponse 把存储模型投影为契约响应：窗口/余额从快照原文还原，
// provider_name 取当前注册表显示名（供应商已下线时回退 id）。
func (s *QuotaService) toAccountResponse(account *model.QuotaAccount) api.QuotaAccount {
	providerName := account.Provider
	if provider, ok := s.providerByID(account.Provider); ok {
		providerName = provider.Name
	}
	resp := api.QuotaAccount{
		Id:             account.ID,
		Provider:       account.Provider,
		ProviderName:   providerName,
		Label:          account.Label,
		CredentialHint: account.CredentialHint,
		PlanName:       account.PlanName,
		Status:         api.QuotaAccountStatus(account.Status),
		LastError:      account.LastError,
		FetchedAt:      api.JSONTimePtr(account.LastFetchedAt),
		LastSuccessAt:  api.JSONTimePtr(account.LastSuccessAt),
		Windows:        []api.QuotaWindow{},
		CreatedAt:      api.JSONTime(account.CreatedAt),
		UpdatedAt:      api.JSONTime(account.UpdatedAt),
	}
	if account.SnapshotJSON == "" {
		return resp
	}
	var snapshot quota.Snapshot
	if err := json.Unmarshal([]byte(account.SnapshotJSON), &snapshot); err != nil {
		s.logger.Warn("quota snapshot_json unreadable", zap.String("account_id", account.ID), zap.Error(err))
		return resp
	}
	resp.Windows = snapshotWindows(snapshot)
	resp.Balance = snapshotBalance(snapshot)
	resp.Note = snapshot.Note
	return resp
}

func snapshotWindows(snapshot quota.Snapshot) []api.QuotaWindow {
	windows := make([]api.QuotaWindow, 0, len(snapshot.Windows))
	for _, w := range snapshot.Windows {
		windows = append(windows, api.QuotaWindow{
			Kind:        api.QuotaWindowKind(w.Kind),
			Label:       w.Label,
			UsedPercent: w.UsedPercent,
			Used:        w.Used,
			Limit:       w.Limit,
			Unit:        api.QuotaWindowUnit(w.Unit),
			ResetAt:     api.JSONTimePtr(w.ResetAt),
		})
	}
	return windows
}

func snapshotBalance(snapshot quota.Snapshot) *api.QuotaBalance {
	if snapshot.Balance == nil {
		return nil
	}
	return &api.QuotaBalance{
		Label:    snapshot.Balance.Label,
		Amount:   snapshot.Balance.Amount,
		Currency: snapshot.Balance.Currency,
	}
}
