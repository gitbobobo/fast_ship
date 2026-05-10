import ky from "ky";
import { useAuthStore } from "@/lib/store/auth-store";

// Separate ky instance for refresh requests (no afterResponse hook to avoid loops)
const refreshClient = ky.create({ prefixUrl: "/api" });
const REFRESH_RETRY_HEADER = "x-fast-ship-refresh-retried";

let refreshPromise: Promise<string> | null = null;

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

        try {
          const newToken = await tryRefreshToken();
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
