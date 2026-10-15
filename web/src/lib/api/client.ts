import ky, { HTTPError } from "ky";
import { useAuthStore } from "@/lib/store/auth-store";

// Separate ky instance for refresh requests (no afterResponse hook to avoid loops)
const refreshClient = ky.create({ prefixUrl: "/api" });
const REFRESH_RETRY_HEADER = "x-fast-ship-refresh-retried";

let refreshPromise: Promise<string> | null = null;

let credentialRotation: Promise<unknown> | null = null;

// 轮换发起请求自身携带此标记，401 时不得等待轮换窗口（等待对象正是自己）。
export const ROTATION_OWNER_HEADER = "x-fast-ship-rotation-owner";

// 凭据轮换（改密续签）窗口内，服务端已使旧凭证失效而新凭证尚未写回 store；
// 此时并发请求的旧 token 收到 401 不能走 refresh→logout，要先等轮换落地。
export function withCredentialRotation<T>(fn: () => Promise<T>): Promise<T> {
  const p = Promise.resolve().then(fn);
  credentialRotation = p;
  const done = () => {
    if (credentialRotation === p) credentialRotation = null;
  };
  p.then(done, done);
  return p;
}

// 401 恢复出口，ky 钩子与 XHR 上传共用：先等轮换窗口落地；store 凭证若已
// 轮换（与本次请求所携不同）直接拿来重试，否则走正常 refresh。
export async function resolveSessionToken(
  usedToken: string | null,
  opts?: { skipRotationWait?: boolean },
): Promise<string> {
  if (!opts?.skipRotationWait && credentialRotation) {
    try {
      await credentialRotation;
    } catch {
      // 轮换失败按普通 401 继续走刷新流程
    }
  }

  const current = useAuthStore.getState().token;
  if (current && current !== usedToken) {
    return current;
  }
  return tryRefreshToken();
}

export async function tryRefreshToken(): Promise<string> {
  if (refreshPromise) return refreshPromise;

  const { refreshToken } = useAuthStore.getState();
  if (!refreshToken) {
    throw new Error("No refresh token");
  }

  refreshPromise = refreshClient
    .post("auth/refresh", { json: { refresh_token: refreshToken } })
    .json<ApiResponse<{ token: string; refresh_token: string }>>()
    .then((res) => {
      const { token, refresh_token: nextRefreshToken } = res.data;
      const authStore = useAuthStore.getState();
      authStore.setToken(token);
      authStore.setRefreshToken(nextRefreshToken);
      return token;
    })
    .finally(() => {
      refreshPromise = null;
    });

  return refreshPromise;
}

// ApiRequestError 把失败的 HTTP 响应一次解码成可直接读的错误：状态码 +
// 服务端 {code,message}。HTTPError 的 body 只能消费一次，在组件 effect 里
// 读会被 React StrictMode 的双执行丢掉——所以解码固定在请求边界完成。
export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: number | null;

  constructor(status: number, code: number | null, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function decodeApiError<T>(promise: Promise<T>): Promise<T> {
  try {
    return await promise;
  } catch (err) {
    if (err instanceof HTTPError) {
      const body = await err.response
        .json<ApiResponse<unknown>>()
        .catch(() => null);
      if (body && typeof body.message === "string" && body.message) {
        throw new ApiRequestError(err.response.status, body.code ?? null, body.message);
      }
    }
    throw err;
  }
}

export const api = ky.create({
  prefixUrl: "/api",
  hooks: {
    beforeRequest: [
      (request) => {
        const token = useAuthStore.getState().token;
        if (token) {
          request.headers.set("Authorization", `Bearer ${token}`);
        }
      },
    ],
    afterResponse: [
      async (request, options, response) => {
        if (
          response.status !== 401 ||
          request.headers.get(REFRESH_RETRY_HEADER) === "1"
        ) {
          return;
        }

        const usedToken =
          request.headers.get("Authorization")?.replace(/^Bearer /, "") ?? null;

        try {
          const newToken = await resolveSessionToken(usedToken, {
            skipRotationWait:
              request.headers.get(ROTATION_OWNER_HEADER) === "1",
          });
          request.headers.set("Authorization", `Bearer ${newToken}`);
          request.headers.set(REFRESH_RETRY_HEADER, "1");
          return ky(request, options);
        } catch {
          useAuthStore.getState().logout();
          window.location.href = "/login";
        }
      },
    ],
  },
});
