package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/quota"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 设备授权会话编排（INT-117）：为 DeviceAuth 非空的供应商代理上游的浏览器
// 设备授权流程。上游协议（路径、字段名、状态词表）在 pkg/quota 的各适配器里，
// 这里只管会话：发起、按节奏代查、批准后建号或换凭据、取消与清理。
//
// 会话只存内存：上游设备密钥属于敏感凭据，不落库；进程重启即丢，之后 GET 返回
// 40431，前端提示重新发起。日志只记 session id / provider / status / account id，
// 绝不记录设备密钥与凭据。

const (
	// deviceAuthExpiryFallback 是上游有效期不可解析时的本地兜底。
	deviceAuthExpiryFallback = 10 * time.Minute

	// deviceAuthMaxTTL 是会话有效期的硬上限（取 min(上游值, now+30min)）：
	// 上游给出极远时间会让含设备密钥的会话长期驻留内存。
	deviceAuthMaxTTL = 30 * time.Minute

	// deviceAuthGrace 是过期会话在内存里的保留时长，超过即被顺手清理；
	// 保留期内 GET 仍能拿到 expired 终态，前端不至于对着 40431 困惑。
	deviceAuthGrace = time.Hour
)

// quotaDeviceSession 是一次设备授权会话。设备密钥与凭据组只存在这里。
type quotaDeviceSession struct {
	id            string
	userID        string
	provider      string
	label         string
	deviceID      string
	deviceKey     string // 上游设备密钥，只在内存
	userCode      string
	activationURL string
	expiresAt     time.Time
	pollInterval  time.Duration
	status        api.QuotaDeviceAuthorizationStatus
	authError     string

	// accountID 是新建路径的预分配账号 id：建号失败保持 pending 后，重试仍用
	// 同一个 id 幂等建号（createWithID 命中同 id 行视为成功），不产生重复账号。
	accountID string
	// rebindID 非空表示批准后为该已有账号整组换凭据（请求带了 account_id），
	// 不再新建。
	rebindID string
	// creds 是首次从上游拿到的凭据组。批准后的 token 端点大概率一次性，
	// 缓存后重试只重跑建号，不再向上游换取。
	creds quota.Credentials

	// mu 串行化同一会话的 GET，避免两次并发 GET 各建一个账号；
	// lastPollAt 控制向上游代查的节奏。
	mu         sync.Mutex
	lastPollAt time.Time
	// canceled 由 Cancel 先置位再摘除 map：GET 可能已持有会话指针并阻塞在
	// 上游请求上，取消必须能打断进行中的轮询（拿到凭据后、建号前检查）。
	canceled atomic.Bool
}

// finalAccountID 是 approved 视图里的 account_id：rebind 是被更新的账号，
// 新建是预分配的 id；非 approved 一律空串。
func (s *quotaDeviceSession) finalAccountID() string {
	if s.status != api.QuotaDeviceAuthorizationStatusApproved {
		return ""
	}
	if s.rebindID != "" {
		return s.rebindID
	}
	return s.accountID
}

// view 投影为契约响应；设备密钥与凭据永不出现。
func (s *quotaDeviceSession) view() api.QuotaDeviceAuthorization {
	return api.QuotaDeviceAuthorization{
		Id:                  s.id,
		Provider:            s.provider,
		Status:              s.status,
		UserCode:            s.userCode,
		ActivationUrl:       s.activationURL,
		ExpiresAt:           api.JSONTime(s.expiresAt),
		PollIntervalSeconds: int(s.pollInterval / time.Second),
		AccountId:           s.finalAccountID(),
		Error:               s.authError,
	}
}

