import type { components } from "@/types/api.gen";
import { api, decodeApiError, ROTATION_OWNER_HEADER } from "./client";

/** 契约类型直接引用 api.gen.ts。 */
type Schemas = components["schemas"];

type LoginRequest = Schemas["LoginRequest"];

type RegisterRequest = Schemas["RegisterRequest"];

type AuthResponse = Schemas["AuthResponse"];

/** 契约名为 RefreshResponse（改密响应复用它表达「新签发的 token 对」）。 */
type SessionTokens = Schemas["RefreshResponse"];

export const authApi = {
  login: (data: LoginRequest) =>
    api.post("auth/login", { json: data }).json<ApiResponse<AuthResponse>>(),

  // decodeApiError：注册失败文案必须来自服务端 message（40306 公开注册已关闭、
  // 40432 邀请失效、40900/40901 用户名/邮箱重复等），不能前端写死。
  register: (data: RegisterRequest) =>
    decodeApiError(
      api.post("auth/register", { json: data }).json<ApiResponse<AuthResponse>>(),
    ),

  // 公开端点（未登录可调）：校验邀请能否用于注册；无效/过期/撤销/已用统一 40432。
  // token 放请求体、不进 URL 路径，避免被请求日志记下明文。
  validateInvite: (token: string) =>
    decodeApiError(
      api
        .post("auth/invites/validate", { json: { token } })
        .json<ApiResponse<InviteValidation>>(),
    ),

  logout: (refreshToken?: string) =>
    api
      .post("auth/logout", { json: { refresh_token: refreshToken ?? "" } })
      .json<ApiResponse<null>>(),

  me: () => api.get("auth/me").json<ApiResponse<User>>(),

  // timezone 三态：省略=不变、空串=清空（回落部署配置）、非空=必须是可加载的 IANA 名称
  updateMe: (data: { username?: string; email?: string; timezone?: string }) =>
    api.put("auth/me", { json: data }).json<ApiResponse<User>>(),

  uploadAvatar: (formData: FormData) =>
    api.post("auth/avatar", { body: formData }).json<ApiResponse<User>>(),

  // 改密即会话轮换的发起方：标记打在端点层，401 钩子据此跳过轮换等待，
  // 避免请求等待自己（自死锁），调用方无需感知该约定。
  updatePassword: (data: { old_password: string; new_password: string }) =>
    api
      .put("auth/password", {
        json: data,
        headers: { [ROTATION_OWNER_HEADER]: "1" },
      })
      .json<ApiResponse<SessionTokens>>(),
};
