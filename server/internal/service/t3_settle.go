package service

import (
	"context"
	"encoding/json"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 线程结算（INT-148）：thread.settle 把线程收进 T3 的历史区（settled），
// thread.unsettle 撤销。恢复队列复用 ResumeQueue（t3_write.go），不在此重复。
//
// 能力检查是硬前提：threadSettlement 是 optionalKey 能力（contracts
// environment.ts），旧版 T3 服务端既没有这两个命令也不认识它们，派发过去只会
// 得到含混的上游错误——必须在派发前就明确拒绝并指出升级出路。

// t3SettlementUnsupportedMessage 是能力缺失时的固定提示。
const t3SettlementUnsupportedMessage = "该 T3 环境不支持线程结算（旧版本服务端），请升级 T3 后重试"

// t3ConfigSupportsThreadSettlement 判定 server.getConfig 响应是否声明
// environment.capabilities.threadSettlement。字段缺失（旧版服务端）或 false
// 都算不支持；响应解析失败同样按不支持处理（坏响应上赌一个写命令更糟）。
func t3ConfigSupportsThreadSettlement(configValue json.RawMessage) bool {
	var config struct {
		Environment struct {
			Capabilities struct {
				ThreadSettlement *bool `json:"threadSettlement"`
			} `json:"capabilities"`
		} `json:"environment"`
	}
	if err := json.Unmarshal(configValue, &config); err != nil {
		return false
	}
	return config.Environment.Capabilities.ThreadSettlement != nil &&
		*config.Environment.Capabilities.ThreadSettlement
}

// requireT3ThreadSettlement 在既有 WS 会话上取一次 server.getConfig，确认
// 环境支持线程结算；不支持时返回 50202 与固定提示。
func (s *T3ThreadsService) requireT3ThreadSettlement(ctx context.Context, session *t3RPCSession) error {
	configValue, err := session.call(ctx, t3RPCServerGetConfig, map[string]any{})
	if err != nil {
		return s.upstreamError(err)
	}
	if !t3ConfigSupportsThreadSettlement(configValue) {
		return errs.New(errs.ErrT3Upstream.Code, t3SettlementUnsupportedMessage)
	}
	return nil
}

// SettleThread 结算线程（orchestration.dispatchCommand 的 thread.settle）：
// 结算后线程不在 T3 的 settled=false 默认筛选里，fs_thread list 要带 settled
// 参数才看得到；unsettle 撤销。回执只表示命令受理，落款时间由 T3 决定
// （settleAt 缺省即当前时刻）。
func (s *T3ThreadsService) SettleThread(ctx context.Context, userID, envID, threadID string) (*T3DispatchResult, error) {
	return s.threadSettlementCommand(ctx, userID, envID, threadID, "thread.settle", nil)
}

// UnsettleThread 撤销结算（thread.unsettle）。契约只接受 reason=user——
// 其余语义（auto-settle 的撤销）是服务端内部命令，客户端不派发。
func (s *T3ThreadsService) UnsettleThread(ctx context.Context, userID, envID, threadID string) (*T3DispatchResult, error) {
	return s.threadSettlementCommand(ctx, userID, envID, threadID, "thread.unsettle", map[string]any{"reason": "user"})
}

// threadSettlementCommand 是结算类命令的共用骨架：operate 守卫 → 预算 →
// 探测拨号 → 能力确认 → 派发。commandId 每次新生成（与 StopThread 一致：
// 回执只表示受理，不做跨重试的幂等合并）。
func (s *T3ThreadsService) threadSettlementCommand(ctx context.Context, userID, envID, threadID, commandType string, extra map[string]any) (*T3DispatchResult, error) {
	if threadID == "" {
		return nil, errs.ErrInvalidParams
	}
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	session, err := s.dialAfterProbe(ctx, env)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	if err := s.requireT3ThreadSettlement(ctx, session); err != nil {
		return nil, err
	}

	command := map[string]any{
		"type":      commandType,
		"commandId": newT3CommandId(),
		"threadId":  threadID,
	}
	for key, value := range extra {
		command[key] = value
	}
	sequence, err := s.dispatchT3Command(ctx, session, threadID, command)
	if err != nil {
		return nil, err
	}
	return &T3DispatchResult{Sequence: sequence}, nil
}
