package service

import (
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// T3 环境会话能力（INT-85）。
//
// access 唯一看精确 token 的 orchestration:operate scope：T3 服务端的全部写
// RPC（dispatchCommand/launchThread/attachments/secrets 等）在
// RpcAuthorization 中都要求该 scope。旧只读环境（INT-82 时代配对）不自动
// 升级——写入口统一 40300，引导重新配对。

// errT3ReadOnlyEnvironment 环境会话缺少 orchestration:operate。
var errT3ReadOnlyEnvironment = errs.New(errs.ErrPermissionDenied.Code, "该环境为只读会话，重新配对后可操作")

// newT3EnvironmentResponse 由落库记录构造 API 响应；scopes/access 来自
// BearerScope 列（配对时服务端授予，原样落库）。
func newT3EnvironmentResponse(env *model.T3Environment, status T3EnvironmentStatus, probeErr string) *T3EnvironmentResponse {
	granted := t3GrantedScopes(env.BearerScope)
	if granted == nil {
		granted = []string{}
	}
	return &T3EnvironmentResponse{
		Id:        env.ID,
		Name:      env.Name,
		Address:   env.Address,
		Status:    status,
		Scopes:    granted,
		Access:    t3AccessOf(env.BearerScope),
		Error:     api.NonEmpty(probeErr),
		UpdatedAt: formatTime(env.UpdatedAt.UTC()),
	}
}

// t3AccessOf 由精确 token 的 scope 列判定会话能力。
func t3AccessOf(scope string) T3EnvironmentAccess {
	if t3HasOperateScope(scope) {
		return T3EnvironmentAccessOperate
	}
	return T3EnvironmentAccessReadOnly
}

// requireOperate 写入口的统一守卫：会话缺 orchestration:operate 时返回 40300。
// 在解析 body、访问环境或发上游之前调用（router 已先用 RequireJWT 挡下
// API Key 的 40301）。
func requireT3Operate(env *model.T3Environment) error {
	if !t3HasOperateScope(env.BearerScope) {
		return errT3ReadOnlyEnvironment
	}
	return nil
}
