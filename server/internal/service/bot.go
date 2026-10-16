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

	bots     *repository.BotRepository
	messages *repository.BotMessageRepository
	events   *repository.BotEventRepository
	runs     *repository.BotRunRepository
	requests *repository.BotModelRequestRepository
	calls    *repository.BotToolCallRepository
	authz    *repository.BotAuthorizationRepository

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
		db:        db,
		cfg:       cfg,
		logger:    logger,
		bots:      repository.NewBotRepository(db),
		messages:  repository.NewBotMessageRepository(db),
		events:    repository.NewBotEventRepository(db),
		runs:      repository.NewBotRunRepository(db),
		requests:  repository.NewBotModelRequestRepository(db),
		calls:     repository.NewBotToolCallRepository(db),
		authz:     repository.NewBotAuthorizationRepository(db),
		projects:  projects,
		issues:    issues,
		threads:   threads,
		bindings:  bindings,
		rt:        newBotRuntimeState(),
		streamHub: newBotStreamHub(),
	}
}

// ---------- 响应类型 ----------

// BotResponse 管理面响应；key 全链路脱敏，只回末四位。NextEventSeq 是
// 事件账本水位：详情页订阅以它-1 起读，与消息快照衔接（不从 0 重放）。
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
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

func botResponseOf(b *model.Bot) BotResponse {
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
	out := make([]BotResponse, 0, len(bots))
	for i := range bots {
		out = append(out, botResponseOf(&bots[i]))
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
	resp := botResponseOf(bot)
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
	resp := botResponseOf(bot)
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
func (s *BotService) Update(userID, botID string, req *UpdateBotRequest) (*BotResponse, error) {
	bot, err := s.bots.FindByIDAndUser(botID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotNotFound
		}
		return nil, errs.ErrInternal
	}

	personaChanged := false
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "name 不能为空")
		}
		bot.Name = name
	}
	if req.PersonaPrompt != nil && *req.PersonaPrompt != bot.PersonaPrompt {
		bot.PersonaPrompt = *req.PersonaPrompt
		bot.SystemPromptText = buildBotSystemPrompt(bot.PersonaPrompt)
		bot.PromptRevision++
		personaChanged = true
	}
	if req.BaseURL != nil {
		baseURL := strings.TrimSpace(*req.BaseURL)
		if baseURL == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "base_url 不能为空")
		}
		if !botValidBaseURL(baseURL) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "base_url 必须是 http(s) 地址")
		}
		bot.BaseURL = baseURL
	}
	if req.Model != nil {
		modelName := strings.TrimSpace(*req.Model)
		if modelName == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "model 不能为空")
		}
		bot.Model = modelName
	}
	if req.APIKey != nil {
		apiKey := strings.TrimSpace(*req.APIKey)
		if apiKey == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "api_key 不能为空串；不提供该字段即保留原 key")
		}
		cipher, last4, err := s.encryptAPIKey(apiKey)
		if err != nil {
			return nil, err
		}
		bot.APIKeyEncrypted = cipher
		bot.APIKeyLast4 = last4
	}

	bot.UpdatedAt = time.Now().UTC()
	if err := s.bots.UpdateConfig(bot); err != nil {
		return nil, errs.ErrInternal
	}
	_ = personaChanged
	resp := botResponseOf(bot)
	return &resp, nil
}

// Delete 有活动 run 或未终结调用时返回冲突：先显式处理，避免静默丢任务。
// pending_confirmation / queued 的调用也算未终结（rejected/cancelled 等终态不算）。
func (s *BotService) Delete(userID, botID string) error {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrBotNotFound
		}
		return errs.ErrInternal
	}

	if _, err := s.runs.FindActiveByBot(botID); err == nil {
		return errs.ErrBotDeleteConflict
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.ErrInternal
	}
	active, err := s.calls.ListByBot(botID, []string{
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

	if err := s.bots.Delete(botID); err != nil {
		return errs.ErrInternal
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

	ok := streamOK
	return &TestConnectionResponse{Ok: ok, Checks: checks}, nil
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
