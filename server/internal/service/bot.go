package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// BotService 是机器人的应用入口：管理 CRUD、测试连接、发消息、运行控制、
// 确认授权与后台调用执行。调度保持单进程——数据库是事实来源，内存 channel
// 只负责唤醒，丢失唤醒可由轻量扫描补回。
type BotService struct {
	db     *gorm.DB
	cfg    *config.Config
	logger *zap.Logger

	bots       *repository.BotRepository
	messages   *repository.BotMessageRepository
	events     *repository.BotEventRepository
	runs       *repository.BotRunRepository
	requests   *repository.BotModelRequestRepository
	calls      *repository.BotToolCallRepository
	authz      *repository.BotAuthorizationRepository
	schedTasks *repository.BotScheduleTaskRepository
	windows    *repository.BotWindowRepository

	projects *ProjectService
	issues   *IssueService
	threads  *T3ThreadsService
	bindings *T3BindingService

	// runtime 与 worker 见 bot_runtime.go / bot_tool_runner.go。
	rt *botRuntimeState

	// 后台 worker：channel 只负责唤醒，队列满/丢失推送由扫描补回。
	workCh      chan string
	workOnce    sync.Once
	cancelMu    sync.Mutex
	callCancels map[string]context.CancelFunc

	// streamHub 是 SSE 订阅注册表；事件先落库再发布，订阅方按 seq 去重。
	streamHub *botStreamHub

	// nowFn 可注入时钟（夜间整理测试）；空则用 time.Now。
	nowFn func() time.Time
	// maintJobs 是全局单一维护队列（夜间整理）；compaction 走主线不入队。
	maintJobs chan botMaintJob
	maintOnce sync.Once
}

type botMaintJob struct {
	botID      string
	triggerKey string
}

func NewBotService(
	db *gorm.DB,
	cfg *config.Config,
	logger *zap.Logger,
	projects *ProjectService,
	issues *IssueService,
	threads *T3ThreadsService,
	bindings *T3BindingService,
) *BotService {
	return &BotService{
		db:         db,
		cfg:        cfg,
		logger:     logger,
		bots:       repository.NewBotRepository(db),
		messages:   repository.NewBotMessageRepository(db),
		events:     repository.NewBotEventRepository(db),
		runs:       repository.NewBotRunRepository(db),
		requests:   repository.NewBotModelRequestRepository(db),
		calls:      repository.NewBotToolCallRepository(db),
		authz:      repository.NewBotAuthorizationRepository(db),
		schedTasks: repository.NewBotScheduleTaskRepository(db),
		windows:    repository.NewBotWindowRepository(db),
		projects:   projects,
		issues:     issues,
		threads:    threads,
		bindings:   bindings,
		rt:         newBotRuntimeState(),
		streamHub:  newBotStreamHub(),
	}
}

// ---------- 响应类型 ----------