// StartDeviceAuthorization 发起设备授权：校验供应商能力与标签（可选带上要换
// 凭据的目标账号），向上游建授权，成功后在内存建会话并返回 pending 视图。
func (s *QuotaService) StartDeviceAuthorization(userID string, req *StartQuotaDeviceAuthorizationRequest) (*api.QuotaDeviceAuthorization, error) {
	provider, appErr := s.requireProvider(req.Provider)
	if appErr != nil {
		return nil, appErr
	}
	if provider.DeviceAuth == nil {
		return nil, errs.ErrQuotaDeviceAuthUnsupported
	}
	label, appErr := validQuotaLabel(req.Label)
	if appErr != nil {
		return nil, appErr
	}
	var rebindID string
	if req.AccountId != nil && *req.AccountId != "" {
		// 为已有账号换凭据：目标必须存在且属于当前用户，否则 40430（不泄露
		// 他人账号的存在性）；供应商还必须与会话一致，否则 40001——放行的话
		// 用户会走完整个浏览器授权，批准后才在 Update 处失败，只看到误导性的
		// 失败提示。这条校验在调上游之前，无任何副作用。
		account, err := s.repo.FindByID(*req.AccountId, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrQuotaAccountNotFound
			}
			return nil, errs.ErrInternal
		}
		if account.Provider != provider.ID {
			return nil, errs.New(errs.ErrInvalidParams.Code, "该账号属于其他供应商，不能换成本供应商的凭据")
		}
		rebindID = *req.AccountId
	}

	s.cleanupDeviceSessions()

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Quota.RequestTimeout())
	defer cancel()
	start, err := provider.DeviceAuth.Start(ctx, s.client, provider.DefaultBaseURL)
	if err != nil {
		// 日志只记 provider：失败原因可能是网络也可能是上游 5xx，均不含敏感值，
		// 但保持「只记 id/provider/status」的最小形态最稳妥。
		s.logger.Warn("quota device auth start failed", zap.String("provider", provider.ID))
		return nil, errs.ErrQuotaDeviceAuthUpstream
	}
	if start.DeviceID == "" || start.DeviceSecret == "" {
		return nil, errs.ErrQuotaDeviceAuthUpstream
	}

	now := s.now().UTC()
	expiresAt := now.Add(deviceAuthExpiryFallback)
	if !start.ExpiresAt.IsZero() {
		expiresAt = start.ExpiresAt.UTC()
	}
	// 有效期封顶：避免上游给出极远时间让含设备密钥的会话长期驻留内存。
	if capped := now.Add(deviceAuthMaxTTL); expiresAt.After(capped) {
		expiresAt = capped
	}

	session := &quotaDeviceSession{
		id:            uuid.NewString(),
		userID:        userID,
		provider:      provider.ID,
		label:         label,
		deviceID:      start.DeviceID,
		deviceKey:     start.DeviceSecret,
		userCode:      start.UserCode,
		activationURL: start.ActivationURL,
		expiresAt:     expiresAt,
		pollInterval:  start.PollInterval,
		status:        api.QuotaDeviceAuthorizationStatusPending,
		accountID:     uuid.NewString(),
		rebindID:      rebindID,
		lastPollAt:    now,
	}
	s.deviceMu.Lock()
	s.deviceSessions[session.id] = session
	s.deviceMu.Unlock()

	view := session.view()
	return &view, nil
}

// GetDeviceAuthorization 查询会话状态；仍 pending 且到了轮询节奏时先向上游
// 代查一次。过期判定顺序：到了节奏先问上游（含已过本地有效期的情形），上游
// 给出终态就按终态；上游仍 pending（或无结论）且已过 expiresAt 才判本地过期——
// 用户在批准页待久一点再切回来，批准结果不应被作废。终态之后不再轮询。
func (s *QuotaService) GetDeviceAuthorization(userID, id string) (*api.QuotaDeviceAuthorization, error) {
	session := s.findDeviceSession(userID, id)
	if session == nil {
		return nil, errs.ErrQuotaDeviceAuthNotFound
	}
	s.cleanupDeviceSessions()

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.status != api.QuotaDeviceAuthorizationStatusPending {
		view := session.view()
		return &view, nil
	}

	now := s.now().UTC()
	if now.Before(session.lastPollAt.Add(session.pollInterval)) {
		view := session.view()
		return &view, nil
	}
	session.lastPollAt = now

	if err := s.pollDeviceAuthorization(session); err != nil &&
		!errors.Is(err, errDeviceAuthPending) && !errors.Is(err, errDeviceAuthAccountRetry) {
		// 传输错误 / 上游 5xx：保持 pending，下次 GET 再试。上游 2xx 但用户
		// 还没批准（errDeviceAuthPending）与「凭据已到手但建号待重试」
		// （errDeviceAuthAccountRetry）都已在别处处理或属正常等待，不额外记日志。
		s.logger.Warn("quota device auth poll failed",
			zap.String("session_id", session.id), zap.String("provider", session.provider))
	}

	// 上游仍无终态（或无结论）且已过本地有效期 → expired。
	if session.status == api.QuotaDeviceAuthorizationStatusPending && now.After(session.expiresAt) {
		session.status = api.QuotaDeviceAuthorizationStatusExpired
		session.authError = "验证码已过期，请重新发起授权。"
		s.logDeviceStatus(session)
	}
	view := session.view()
	return &view, nil
}

// CancelDeviceAuthorization 取消会话（用户取消 / 关闭对话框时调用）。先置
// 取消标志再摘除 map 并立即返回：并发进行中的 GET 阻塞在上游请求上时也能
// 在建号前看到取消，不落库。
func (s *QuotaService) CancelDeviceAuthorization(userID, id string) error {
	s.deviceMu.Lock()
	session, ok := s.deviceSessions[id]
	if ok && session.userID == userID {
		session.canceled.Store(true)
		delete(s.deviceSessions, id)
	} else {
		ok = false
	}
	s.deviceMu.Unlock()
	if !ok {
		return errs.ErrQuotaDeviceAuthNotFound
	}
	return nil
}

