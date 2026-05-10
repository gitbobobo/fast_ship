import { create } from "zustand";
import { persist } from "zustand/middleware";

interface AuthState {
  token: string | null;
  refreshToken: string | null;
  user: User | null;
  setAuth: (token: string, refreshToken: string, user: User) => void;
  setToken: (token: string) => void;
  setRefreshToken: (refreshToken: string | null) => void;
  setUser: (user: User) => void;
  logout: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      refreshToken: null,
      user: null,
      setAuth: (token, refreshToken, user) =>
        set({ token, refreshToken, user }),
      setToken: (token) => set({ token }),
      setRefreshToken: (refreshToken) => set({ refreshToken }),
      setUser: (user) => set({ user }),
      logout: () => set({ token: null, refreshToken: null, user: null }),
    }),
    {
      name: "fast-ship-auth",
      partialize: (state) => ({
        token: state.token,
        refreshToken: state.refreshToken,
      }),
    },
  ),
);