// BotResponse 管理面响应；key 全链路脱敏，只回末四位。NextEventSeq 是
// 事件账本水位：详情页订阅以它-1 起读，与消息快照衔接（不从 0 重放）。
// 窗口能力四字段（INT-115）按「manual > detected > fallback」解析，
// ContextSource 如实区分来源，展示层不得把回退值说成已检测。
type BotResponse struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	PersonaPrompt         string `json:"persona_prompt"`
	PromptRevision        int64  `json:"prompt_revision"`
	PlatformPromptVersion int64  `json:"platform_prompt_version"`
	ToolsetVersion        int64  `json:"toolset_version"`
	BaseURL               string `json:"base_url"`
	Model                 string `json:"model"`
	APIKeyLast4           string `json:"api_key_last4"`
	NextEventSeq          int64  `json:"next_event_seq"`
	ContextWindow         int64  `json:"context_window"`
	MaxOutputTokens       int64  `json:"max_output_tokens"`
	ContextSource         string `json:"context_source"` // manual|detected|known|fallback
	ContextDetail         string `json:"context_detail,omitempty"`
	// LastMessage 是主线最近一条正文 trim 后非空的 user/assistant 消息预览
	// （INT-125）。仅 GET /api/bots 列表端点填充；详情与聊天快照等其它端点保持
	// nil，无 omitempty 让 null 显式出现，前端按 null 处理。
	LastMessage *BotLastMessage `json:"last_message"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// BotLastMessage 是机器人列表端点的最近消息预览：主线（不含定时任务会话）
// 最近一条正文 trim 后非空的 user/assistant 消息，按 seq 最大取。契约只有
// kind/text 两个字段（左栏只读 text，kind 用于区分「你：」前缀），不带回消息
// 时间。Text 返回完整正文，截断展示由前端负责。
type BotLastMessage struct {
	// Kind user / assistant。
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// botResponseOf 是 bot 的 API 投影；需要服务解析窗口能力，因此是方法。
func (s *BotService) botResponseOf(b *model.Bot) BotResponse {
	cap := s.modelCapability(b, b.Model)
	return BotResponse{
		ID:                    b.ID,
		Name:                  b.Name,
		PersonaPrompt:         b.PersonaPrompt,
		PromptRevision:        b.PromptRevision,
		PlatformPromptVersion: b.PlatformPromptVersion,
		ToolsetVersion:        b.ToolsetVersion,
		BaseURL:               b.BaseURL,
		Model:                 b.Model,
		APIKeyLast4:           b.APIKeyLast4,
		NextEventSeq:          b.NextEventSeq,
		ContextWindow:         cap.WindowTokens,
		MaxOutputTokens:       cap.MaxOutputTokens,
		ContextSource:         cap.Source,
		ContextDetail:         cap.Detail,
		CreatedAt:             b.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:             b.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type CreateBotRequest struct {
	Name          string `json:"name"`
	PersonaPrompt string `json:"persona_prompt"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	APIKey        string `json:"api_key"`
}

type UpdateBotRequest struct {
	Name          *string `json:"name,omitempty"`
	PersonaPrompt *string `json:"persona_prompt,omitempty"`
	BaseURL       *string `json:"base_url,omitempty"`
	Model         *string `json:"model,omitempty"`
	// APIKey 指针三态：缺省保留原 key；空串拒绝（避免误清空）；非空替换。
	APIKey *string `json:"api_key,omitempty"`
}

