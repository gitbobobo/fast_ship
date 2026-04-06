import { api } from "./client";

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
  user: User;
}

export const authApi = {
  login: (data: LoginRequest) =>
    api.post("auth/login", { json: data }).json<ApiResponse<AuthResponse>>(),

  register: (data: RegisterRequest) =>
    api.post("auth/register", { json: data }).json<ApiResponse<AuthResponse>>(),

  logout: () => api.post("auth/logout").json<ApiResponse<null>>(),

  me: () => api.get("auth/me").json<ApiResponse<User>>(),

  updateMe: (data: { username?: string; email?: string }) =>
    api.put("auth/me", { json: data }).json<ApiResponse<User>>(),

  updatePassword: (data: { old_password: string; new_password: string }) =>
    api.put("auth/password", { json: data }).json<ApiResponse<null>>(),
};
