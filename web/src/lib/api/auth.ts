import { api, ROTATION_OWNER_HEADER } from "./client";

interface LoginRequest {
  login: string;
  password: string;
}

interface RegisterRequest {
  username: string;
  email: string;
  password: string;
}

interface AuthResponse {
  token: string;
  refresh_token: string;
  user: User;
}

interface SessionTokens {
  token: string;
  refresh_token: string;
}

export const authApi = {
  login: (data: LoginRequest) =>
    api.post("auth/login", { json: data }).json<ApiResponse<AuthResponse>>(),

  register: (data: RegisterRequest) =>
    api
      .post("auth/register", { json: data })
      .json<ApiResponse<AuthResponse>>(),

  logout: (refreshToken?: string) =>
    api
      .post("auth/logout", { json: { refresh_token: refreshToken ?? "" } })
      .json<ApiResponse<null>>(),

  me: () => api.get("auth/me").json<ApiResponse<User>>(),

  updateMe: (data: { username?: string; email?: string }) =>
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