// errDeviceAuthPending 表示上游 2xx、没有凭据、状态也非终态——用户还没在
// 浏览器批准的正常等待。
var errDeviceAuthPending = errors.New("device authorization still pending")

// errDeviceAuthAccountRetry 表示凭据已到手但建号/换凭据失败，保持 pending 待
// 下次 GET 重试（失败原因已在 pollDeviceAuthorization 里记 Warn）。
var errDeviceAuthAccountRetry = errors.New("device authorization account create deferred")

// pollDeviceAuthorization 向上游代查一次并按结论推进会话状态。返回
// errDeviceAuthPending 表示正常等待；返回 errDeviceAuthAccountRetry 表示建号
// 待重试；返回其他 error 表示传输错误 / 5xx（由调用方记 Warn）。三类都保持 pending。
func (s *QuotaService) pollDeviceAuthorization(session *quotaDeviceSession) error {
	provider, ok := s.providerByID(session.provider)
	if !ok || provider.DeviceAuth == nil {
		return errors.New("provider missing device auth")
	}

	// 凭据首次拿到后只在内存缓存，之后重试不再向上游换取：批准后的 token
	// 端点大概率一次性，重复兑换可能让整个授权被静默吞掉。
	if session.creds == nil {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Quota.RequestTimeout())
		defer cancel()
		poll, err := provider.DeviceAuth.Poll(ctx, s.client, provider.DefaultBaseURL, session.deviceID, session.deviceKey)
		if err != nil {
			return err
		}
		switch poll.State {
		case quota.DeviceAuthPending:
			return errDeviceAuthPending
		case quota.DeviceAuthExpired, quota.DeviceAuthDenied, quota.DeviceAuthInvalid:
			// 两边的字符串常量同词表（pending/approved/expired/denied/invalid），
			// 直接映射；契约 enum 变更时这里要跟着对。
			session.status = api.QuotaDeviceAuthorizationStatus(poll.State)
			session.authError = poll.Message
			s.logDeviceStatus(session)
			return nil
		case quota.DeviceAuthApproved:
			if len(poll.Credentials) == 0 {
				return errors.New("device auth approved without credentials")
			}
			session.creds = poll.Credentials
		}
	}

	// 取消要能打断进行中的轮询：拿到凭据之后、动库之前检查取消标志，
	// 已取消则直接返回，不建号也不改状态。
	if session.canceled.Load() {
		return nil
	}

	if session.rebindID != "" {
		// 为已有账号整组换凭据（Update 自带同步重抓）。
		creds := api.QuotaCredentials(session.creds)
		if _, err := s.Update(session.userID, session.rebindID, &UpdateQuotaAccountRequest{Credentials: &creds}); err != nil {
			s.logger.Warn("quota device auth account update failed",
				zap.String("session_id", session.id),
				zap.String("account_id", session.rebindID),
				zap.String("provider", session.provider))
			return errDeviceAuthAccountRetry
		}
		session.accountID = session.rebindID
	} else {
		// 幂等建号：预分配的 id 已存在（前次重试落库成功但会话未推进）视为
		// 成功；建号失败保持 pending 并记 Warn（只带 id，不带密钥），下次重试。
		if appErr := s.createWithID(session.userID, session.accountID, &CreateQuotaAccountRequest{
			Provider:    session.provider,
			Label:       session.label,
			Credentials: api.QuotaCredentials(session.creds),
		}); appErr != nil {
			s.logger.Warn("quota device auth account create failed",
				zap.String("session_id", session.id),
				zap.String("account_id", session.accountID),
				zap.String("provider", session.provider))
			return errDeviceAuthAccountRetry
		}
		s.FetchAccountNow(context.Background(), session.accountID)
	}
	session.status = api.QuotaDeviceAuthorizationStatusApproved
	session.authError = ""
	s.logDeviceStatus(session)
	return nil
}

func (s *QuotaService) findDeviceSession(userID, id string) *quotaDeviceSession {
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	session, ok := s.deviceSessions[id]
	if !ok || session.userID != userID {
		return nil
	}
	return session
}

// cleanupDeviceSessions 清掉过期超过保留期的会话；POST/GET 时顺手调用。
func (s *QuotaService) cleanupDeviceSessions() {
	cutoff := s.now().UTC().Add(-deviceAuthGrace)
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	for id, session := range s.deviceSessions {
		if session.expiresAt.Before(cutoff) {
			delete(s.deviceSessions, id)
		}
	}
}

// logDeviceStatus 是设备授权唯一的日志形态：只含 session id / provider / status。
func (s *QuotaService) logDeviceStatus(session *quotaDeviceSession) {
	s.logger.Info("quota device auth status",
		zap.String("session_id", session.id),
		zap.String("provider", session.provider),
		zap.String("status", string(session.status)))
}
