import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import ProfilePage from "@/routes/settings/profile";
import PasswordPage from "@/routes/settings/password";
import { renderWithRoute } from "@/test/render";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    updateMe: vi.fn(),
    updatePassword: vi.fn(),
  },
}));

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

describe("Settings Profile Page", () => {
  const setUser = vi.fn();

  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: {
        id: "user-1",
        username: "old-name",
        email: "old@example.com",
        created_at: "2026-04-06T10:00:00Z",
        updated_at: "2026-04-06T10:00:00Z",
      },
      setUser,
    } as unknown as ReturnType<typeof useAuthStore>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("updates profile and syncs the auth store", async () => {
    vi.mocked(authApi.updateMe).mockResolvedValue({
      data: {
        id: "user-1",
        username: "new-name",
        email: "new@example.com",
        created_at: "2026-04-06T10:00:00Z",
        updated_at: "2026-04-06T11:00:00Z",
      },
    } as Awaited<ReturnType<typeof authApi.updateMe>>);

    const user = userEvent.setup();

    renderWithRoute(<ProfilePage />, {
      path: "/settings/profile",
      initialEntry: "/settings/profile",
    });

    const usernameInput = screen.getByLabelText("用户名");
    const emailInput = screen.getByLabelText("邮箱");

    await user.clear(usernameInput);
    await user.type(usernameInput, "new-name");
    await user.clear(emailInput);
    await user.type(emailInput, "new@example.com");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(authApi.updateMe).toHaveBeenCalledWith({
        username: "new-name",
        email: "new@example.com",
      }),
    );
    expect(setUser).toHaveBeenCalledWith({
      id: "user-1",
      username: "new-name",
      email: "new@example.com",
      created_at: "2026-04-06T10:00:00Z",
      updated_at: "2026-04-06T11:00:00Z",
    });
  });
});

describe("Settings Password Page", () => {
  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: {
        id: "user-1",
        username: "test-user",
        email: "test@example.com",
        created_at: "2026-04-06T10:00:00Z",
        updated_at: "2026-04-06T10:00:00Z",
      },
    } as unknown as ReturnType<typeof useAuthStore>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("updates password and resets password fields on success", async () => {
    vi.mocked(authApi.updatePassword).mockResolvedValue({
      data: null,
    } as Awaited<ReturnType<typeof authApi.updatePassword>>);

    const user = userEvent.setup();

    renderWithRoute(<PasswordPage />, {
      path: "/settings/password",
      initialEntry: "/settings/password",
    });

    const oldPasswordInput = screen.getByLabelText("当前密码");
    const newPasswordInput = screen.getByLabelText("新密码");
    const confirmPasswordInput = screen.getByLabelText("确认新密码");

    await user.type(oldPasswordInput, "OldPass123");
    await user.type(newPasswordInput, "NewPass123");
    await user.type(confirmPasswordInput, "NewPass123");
    await user.click(screen.getByRole("button", { name: "修改密码" }));

    await waitFor(() =>
      expect(authApi.updatePassword).toHaveBeenCalledWith({
        old_password: "OldPass123",
        new_password: "NewPass123",
      }),
    );
    await waitFor(() => {
      expect(oldPasswordInput).toHaveValue("");
      expect(newPasswordInput).toHaveValue("");
      expect(confirmPasswordInput).toHaveValue("");
    });
  });
});
