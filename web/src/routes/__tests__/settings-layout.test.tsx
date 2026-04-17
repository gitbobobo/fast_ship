import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { screen, waitFor, fireEvent, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import SettingsLayout from "@/routes/settings/layout";
import GeneralPage from "@/routes/settings/general";
import AISettingsPage from "@/routes/settings/ai";
import ProfilePage from "@/routes/settings/profile";
import PasswordPage from "@/routes/settings/password";
import ApiKeysPage from "@/routes/settings/api-keys";
import { useAuthStore } from "@/lib/store/auth-store";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// Mock modules
vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    updateMe: vi.fn(),
    updatePassword: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-api-keys", () => ({
  useApiKeys: vi.fn(() => ({ data: [], isLoading: false })),
  useCreateApiKey: vi.fn(() => ({ mutateAsync: vi.fn() })),
  useDeleteApiKey: vi.fn(() => ({ mutateAsync: vi.fn() })),
}));

vi.mock("@/lib/hooks/use-ai", () => ({
  useAISettings: vi.fn(() => ({
    data: {
      api_host: "https://api.minimaxi.com",
      model: "MiniMax-M2.5",
      configured: false,
      updated_at: null,
    },
    isLoading: false,
  })),
  useUpdateAISettings: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
}));

const mockUser = {
  id: "user-1",
  username: "testuser",
  email: "test@example.com",
  created_at: "2026-04-06T10:00:00Z",
  updated_at: "2026-04-06T10:00:00Z",
};

function renderWithProviders(ui: React.ReactElement, { initialEntry = "/settings" } = {}) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route path="/settings" element={ui}>
            <Route path="general" element={<GeneralPage />} />
            <Route path="profile" element={<ProfilePage />} />
            <Route path="password" element={<PasswordPage />} />
            <Route path="ai" element={<AISettingsPage />} />
            <Route path="api-keys" element={<ApiKeysPage />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Settings Layout", () => {
  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: mockUser,
      setUser: vi.fn(),
    } as unknown as ReturnType<typeof useAuthStore>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders settings layout with sidebar", () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/general" });

    // 检查侧边栏标题和主要元素
    expect(screen.getByText("设置")).toBeInTheDocument();
    expect(screen.getAllByText("通用").length).toBeGreaterThan(0);
    expect(screen.getAllByText("个人信息").length).toBeGreaterThan(0);
    expect(screen.getAllByText("修改密码").length).toBeGreaterThan(0);
    expect(screen.getAllByText("AI 配置").length).toBeGreaterThan(0);
    expect(screen.getAllByText("API Keys").length).toBeGreaterThan(0);
  });

  it("redirects from /settings to /settings/general", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings" });

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });
  });

  it("navigates to general page when general link is clicked", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/profile" });

    // 先确保在个人信息页面
    await waitFor(() => {
      expect(screen.getByText("修改你的用户名和邮箱")).toBeInTheDocument();
    });

    // 点击通用导航链接
    const links = screen.getAllByRole("link", { name: /通用/i });
    expect(links.length).toBeGreaterThan(0);
    fireEvent.click(links[0]);

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });
  });

  it("navigates to profile page when profile link is clicked", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/password" });

    // 先确保在密码页面
    await waitFor(() => {
      expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
    });

    // 点击个人信息导航链接
    const links = screen.getAllByRole("link", { name: /个人信息/i });
    expect(links.length).toBeGreaterThan(0);
    fireEvent.click(links[0]);

    await waitFor(() => {
      expect(screen.getByText("修改你的用户名和邮箱")).toBeInTheDocument();
    });
  });

  it("navigates to password page when password link is clicked", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/general" });

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });

    // 点击修改密码导航链接
    const links = screen.getAllByRole("link", { name: /修改密码/i });
    expect(links.length).toBeGreaterThan(0);
    fireEvent.click(links[0]);

    await waitFor(() => {
      expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
    });
  });

  it("navigates to api-keys page when api-keys link is clicked", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/general" });

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });

    // 点击 API Keys 导航链接
    const links = screen.getAllByRole("link", { name: /API Keys/i });
    expect(links.length).toBeGreaterThan(0);
    fireEvent.click(links[0]);

    await waitFor(() => {
      expect(screen.getByText("API Key 管理")).toBeInTheDocument();
    });
  });

  it("navigates to ai settings page when ai link is clicked", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/general" });

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });

    const links = screen.getAllByRole("link", { name: /AI 配置/i });
    expect(links.length).toBeGreaterThan(0);
    fireEvent.click(links[0]);

    await waitFor(() => {
      expect(screen.getByText("配置 MiniMax 接口，用于问题详情页的智能识别建议。")).toBeInTheDocument();
    });
  });

  it("highlights active navigation item", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/general" });

    await waitFor(() => {
      const activeLinks = screen.getAllByRole("link", { current: "page" });
      expect(activeLinks.length).toBeGreaterThan(0);
      expect(activeLinks[0]).toHaveTextContent("通用");
    });
  });
});

describe("Settings Pages Content", () => {
  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: mockUser,
      setUser: vi.fn(),
    } as unknown as ReturnType<typeof useAuthStore>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("general page displays theme settings", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/general" });

    await waitFor(() => {
      // 使用更具体的文本来匹配页面内容
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
      expect(screen.getByText("主题")).toBeInTheDocument();
    });
  });

  it("profile page displays user information form", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/profile" });

    await waitFor(() => {
      expect(screen.getByText("修改你的用户名和邮箱")).toBeInTheDocument();
      expect(screen.getByLabelText("用户名")).toBeInTheDocument();
      expect(screen.getByLabelText("邮箱")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "保存" })).toBeInTheDocument();
    });
  });

  it("profile page pre-fills user data", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/profile" });

    await waitFor(() => {
      const usernameInput = screen.getByLabelText("用户名") as HTMLInputElement;
      const emailInput = screen.getByLabelText("邮箱") as HTMLInputElement;
      
      expect(usernameInput.value).toBe("testuser");
      expect(emailInput.value).toBe("test@example.com");
    });
  });

  it("password page displays password form", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/password" });

    await waitFor(() => {
      expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
      expect(screen.getByLabelText("当前密码")).toBeInTheDocument();
      expect(screen.getByLabelText("新密码")).toBeInTheDocument();
      expect(screen.getByLabelText("确认新密码")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "修改密码" })).toBeInTheDocument();
    });
  });

  it("api-keys page displays api key management", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/api-keys" });

    await waitFor(() => {
      expect(screen.getByText("API Key 管理")).toBeInTheDocument();
      expect(screen.getByText("API Key 用于 CI/CD 等自动化场景，仅拥有受限权限")).toBeInTheDocument();
    });
  });

  it("ai page displays minimax settings form", async () => {
    renderWithProviders(<SettingsLayout />, { initialEntry: "/settings/ai" });

    await waitFor(() => {
      expect(screen.getByText("配置 MiniMax 接口，用于问题详情页的智能识别建议。")).toBeInTheDocument();
      expect(screen.getByLabelText("API Host")).toBeInTheDocument();
      expect(screen.getByLabelText("模型")).toBeInTheDocument();
      expect(screen.getByLabelText("API Key")).toBeInTheDocument();
    });
  });
});
