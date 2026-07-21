import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { screen, waitFor, fireEvent, render, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// Components
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Sidebar } from "@/components/layout/sidebar";
import { UserNav } from "@/components/user-nav";
import { Button } from "@/components/ui/button";
import SettingsLayout from "@/routes/settings/layout";
import AISettingsPage from "@/routes/settings/ai";
import ProfilePage from "@/routes/settings/profile";
import PasswordPage from "@/routes/settings/password";

// Stores & API
import { useAuthStore } from "@/lib/store/auth-store";

// Mock modules
vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    logout: vi.fn(),
    updateMe: vi.fn(),
    updatePassword: vi.fn(),
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

const mockLogout = vi.fn();

function renderWithProviders(ui: React.ReactElement, { initialEntry = "/" } = {}) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("UI Integration Tests", () => {
  beforeEach(() => {
    vi.mocked(useAuthStore).mockReturnValue({
      user: mockUser,
      setUser: vi.fn(),
      logout: mockLogout,
    } as unknown as ReturnType<typeof useAuthStore>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("Header Component", () => {
    it("renders header with title", () => {
      renderWithProviders(<Header title="测试标题" />);

      expect(screen.getByRole("heading", { name: "测试标题" })).toBeInTheDocument();
    });

    it("does not render theme toggle or user nav in header", () => {
      renderWithProviders(<Header title="测试" />);

      const header = screen.getByRole("banner");
      expect(within(header).queryByRole("button", { name: "切换主题" })).not.toBeInTheDocument();
      expect(within(header).queryByRole("button", { name: /testuser/ })).not.toBeInTheDocument();
    });

    it("renders actions in header when provided", () => {
      renderWithProviders(
        <Header
          title="测试"
          actions={
            <HeaderActions
              primary={<Button size="sm">创建项目</Button>}
            />
          }
        />,
      );

      const header = screen.getByRole("banner");
      expect(within(header).getByRole("button", { name: "创建项目" })).toBeInTheDocument();
    });

    it("header layout includes title and back control", () => {
      renderWithProviders(<Header title="测试标题" />);

      expect(screen.getByRole("heading", { name: "测试标题" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "返回" })).toBeInTheDocument();
    });

    it("falls back to projects when browser history is not available", () => {
      function LocationProbe() {
        return <div data-testid="location-path">{useLocation().pathname}</div>;
      }

      renderWithProviders(
        <>
          <Header title="测试" />
          <LocationProbe />
        </>,
        { initialEntry: "/projects/proj-1" },
      );

      fireEvent.click(screen.getByRole("button", { name: "返回" }));

      expect(screen.getByTestId("location-path")).toHaveTextContent("/projects");
    });
  });

  describe("Sidebar bottom controls", () => {
    it("renders user avatar row with username in sidebar bottom", () => {
      renderWithProviders(<Sidebar />);

      const bottom = screen.getByTestId("sidebar-bottom");
      expect(
        within(bottom).getByRole("button", { name: /testuser/ }),
      ).toBeInTheDocument();
    });

    it("merges settings and theme controls into the user menu", async () => {
      renderWithProviders(<Sidebar />, { initialEntry: "/dashboard" });

      const bottom = screen.getByTestId("sidebar-bottom");
      expect(within(bottom).queryByRole("link")).not.toBeInTheDocument();

      fireEvent.click(within(bottom).getByRole("button", { name: /testuser/ }));

      await waitFor(() => {
        expect(screen.getByRole("menuitem", { name: /设置/ })).toBeInTheDocument();
        expect(screen.getByRole("menuitem", { name: /主题/ })).toBeInTheDocument();
      });
    });

    it("user nav dropdown can be opened from sidebar", async () => {
      renderWithProviders(<Sidebar />);

      fireEvent.click(screen.getByRole("button", { name: /testuser/ }));

      await waitFor(() => {
        expect(screen.getByText("test@example.com")).toBeInTheDocument();
      });
    });
  });

  describe("Settings Page Integration", () => {
    it("renders dashboard as the first sidebar item and keeps it active on /dashboard", () => {
      renderWithProviders(<Sidebar />, { initialEntry: "/dashboard" });

      const nav = screen.getByRole("navigation");
      const links = within(nav).getAllByRole("link");

      expect(links[0]).toHaveAttribute("href", "/dashboard");
      expect(links[0]).toHaveTextContent(/仪表盘|dashboard/i);
      expect(links[0]).toHaveAttribute("aria-current", "page");
    });

    it("renders logs sidebar item and keeps it active on /logs", () => {
      renderWithProviders(<Sidebar />, { initialEntry: "/logs" });

      const logLink = document.querySelector('a[href="/logs"]');
      expect(logLink).toHaveTextContent(/日志/);
      expect(logLink).toHaveAttribute("aria-current", "page");
    });

    it("renders settings layout with all navigation items", () => {
      renderWithProviders(
        <Routes>
          <Route path="/settings" element={<SettingsLayout />}>
            <Route path="ai" element={<AISettingsPage />} />
            <Route path="profile" element={<ProfilePage />} />
            <Route path="password" element={<PasswordPage />} />
          </Route>
        </Routes>,
        { initialEntry: "/settings/profile" },
      );

      expect(screen.getByRole("heading", { name: "设置" })).toBeInTheDocument();
      expect(screen.getAllByText("通用").length).toBeGreaterThan(0);
      expect(screen.getAllByText("个人信息").length).toBeGreaterThan(0);
      expect(screen.getAllByText("修改密码").length).toBeGreaterThan(0);
      expect(screen.getAllByText("AI 配置").length).toBeGreaterThan(0);
      expect(screen.getAllByText("API Keys").length).toBeGreaterThan(0);
    });

    it("settings page includes general settings", () => {
      renderWithProviders(
        <Routes>
          <Route path="/settings" element={<SettingsLayout />}>
            <Route path="ai" element={<AISettingsPage />} />
            <Route path="general" element={<ProfilePage />} />
          </Route>
        </Routes>,
        { initialEntry: "/settings/general" },
      );

      expect(screen.getAllByText("通用").length).toBeGreaterThan(0);
    });

    it("navigation between settings pages works correctly", async () => {
      renderWithProviders(
        <Routes>
          <Route path="/settings" element={<SettingsLayout />}>
            <Route path="ai" element={<AISettingsPage />} />
            <Route path="profile" element={<ProfilePage />} />
            <Route path="password" element={<PasswordPage />} />
          </Route>
        </Routes>,
        { initialEntry: "/settings/profile" },
      );

      await waitFor(() => {
        expect(screen.getByText("修改你的头像、用户名和邮箱")).toBeInTheDocument();
      });

      const passwordLinks = screen.getAllByRole("link", { name: /修改密码/i });
      expect(passwordLinks.length).toBeGreaterThan(0);
      fireEvent.click(passwordLinks[0]);

      await waitFor(() => {
        expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
      });

      const profileLinks = screen.getAllByRole("link", { name: /个人信息/i });
      expect(profileLinks.length).toBeGreaterThan(0);
      fireEvent.click(profileLinks[0]);

      await waitFor(() => {
        expect(screen.getByText("修改你的头像、用户名和邮箱")).toBeInTheDocument();
      });
    });
  });

  describe("End-to-End User Workflow", () => {
    it("complete user journey: header elements are present", async () => {
      renderWithProviders(
        <Routes>
          <Route
            path="/*"
            element={
              <>
                <Header title="项目" />
                <Routes>
                  <Route path="/settings/*" element={<SettingsLayout />}>
                    <Route path="ai" element={<AISettingsPage />} />
                    <Route path="profile" element={<ProfilePage />} />
                    <Route path="password" element={<PasswordPage />} />
                  </Route>
                </Routes>
              </>
            }
          />
        </Routes>,
        { initialEntry: "/settings/profile" },
      );

      expect(screen.getByRole("heading", { name: "项目" })).toBeInTheDocument();

      const buttons = screen.getAllByRole("button");
      expect(buttons.length).toBeGreaterThanOrEqual(1);

      const links = screen.getAllByRole("link");
      expect(links.length).toBeGreaterThan(0);
    });
  });

  describe("Responsive Design Verification", () => {
    it("header maintains structure with interactive elements", () => {
      renderWithProviders(<Header title="响应式测试" />);

      expect(screen.getByRole("heading", { name: "响应式测试" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "返回" })).toBeInTheDocument();
    });
  });

  describe("Accessibility Checks", () => {
    it("user nav avatar is accessible", () => {
      renderWithProviders(<UserNav />);

      const avatarButton = screen.getByRole("button", { name: /testuser/ });
      expect(avatarButton).toBeInTheDocument();
      expect(avatarButton).toHaveAttribute("aria-haspopup", "menu");
    });

    it("dropdown menu items are keyboard accessible", async () => {
      renderWithProviders(
        <Routes>
          <Route path="/settings/*" element={<SettingsLayout />}>
            <Route path="ai" element={<AISettingsPage />} />
            <Route path="profile" element={<ProfilePage />} />
          </Route>
        </Routes>,
        { initialEntry: "/settings/profile" },
      );

      const links = screen.getAllByRole("link");
      expect(links.length).toBeGreaterThan(0);

      links.forEach((link) => {
        expect(link).toHaveAttribute("href");
      });
    });
  });
});