// ConnectionCheckResult 是测试连接的单项检查结果。
type ConnectionCheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type TestConnectionResponse struct {
	Ok     bool                    `json:"ok"`
	Checks []ConnectionCheckResult `json:"checks"`
	// 窗口能力（INT-115）：同步探测成功时为 detected 并写入缓存；探测失败
	// 回退到 capabilityFor 的解析结果（manual / 默认窗口），来源如实标注。
	ContextWindow   int64  `json:"context_window"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	ContextSource   string `json:"context_source"`
	ContextDetail   string `json:"context_detail,omitempty"`
}

type TestConnectionRequest struct {
	// BotID 可选：给定时用已保存配置测试；同时给 BotID 与表单字段时以表单
	// 覆盖地址/模型（key 仍用已保存的）。
	BotID   *string `json:"bot_id,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
	Model   *string `json:"model,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
}

// ---------- 管理 CRUD ----------

func (s *BotService) List(userID string) ([]BotResponse, error) {
	bots, err := s.bots.ListByUser(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	// 最近消息预览一次批量取（INT-125），只在本列表端点填充；未命中的机器人
	// 保持 nil，序列化为显式 null。
	ids := make([]string, 0, len(bots))
	for i := range bots {
		ids = append(ids, bots[i].ID)
	}
	lasts, err := s.bots.LastMessagesByBotIDs(ids)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotResponse, 0, len(bots))
	for i := range bots {
		resp := s.botResponseOf(&bots[i])
		if row, ok := lasts[bots[i].ID]; ok {
			resp.LastMessage = &BotLastMessage{Kind: row.Kind, Text: row.Text}
		}
		out = append(out, resp)
	}
	return out, nil
}

func (s *BotService) Get(userID, botID string) (*BotResponse, error) {
	bot, err := s.bots.FindByIDAndUser(botID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotNotFound
		}
		return nil, errs.ErrInternal
	}
	resp := s.botResponseOf(bot)
	return &resp, nil
}

func (s *BotService) Create(userID string, req *CreateBotRequest) (*BotResponse, error) {
	bot, err := s.buildBot(userID, req)
	if err != nil {
		return nil, err
	}
	if err := s.bots.Create(bot); err != nil {
		return nil, errs.ErrInternal
	}
	// 窗口探测 best-effort 后台执行：不阻塞响应，失败不影响创建结果。
	s.refreshBotModelWindowAsync(bot.ID)
	resp := s.botResponseOf(bot)
	return &resp, nil
}

// buildBot 校验并装配新机器人配置；key 加密落库，任何路径不落明文。
func (s *BotService) buildBot(userID string, req *CreateBotRequest) (*model.Bot, error) {
	name := strings.TrimSpace(req.Name)
	baseURL := strings.TrimSpace(req.BaseURL)
	modelName := strings.TrimSpace(req.Model)
	apiKey := strings.TrimSpace(req.APIKey)
	if name == "" || baseURL == "" || modelName == "" || apiKey == "" {
		return nil, errs.ErrInvalidParams
	}
	if !botValidBaseURL(baseURL) {
		return nil, errs.New(errs.ErrInvalidParams.Code, "base_url 必须是 http(s) 地址")
	}

	cipher, last4, err := s.encryptAPIKey(apiKey)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	return &model.Bot{
		ID:                    uuid.NewString(),
		UserID:                userID,
		Name:                  name,
		PersonaPrompt:         req.PersonaPrompt,
		SystemPromptText:      buildBotSystemPrompt(req.PersonaPrompt),
		PromptRevision:        1,
		PlatformPromptVersion: botPlatformPromptVersion,
		ToolsetVersion:        botToolsetVersion,
		LastToldVersion:       botToolsetVersion,
		BaseURL:               baseURL,
		Model:                 modelName,
		APIKeyEncrypted:       cipher,
		APIKeyLast4:           last4,
		NextMessageSeq:        1,
		NextEventSeq:          1,
		OfferedInputSeq:       0,
		CreatedAt:             now,
		UpdatedAt:             now,
	}, nil
}

// Update 指针字段表达「未提供保留」；key 缺省保留，空串拒绝。
// 人设变化重建系统提示词并递增 revision。配置变化对下个 run 生效。
// 读行、应用请求与写库收进同一短事务：生产库单连接让读-改-写与并发写者
// 串行，不会把事务外读到的旧状态写回。人设未变时按列跳过提示词四列
// （persona_prompt/system_prompt_text/prompt_revision/platform_prompt_version），
// 改名/换模型/换 key 不得反向撤销并发激活写入的提示词状态。
func (s *BotService) Update(userID, botID string, req *UpdateBotRequest) (*BotResponse, error) {
	// key 加密在事务外完成：加密不打库，提前做掉，事务闭包内不引入
	// 非事务调用。
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

	var updated *model.Bot
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotRepository(tx)
		bot, err := repo.FindByIDAndUser(botID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrBotNotFound
			}
			return errs.ErrInternal
		}

		personaChanged := false
		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				return errs.New(errs.ErrInvalidParams.Code, "name 不能为空")
			}
			bot.Name = name
		}
		if req.PersonaPrompt != nil && *req.PersonaPrompt != bot.PersonaPrompt {
			bot.PersonaPrompt = *req.PersonaPrompt
			bot.SystemPromptText = buildBotSystemPrompt(bot.PersonaPrompt)
			bot.PromptRevision++
			bot.PlatformPromptVersion = botPlatformPromptVersion
			personaChanged = true
		}
		if req.BaseURL != nil {
			baseURL := strings.TrimSpace(*req.BaseURL)
			if baseURL == "" {
				return errs.New(errs.ErrInvalidParams.Code, "base_url 不能为空")
			}
			if !botValidBaseURL(baseURL) {
				return errs.New(errs.ErrInvalidParams.Code, "base_url 必须是 http(s) 地址")
			}
			bot.BaseURL = baseURL
		}
		if req.Model != nil {
			modelName := strings.TrimSpace(*req.Model)
			if modelName == "" {
				return errs.New(errs.ErrInvalidParams.Code, "model 不能为空")
			}
			bot.Model = modelName
		}
		if req.APIKey != nil {
			bot.APIKeyEncrypted = newCipher
			bot.APIKeyLast4 = newLast4
		}

		bot.UpdatedAt = time.Now().UTC()
		if err := repo.UpdateConfigTx(tx, bot, personaChanged); err != nil {
			return errs.ErrInternal
		}
		updated = bot
		return nil
	})
	if txErr != nil {
		// 已知哨兵（不存在、参数校验等业务错误）原样透出，其余按内部错误
		// 收敛——与 SendMessage 的事务错误映射同一约定。
		var appErr *errs.AppError
		if errors.As(txErr, &appErr) {
			return nil, txErr
		}
		return nil, errs.ErrInternal
	}
	// 地址或模型可能已变：后台 best-effort 刷新窗口缓存，不阻塞响应。
	s.refreshBotModelWindowAsync(updated.ID)
	resp := s.botResponseOf(updated)
	return &resp, nil
}

// Delete 有活动 run 或未终结调用时返回冲突：先显式处理，避免静默丢任务。
// pending_confirmation / queued 的调用也算未终结（rejected/cancelled 等终态
// 不算）。冲突检查与删除在同一事务内：发送准入（NextMessageSeqTx 取号）与
// 删除天然串行，先删则并发发送因 bot 缺失失败，先入账则删除撞活动 run。
func (s *BotService) Delete(userID, botID string) error {
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := repository.NewBotRepository(tx).FindByIDAndUser(botID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrBotNotFound
			}
			return errs.ErrInternal
		}
		if _, err := repository.NewBotRunRepository(tx).FindActiveByBot(botID); err == nil {
			return errs.ErrBotDeleteConflict
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrInternal
		}
		// 定时任务 run 不占主线槽，删除时同样视为进行中：避免级联删除一个
		// 正在执行的会话。
		var activeSchedule int64
		if err := tx.Model(&model.BotRun{}).
			Where("bot_id = ? AND finished_at IS NULL AND session_kind = ?",
				botID, model.BotSessionKindSchedule).
			Count(&activeSchedule).Error; err != nil {
			return errs.ErrInternal
		}
		if activeSchedule > 0 {
			return errs.ErrBotDeleteConflict
		}
		active, err := repository.NewBotToolCallRepository(tx).ListByBot(botID, []string{
			string(model.BotCallStatusPendingConfirmation),
			string(model.BotCallStatusQueued),
			string(model.BotCallStatusRunning),
		}, 1)
		if err != nil {
			return errs.ErrInternal
		}
		if len(active) > 0 {
			return errs.ErrBotDeleteConflict
		}
		// 未消费输入也算冲突：消息事务已提交但 run 尚未由调度异步创建，
		// 此时删除会把刚受理的消息级联掉，而发送方已拿到成功响应。判定用
		// 真实的输入消息（user/lifecycle）——后台结果等非输入消息也占
		// seq，裸水位比较会永远冲突。
		var bot struct {
			OfferedInputSeq int64
		}
		if err := tx.Raw("SELECT offered_input_seq FROM bots WHERE id = ?", botID).Scan(&bot).Error; err != nil {
			return errs.ErrInternal
		}
		hasNew, err := repository.NewBotMessageRepository(tx).HasModelInputSinceTx(tx, botID, model.BotSessionKindMain, "", bot.OfferedInputSeq)
		if err != nil {
			return errs.ErrInternal
		}
		if hasNew {
			return errs.ErrBotDeleteConflict
		}
		if err := repository.NewBotRepository(tx).Delete(botID); err != nil {
			return errs.ErrInternal
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}
	return nil
}

// ---------- 测试连接 ----------

// TestConnection 只做最小模型探测，不写入主线。流式与工具调用兼容性分别返回。
func (s *BotService) TestConnection(userID string, req *TestConnectionRequest) (*TestConnectionResponse, error) {
	baseURL, modelName, apiKey, err := s.resolveTestTarget(userID, req)
	if err != nil {
		return nil, err
	}

	checks := make([]ConnectionCheckResult, 0, 2)

	streamOK, streamDetail := botProbeStreaming(baseURL, modelName, apiKey)
	checks = append(checks, ConnectionCheckResult{Name: "streaming", Passed: streamOK, Detail: streamDetail})

	toolOK, toolDetail := botProbeToolCall(baseURL, modelName, apiKey)
	checks = append(checks, ConnectionCheckResult{Name: "tool_call", Passed: toolOK, Detail: toolDetail})

	// 聚合成功要求两项都过：能聊天不能调用工具的供应商跑不了工具循环，
	// 编辑器据 ok 判定测试结果，不能给假成功。
	ok := streamOK && toolOK

	// 窗口探测同步执行并把结果并入返回；探测与持久化只发生一次——响应里的
	// 结果与缓存里的结果同源（refreshModelWindow 的探测+持久化拆成两步后，
	// 这里复用同一步持久化，不做第二次 /models 请求）。持久化按 INT-115 F6
	// 落列表内全部模型（同一事务批量），配置的辅助模型同样能拿到输出上限。
	// 探测失败回退到当前解析值并落失败标记，不冒泡为测试失败（可达性问题
	// 已由上面两项检查给出）。
	capability := s.capabilityFor(baseURL, modelName)
	ctx, cancel := context.WithTimeout(context.Background(), botWindowDetectTimeout)
	det, detErr := s.detectModelWindows(ctx, baseURL, apiKey, modelName)
	cancel()
	if detErr == nil {
		capability = *det.Target
		if err := s.persistModelWindows(baseURL, modelName, det.Entries); err != nil {
			s.logger.Info("模型窗口缓存写入失败",
				zap.String("model", modelName), zap.Error(err))
		}
	} else {
		s.recordModelWindowProbeFailure(baseURL, modelName, detErr)
	}

	return &TestConnectionResponse{
		Ok:              ok,
		Checks:          checks,
		ContextWindow:   capability.WindowTokens,
		MaxOutputTokens: capability.MaxOutputTokens,
		ContextSource:   capability.Source,
		ContextDetail:   capability.Detail,
	}, nil
}

// resolveTestTarget 解析测试目标：未保存配置 / 已保存 key + 表单覆盖。
func (s *BotService) resolveTestTarget(userID string, req *TestConnectionRequest) (baseURL, modelName, apiKey string, err error) {
	if req.BotID == nil || *req.BotID == "" {
		if req.BaseURL == nil || req.Model == nil || req.APIKey == nil {
			return "", "", "", errs.New(errs.ErrInvalidParams.Code, "未保存配置时必须提供 base_url、model 与 api_key")
		}
		return strings.TrimSpace(*req.BaseURL), strings.TrimSpace(*req.Model), strings.TrimSpace(*req.APIKey), nil
	}

	bot, err := s.bots.FindByIDAndUser(*req.BotID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", errs.ErrBotNotFound
		}
		return "", "", "", errs.ErrInternal
	}
	plain, err := s.decryptAPIKey(bot)
	if err != nil {
		return "", "", "", err
	}
	// 表单里填了替换密钥就测新密钥：旧密钥有效不代表替换后能用，测试
	// 连接必须反映将要保存的配置。
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		plain = strings.TrimSpace(*req.APIKey)
	}
	if req.BaseURL != nil && strings.TrimSpace(*req.BaseURL) != "" {
		baseURL = strings.TrimSpace(*req.BaseURL)
	} else {
		baseURL = bot.BaseURL
	}
	if req.Model != nil && strings.TrimSpace(*req.Model) != "" {
		modelName = strings.TrimSpace(*req.Model)
	} else {
		modelName = bot.Model
	}
	return baseURL, modelName, plain, nil
}

// ---------- 密钥 ----------

func (s *BotService) encryptAPIKey(plain string) ([]byte, string, error) {
	cipher, err := crypto.Encrypt([]byte(plain), []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, "", errs.ErrInternal
	}
	return cipher, botKeyLast4(plain), nil
}

func (s *BotService) decryptAPIKey(bot *model.Bot) (string, error) {
	plain, err := crypto.Decrypt(bot.APIKeyEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return "", errs.ErrInternal
	}
	return string(plain), nil
}

// botKeyLast4 只保留末四位用于展示；任何日志、错误、快照禁止出现明文。
func botKeyLast4(plain string) string {
	if len(plain) <= 4 {
		return plain
	}
	return plain[len(plain)-4:]
}

// botValidBaseURL 允许 http/https（自托管、内网地址都允许），不追加 /v1。
func botValidBaseURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// ---------- 发消息 ----------

// SendMessageRequest 是主线发消息入参；client_message_id 必填（幂等键）。
type SendMessageRequest struct {
	// Text 用户正文。
	Text string `json:"text"`
	// ClientMessageID 必填；同键同正文返回已有消息，同键不同正文返回冲突。
	ClientMessageID string `json:"client_message_id"`
}

// SendMessageResult 返回已落库消息与当前/待启动 run 信息。
type SendMessageResult struct {
	Message BotMessageResponse `json:"message"`
	// Run 是当前活动 run（可能尚未包含本消息）；没有活动 run 时为 null，
	// 本次发送会触发新 run。
	Run *BotRunResponse `json:"run"`
	// Queued 恒为 true：消息已落库，执行交给 runtime。
	Queued bool `json:"queued"`
}

// SendMessage：校验归属 → 短事务幂等检查、取号、追加 user 消息与事件 →
// 唤醒 runtime（事务外）。模型请求与业务调用都在事务外。
func (s *BotService) SendMessage(userID, botID string, req *SendMessageRequest) (*SendMessageResult, error) {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotNotFound
		}
		return nil, errs.ErrInternal
	}
	text := strings.TrimSpace(req.Text)
	clientMsgID := strings.TrimSpace(req.ClientMessageID)
	if text == "" || clientMsgID == "" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "text 与 client_message_id 必填")
	}

	var msg *model.BotMessage
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		if existing, findErr := txMsgs.FindByClientMessageID(botID, clientMsgID); findErr == nil {
			var content botUserContent
			if json.Unmarshal([]byte(existing.ContentJSON), &content) == nil && content.Text == text {
				msg = existing
				return nil
			}
			return errs.ErrBotClientMessageConflict
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}

		seq, seqErr := s.bots.NextMessageSeqTx(tx, botID)
		if seqErr != nil {
			return seqErr
		}
		content := botUserContent{Text: text}
		raw, marshalErr := json.Marshal(content)
		if marshalErr != nil {
			return marshalErr
		}
		msg = &model.BotMessage{
			ID:              uuid.NewString(),
			BotID:           botID,
			Seq:             seq,
			Kind:            model.BotMessageKindUser,
			ContentJSON:     string(raw),
			SessionKind:     model.BotSessionKindMain,
			ClientMessageID: &clientMsgID,
			CreatedAt:       time.Now().UTC(),
		}
		if err := txMsgs.AppendTx(tx, msg); err != nil {
			return err
		}
		// user_message 携带完整消息投影：其他标签页据此直接入时间线，
		// 不依赖自己发消息时的 REST 响应。
		return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "user_message",
			MessageID: msg.ID,
			Payload:   botMessageResponseOf(msg),
		})
	})
	if txErr != nil {
		if errors.Is(txErr, errs.ErrBotClientMessageConflict) {
			return nil, txErr
		}
		return nil, errs.ErrInternal
	}

	// 事务已提交：发布 SSE 事件并唤醒 runtime；没有活动 run 时由它创建/认领
	// 唯一 run。trigger_key 用消息 seq，重复投递的触发不会产生第二个 run。
	s.publishEvent(botID)
	s.wakeRuntime(botID, botWake{
		Trigger: model.BotTriggerUserMessage,
		Key:     fmt.Sprintf("user_message:%d", msg.Seq),
		Context: fmt.Sprintf(`{"message_seq":%d,"client_message_id":%q}`, msg.Seq, clientMsgID),
	})

	runResp := s.currentRunResponse(botID)
	return &SendMessageResult{
		Message: botMessageResponseOf(msg),
		Run:     runResp,
		Queued:  true,
	}, nil
}

// currentRunResponse 读当前活动 run 概要；没有则为 nil。
func (s *BotService) currentRunResponse(botID string) *BotRunResponse {
	run, err := s.runs.FindActiveByBot(botID)
	if err != nil {
		return nil
	}
	resp := botRunResponseOf(run)
	return &resp
}

// ListMessages 主线分页：before/after 是排他 seq 边界，页内正序；两边界都
// 缺省时返回最新的 limit 条（见 repository.ListMainline）。
func (s *BotService) ListMessages(userID, botID string, before, after int64, limit int) ([]BotMessageResponse, int64, error) {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrBotNotFound
		}
		return nil, 0, errs.ErrInternal
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.messages.ListMainline(botID, before, after, limit)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}
	out := make([]BotMessageResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botMessageResponseOf(&rows[i]))
	}
	return out, int64(len(out)), nil
}
