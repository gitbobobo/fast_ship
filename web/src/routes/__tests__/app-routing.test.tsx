import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { beforeEach, vi } from "vitest";

const { browserEntries, authState, projectsMock } = vi.hoisted(() => ({
  browserEntries: ["/"] as string[],
  authState: {
    token: null as string | null,
    user: null as unknown,
    setUser: vi.fn(),
    logout: vi.fn(),
  },
  projectsMock: vi.fn(),
}));

vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();

  return {
    ...actual,
    BrowserRouter: ({ children }: { children: ReactNode }) => (
      <actual.MemoryRouter initialEntries={browserEntries}>
        {children}
      </actual.MemoryRouter>
    ),
    Navigate: ({ to }: { to: string }) => (
      <div data-testid="navigate-target">{String(to)}</div>
    ),
  };
});

vi.mock("@/lib/store/auth-store", () => {
  // watch*Identity 会订阅 zustand store：mock 需要带 subscribe/getState/setState
  const store = vi.fn(
    (selector?: (state: typeof authState) => unknown) =>
      selector ? selector(authState) : authState,
  ) as unknown as Record<string, unknown>;
  store.subscribe = vi.fn(() => vi.fn());
  store.getState = vi.fn(() => authState);
  store.setState = vi.fn();
  return { useAuthStore: store };
});

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    me: vi.fn().mockRejectedValue(new Error("测试环境不联网")),
    logout: vi.fn(),
  },
}));

vi.mock("@/components/layout/sidebar", () => ({
  Sidebar: () => <div>侧边栏</div>,
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: projectsMock,
}));

// 路由存在性用桩页面断言；旧路径断言重定向目标
vi.mock("@/routes/issues/index", () => ({ default: () => "issues-page-stub" }));
vi.mock("@/routes/board/index", () => ({ default: () => "board-page-stub" }));
vi.mock("@/routes/versions/index", () => ({
  default: () => "versions-page-stub",
}));
vi.mock("@/routes/documents/index", () => ({
  default: () => "documents-page-stub",
}));
vi.mock("@/routes/screenshots/index", () => ({
  default: () => "screenshots-page-stub",
}));
vi.mock("@/routes/logs/index", () => ({ default: () => "logs-page-stub" }));
vi.mock("@/routes/logs/$runId", () => ({
  default: () => "log-run-detail-stub",
}));

import App from "@/App";

function login() {
  authState.token = "jwt-token";
}

function goto(entry: string) {
  browserEntries.splice(0, browserEntries.length, entry);
  return render(<App />);
}

beforeEach(() => {
  authState.token = null;
  authState.user = null;
  browserEntries.splice(0, browserEntries.length, "/");
  projectsMock.mockReset().mockReturnValue({
    data: {
      items: [{ id: "proj-1", name: "Demo", is_shelved: false }],
      total: 1,
      page: 1,
      page_size: 100,
    },
    isLoading: false,
  });
});

describe("App routing defaults", () => {
  it("redirects the app root to dashboard", async () => {
    goto("/");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/dashboard",
    );
  });

  it("redirects unmatched routes to dashboard", async () => {
    goto("/does-not-exist");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/dashboard",
    );
  });

  it("keeps the login gate ahead of the legacy documents redirect", async () => {
    goto("/documents");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/login",
    );
  });

  it("registers the reviews route", async () => {
    goto("/reviews");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/login",
    );
  });
});

describe("App 旧版单项目路由重定向", () => {
  it("moves ?project= into the path and keeps the other query params", async () => {
    login();
    goto("/issues?project=proj-1&state=closed");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/projects/proj-1/issues?state=closed",
    );
  });

  it("falls back to the default project without ?project=", async () => {
    login();
    goto("/board");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/projects/proj-1/board",
    );
  });

  it("falls back when ?project= is not in the project list", async () => {
    login();
    goto("/versions?project=ghost");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/projects/proj-1/versions",
    );
  });

  it("keeps the runId segment and the other query params for log runs", async () => {
    login();
    goto("/logs/run-9?project=proj-1&level=error");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/projects/proj-1/logs/run-9?level=error",
    );
  });

  it("redirects to /projects when there is no project at all", async () => {
    login();
    projectsMock.mockReturnValue({
      data: { items: [], total: 0, page: 1, page_size: 100 },
      isLoading: false,
    });
    goto("/screenshots");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/projects",
    );
  });
});

describe("App 项目级路由", () => {
  it("renders the issues page under /projects/:id/issues", async () => {
    login();
    goto("/projects/proj-1/issues");

    expect(await screen.findByText("issues-page-stub")).toBeInTheDocument();
  });

  it("renders the board page under /projects/:id/board", async () => {
    login();
    goto("/projects/proj-1/board");

    expect(await screen.findByText("board-page-stub")).toBeInTheDocument();
  });

  it("renders the versions page under /projects/:id/versions", async () => {
    login();
    goto("/projects/proj-1/versions");

    expect(await screen.findByText("versions-page-stub")).toBeInTheDocument();
  });

  it("renders the documents page under /projects/:id/documents", async () => {
    login();
    goto("/projects/proj-1/documents");

    expect(await screen.findByText("documents-page-stub")).toBeInTheDocument();
  });

  it("renders the screenshots page under /projects/:id/screenshots", async () => {
    login();
    goto("/projects/proj-1/screenshots");

    expect(
      await screen.findByText("screenshots-page-stub"),
    ).toBeInTheDocument();
  });

  it("renders the logs page under /projects/:id/logs", async () => {
    login();
    goto("/projects/proj-1/logs");

    expect(await screen.findByText("logs-page-stub")).toBeInTheDocument();
  });

  it("renders the log run detail under /projects/:id/logs/:runId", async () => {
    login();
    goto("/projects/proj-1/logs/run-3");

    expect(await screen.findByText("log-run-detail-stub")).toBeInTheDocument();
  });

  it("sends /projects/:id/versions/new straight to the new list page with create=1", async () => {
    login();
    goto("/projects/proj-1/versions/new");

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/projects/proj-1/versions?create=1",
    );
  });
});
