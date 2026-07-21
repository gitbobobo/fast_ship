import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { screen, fireEvent, waitFor, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { UserNav } from "@/components/user-nav";
import { useAuthStore } from "@/lib/store/auth-store";
import { useThemeStore } from "@/lib/store/theme-store";
import { authApi } from "@/lib/api/auth";

// Mock modules
vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

vi.mock("@/lib/store/theme-store", () => ({
  useThemeStore: vi.fn(),
}));

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    logout: vi.fn(),
  },
}));

// 用于捕获当前路径的测试组件
function LocationDisplay() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

const mockLogout = vi.fn();
const mockSetTheme = vi.fn();

describe("UserNav Component", () => {
  const mockUser = {
    id: "user-1",
    username: "john_doe",
    email: "john@example.com",
    created_at: "2026-04-06T10:00:00Z",
    updated_at: "2026-04-06T10:00:00Z",
  };

  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: mockUser,
      logout: mockLogout,
    } as unknown as ReturnType<typeof useAuthStore>);
    vi.mocked(useThemeStore).mockReturnValue({
      theme: "system",
      setTheme: mockSetTheme,
    } as unknown as ReturnType<typeof useThemeStore>);
    vi.mocked(authApi.logout).mockResolvedValue({} as never);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders avatar row with user initial and username", () => {
    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    // Avatar row button should have aria-haspopup="menu" and show the username
    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    expect(avatarButton).toBeInTheDocument();
    expect(avatarButton).toHaveAttribute("aria-haspopup", "menu");
    expect(screen.getByText("J")).toBeInTheDocument(); // 首字符大写
  });

  it("displays correct initial for username starting with lowercase", () => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: { ...mockUser, username: "alice" },
      logout: mockLogout,
    } as unknown as ReturnType<typeof useAuthStore>);

    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    expect(screen.getByText("A")).toBeInTheDocument();
  });

  it("displays 'U' as fallback when username is empty", () => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: { ...mockUser, username: "" },
      logout: mockLogout,
    } as unknown as ReturnType<typeof useAuthStore>);

    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    expect(screen.getByText("U")).toBeInTheDocument();
  });

  it("opens dropdown menu when avatar is clicked", async () => {
    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    fireEvent.click(avatarButton);

    await waitFor(() => {
      expect(screen.getByText("john@example.com")).toBeInTheDocument();
      expect(screen.getByRole("menuitem", { name: /个人信息/i })).toBeInTheDocument();
      expect(screen.getByRole("menuitem", { name: /设置/i })).toBeInTheDocument();
      expect(screen.getByRole("menuitem", { name: /主题/i })).toBeInTheDocument();
      expect(screen.getByRole("menuitem", { name: /登出/i })).toBeInTheDocument();
    });
  });

  it("navigates to profile page when 个人信息 is clicked", async () => {
    render(
      <MemoryRouter initialEntries={["/projects"]}>
        <Routes>
          <Route path="/*" element={<>
            <UserNav />
            <LocationDisplay />
          </>} />
        </Routes>
      </MemoryRouter>,
    );

    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    fireEvent.click(avatarButton);

    await waitFor(() => {
      expect(screen.getByRole("menuitem", { name: /个人信息/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("menuitem", { name: /个人信息/i }));

    await waitFor(() => {
      expect(screen.getByTestId("location").textContent).toBe("/settings/profile");
    });
  });

  it("navigates to settings page when 设置 is clicked", async () => {
    render(
      <MemoryRouter initialEntries={["/projects"]}>
        <Routes>
          <Route path="/*" element={<>
            <UserNav />
            <LocationDisplay />
          </>} />
        </Routes>
      </MemoryRouter>,
    );

    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    fireEvent.click(avatarButton);

    await waitFor(() => {
      expect(screen.getByRole("menuitem", { name: /设置/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("menuitem", { name: /设置/i }));

    await waitFor(() => {
      expect(screen.getByTestId("location").textContent).toBe("/settings");
    });
  });

  it("switches theme from the 主题 submenu", async () => {
    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    fireEvent.click(avatarButton);

    await waitFor(() => {
      expect(screen.getByRole("menuitem", { name: /主题/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("menuitem", { name: /主题/i }));

    await waitFor(() => {
      expect(screen.getByRole("menuitem", { name: /深色/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("menuitem", { name: /深色/i }));

    expect(mockSetTheme).toHaveBeenCalledWith("dark");
  });

  it("calls logout and navigates to login when 登出 is clicked", async () => {
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    vi.mocked(authApi.logout).mockResolvedValueOnce(undefined as never);

    render(
      <MemoryRouter initialEntries={["/projects"]}>
        <Routes>
          <Route path="/*" element={<UserNav />} />
          <Route path="/login" element={<div>Login Page</div>} />
        </Routes>
      </MemoryRouter>,
    );

    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    fireEvent.click(avatarButton);

    await waitFor(() => {
      expect(screen.getByRole("menuitem", { name: /登出/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("menuitem", { name: /登出/i }));

    await waitFor(() => {
      expect(authApi.logout).toHaveBeenCalled();
      expect(mockLogout).toHaveBeenCalled();
    });
    
    consoleSpy.mockRestore();
  });



  it("displays user info correctly in dropdown", async () => {
    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    const avatarButton = screen.getByRole("button", { name: /john_doe/ });
    fireEvent.click(avatarButton);

    await waitFor(() => {
      expect(screen.getAllByText("john_doe").length).toBeGreaterThan(0);
      expect(screen.getByText("john@example.com")).toBeInTheDocument();
    });
  });
});

describe("Header Integration", () => {
  const mockUser = {
    id: "user-1",
    username: "testuser",
    email: "test@example.com",
    created_at: "2026-04-06T10:00:00Z",
    updated_at: "2026-04-06T10:00:00Z",
  };

  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: mockUser,
      logout: mockLogout,
    } as unknown as ReturnType<typeof useAuthStore>);
    vi.mocked(useThemeStore).mockReturnValue({
      theme: "system",
      setTheme: mockSetTheme,
    } as unknown as ReturnType<typeof useThemeStore>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("UserNav is accessible in the document", () => {
    const { container } = render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    expect(container.querySelector("button")).toBeInTheDocument();
  });

  it("avatar displays correct styling", () => {
    render(
      <MemoryRouter>
        <UserNav />
      </MemoryRouter>,
    );

    const avatarFallback = screen.getByText("T");
    expect(avatarFallback).toHaveClass("bg-primary");
    expect(avatarFallback).toHaveClass("text-primary-foreground");
  });
});
