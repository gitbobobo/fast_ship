import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { Link } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AppLayout from "@/routes/_layout";
import { useAuthStore } from "@/lib/store/auth-store";
import { resetScrollPositions } from "@/lib/scroll-positions";

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    me: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

vi.mock("@/components/layout/sidebar", () => ({
  Sidebar: () => <div>侧边栏</div>,
}));

const scrollTopValues = new WeakMap<HTMLElement, number>();

function installScrollTopStub() {
  Object.defineProperty(HTMLElement.prototype, "scrollTop", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollTopValues.get(this) ?? 0;
    },
    set(this: HTMLElement, value: number) {
      scrollTopValues.set(this, Number(value) || 0);
    },
  });
}

const authState = {
  token: "jwt-token",
  user: {
    id: "user-1",
    username: "godbobo",
    email: "godbobo@example.com",
    avatar_url: "",
    created_at: "2026-04-06T10:00:00Z",
    updated_at: "2026-04-06T10:00:00Z",
  },
  setUser: vi.fn(),
  logout: vi.fn(),
};

function ListPage() {
  return (
    <div>
      <p>问题列表</p>
      <Link to="/projects/p1/issues/i1">打开详情</Link>
    </div>
  );
}

function DetailPage() {
  return (
    <div>
      <p>问题详情</p>
      <Link to="/issues">返回列表</Link>
    </div>
  );
}

describe("layout list scroll restoration", () => {
  beforeEach(() => {
    resetScrollPositions();
    installScrollTopStub();
    vi.mocked(useAuthStore).mockImplementation(((
      selector?: (state: typeof authState) => unknown,
    ) => (selector ? selector(authState) : authState)) as typeof useAuthStore);
  });

  afterEach(() => {
    resetScrollPositions();
    vi.clearAllMocks();
  });

  it("restores main scroll after leaving a list page and coming back", () => {
    render(
      <MemoryRouter initialEntries={["/issues"]}>
        <Routes>
          <Route element={<AppLayout />}>
            <Route path="/issues" element={<ListPage />} />
            <Route path="/projects/:id/issues/:iid" element={<DetailPage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const main = document.querySelector("main") as HTMLElement;
    main.scrollTop = 560;
    fireEvent.scroll(main);

    fireEvent.click(screen.getByRole("link", { name: "打开详情" }));
    expect(screen.getByText("问题详情")).toBeInTheDocument();
    expect(main.scrollTop).toBe(0);

    fireEvent.click(screen.getByRole("link", { name: "返回列表" }));
    expect(screen.getByText("问题列表")).toBeInTheDocument();
    expect(main.scrollTop).toBe(560);
  });
});
