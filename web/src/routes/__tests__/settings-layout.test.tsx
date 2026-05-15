import { describe, it, expect, vi } from "vitest";
import { screen, waitFor, fireEvent, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import SettingsLayout from "@/routes/settings/layout";

vi.mock("@/components/layout/header", () => ({
  Header: ({ title }: { title?: string }) => <header>{title}</header>,
}));

function renderWithRouter(initialEntry = "/settings") {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/settings" element={<SettingsLayout />}>
          <Route path="general" element={<div>管理应用的基本设置</div>} />
          <Route path="profile" element={<div>修改你的头像、用户名和邮箱</div>} />
          <Route path="password" element={<div>设置新的登录密码</div>} />
          <Route
            path="ai"
            element={<div>配置 MiniMax 接口，用于问题详情页的智能识别建议。</div>}
          />
          <Route path="api-keys" element={<div>API Key 管理</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("Settings Layout", () => {
  it("renders settings layout with sidebar", () => {
    renderWithRouter("/settings/general");

    expect(screen.getByText("设置")).toBeInTheDocument();
    expect(screen.getAllByText("通用").length).toBeGreaterThan(0);
    expect(screen.getAllByText("个人信息").length).toBeGreaterThan(0);
    expect(screen.getAllByText("修改密码").length).toBeGreaterThan(0);
    expect(screen.getAllByText("AI 配置").length).toBeGreaterThan(0);
    expect(screen.getAllByText("API Keys").length).toBeGreaterThan(0);
  });

  it("redirects from /settings to /settings/general", async () => {
    renderWithRouter("/settings");

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });
  });

  it("navigates to general page when general link is clicked", async () => {
    renderWithRouter("/settings/profile");

    await waitFor(() => {
      expect(screen.getByText("修改你的头像、用户名和邮箱")).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByRole("link", { name: /通用/i })[0]);

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });
  });

  it("navigates to profile page when profile link is clicked", async () => {
    renderWithRouter("/settings/password");

    await waitFor(() => {
      expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByRole("link", { name: /个人信息/i })[0]);

    await waitFor(() => {
      expect(screen.getByText("修改你的头像、用户名和邮箱")).toBeInTheDocument();
    });
  });

  it("navigates to password page when password link is clicked", async () => {
    renderWithRouter("/settings/general");

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByRole("link", { name: /修改密码/i })[0]);

    await waitFor(() => {
      expect(screen.getByText("设置新的登录密码")).toBeInTheDocument();
    });
  });

  it("navigates to api-keys page when api-keys link is clicked", async () => {
    renderWithRouter("/settings/general");

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByRole("link", { name: /API Keys/i })[0]);

    await waitFor(() => {
      expect(screen.getByText("API Key 管理")).toBeInTheDocument();
    });
  });

  it("navigates to ai settings page when ai link is clicked", async () => {
    renderWithRouter("/settings/general");

    await waitFor(() => {
      expect(screen.getByText("管理应用的基本设置")).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByRole("link", { name: /AI 配置/i })[0]);

    await waitFor(() => {
      expect(
        screen.getByText("配置 MiniMax 接口，用于问题详情页的智能识别建议。"),
      ).toBeInTheDocument();
    });
  });

  it("highlights active navigation item", async () => {
    renderWithRouter("/settings/general");

    await waitFor(() => {
      const activeLinks = screen.getAllByRole("link", { current: "page" });
      expect(activeLinks.length).toBeGreaterThan(0);
      expect(activeLinks[0]).toHaveTextContent("通用");
    });
  });
});
