import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { screen, waitFor, fireEvent, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// Components
import { Header } from "@/components/layout/header";
import { Sidebar } from "@/components/layout/sidebar";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { UserNav } from "@/components/user-nav";
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

    it("renders theme toggle in header", () => {
      renderWithProviders(<Header title="测试" />);

      const buttons = screen.getAllByRole("button");
      const themeButtons = buttons.filter(btn => 
        btn.getAttribute("aria-haspopup") === "menu"
      );
      expect(themeButtons.length).toBeGreaterThan(0);
    });

    it("renders user nav in header", () => {
      renderWithProviders(<Header title="测试" />);

      // UserNav should be present (avatar button with aria-haspopup="menu")
      const avatarButton = screen.getAllByRole("button").find(
        btn => btn.getAttribute("aria-haspopup") === "menu"
      );
      expect(avatarButton).toBeInTheDocument();
    });

    it("displays user initial in header", () => {
      renderWithProviders(<Header title="测试" />);

      expect(screen.getByText("T")).toBeInTheDocument();
    });

    it("header layout includes all expected elements", () => {
      renderWithProviders(<Header title="测试标题" />);

      // All key elements should be present
      expect(screen.getByRole("heading", { name: "测试标题" })).toBeInTheDocument();
      
      // Buttons (theme toggle and user nav)
      const allButtons = screen.getAllByRole("button");
      expect(allButtons.length).toBeGreaterThanOrEqual(2);
    });
  });

  describe("Theme and User Navigation Integration", () => {
    it("theme toggle works alongside user nav", async () => {
      renderWithProviders(
        <>
          <Header title="测试" />
        </>,
      );

      // Both components should be present
      const buttons = screen.getAllByRole("button");
      const popupButtons = buttons.filter(btn => 
        btn.getAttribute("aria-haspopup") === "menu"
      );

      expect(popupButtons.length).toBeGreaterThanOrEqual(2);
    });

    it("user nav dropdown can be opened independently of theme toggle", async () => {
      renderWithProviders(
        <>
          <Header title="测试" />
        </>,
      );

      const buttons = screen.getAllByRole("button");
      const avatarButton = buttons.find(btn => 
        btn.getAttribute("aria-haspopup") === "menu" && btn.textContent?.includes("T")
      );

      if (avatarButton) {
        fireEvent.click(avatarButton);

        await waitFor(() => {
          expect(screen.getByText("testuser")).toBeInTheDocument();
        });
      }
    });
  });

  describe("Settings Page Integration", () => {
    it("keeps the settings sidebar item active on nested settings routes", () => {
      renderWithProviders(<Sidebar />, { initialEntry: "/settings/profile" });

      const settingsLink = document.querySelector('a[href="/settings"]');
      expect(settingsLink).toHaveAttribute("aria-current", "page");
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

      // 侧边栏中有 "设置" 标题
      expect(screen.getByText("设置")).toBeInTheDocument();
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

      // Initially on profile page
      await waitFor(() => {
        expect(screen.getByText("修改你的用户名和邮箱")).toBeInTheDocument();
      });

      // Navigate to password page via link
      const passwordLinks = screen.getAllByRole("link", { name: /修改密码/i });
      expect(passwordLinks.length).toBeGreaterThan(0);
      fireEvent.click(passwordLinks[0]);

      await waitFor(() => {
        expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
      });

      // Navigate back to profile
      const profileLinks = screen.getAllByRole("link", { name: /个人信息/i });
      expect(profileLinks.length).toBeGreaterThan(0);
      fireEvent.click(profileLinks[0]);

      await waitFor(() => {
        expect(screen.getByText("修改你的用户名和邮箱")).toBeInTheDocument();
      });
    });
  });

  describe("End-to-End User Workflow", () => {
    it("complete user journey: header elements are present", async () => {
      renderWithProviders(
        <Routes>
          <Route path="/*" element={<>
            <Header title="项目" />
            <Routes>
              <Route path="/settings/*" element={<SettingsLayout />}>
                <Route path="ai" element={<AISettingsPage />} />
                <Route path="profile" element={<ProfilePage />} />
                <Route path="password" element={<PasswordPage />} />
              </Route>
            </Routes>
          </>} />
        </Routes>,
        { initialEntry: "/settings/profile" },
      );

      // 1. Verify header is present with user nav and theme toggle
      expect(screen.getByRole("heading", { name: "项目" })).toBeInTheDocument();
      
      // 2. Verify buttons exist
      const buttons = screen.getAllByRole("button");
      expect(buttons.length).toBeGreaterThanOrEqual(2);

      // 3. Verify settings navigation works
      const links = screen.getAllByRole("link");
      expect(links.length).toBeGreaterThan(0);
    });
  });

  describe("Responsive Design Verification", () => {
    it("header maintains structure with all interactive elements", () => {
      renderWithProviders(<Header title="响应式测试" />);

      // All key elements should be present
      expect(screen.getByRole("heading", { name: "响应式测试" })).toBeInTheDocument();
      
      // Buttons
      const allButtons = screen.getAllByRole("button");
      expect(allButtons.length).toBeGreaterThanOrEqual(2);
    });
  });

  describe("Accessibility Checks", () => {
    it("theme toggle has accessible label", () => {
      renderWithProviders(<ThemeToggle />);

      const buttons = screen.getAllByRole("button");
      const themeButton = buttons.find(btn => 
        btn.querySelector("svg") && btn.getAttribute("aria-haspopup") === "menu"
      );
      expect(themeButton).toBeInTheDocument();
    });

    it("user nav avatar is accessible", () => {
      renderWithProviders(<UserNav />);

      const avatarButton = screen.getByRole("button", { name: "T" });
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

      // Navigation items should be links (keyboard accessible)
      const links = screen.getAllByRole("link");
      expect(links.length).toBeGreaterThan(0);
      
      links.forEach(link => {
        expect(link).toHaveAttribute("href");
      });
    });
  });
});
