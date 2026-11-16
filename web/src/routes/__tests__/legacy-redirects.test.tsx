import { render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  LegacyLogRunRedirect,
  LegacySectionRedirect,
} from "@/routes/legacy-redirects";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";

const { projectsMock } = vi.hoisted(() => ({ projectsMock: vi.fn() }));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: projectsMock,
}));

function project(id: string, is_shelved = false): Project {
  return { id, is_shelved } as Project;
}

function mockProjects(items: Project[], isLoading = false) {
  projectsMock.mockReset().mockReturnValue({
    data: { items, total: items.length, page: 1, page_size: 100 },
    isLoading,
  });
}

function LocationProbe() {
  const location = useLocation();
  return (
    <div data-testid="location">
      {location.pathname}
      {location.search}
    </div>
  );
}

function renderRedirect(ui: ReactElement, entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      {/* 旧路径挂载组件；重定向到达的新路径不渲染它，避免二次跳转 */}
      <Routes>
        <Route path="/logs/:runId" element={ui} />
        <Route path="*" element={ui} />
        <Route path="/projects/*" element={null} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

async function expectRedirected(target: string) {
  expect(await screen.findByTestId("location")).toHaveTextContent(target);
}

describe("旧版单项目路由重定向", () => {
  beforeEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
    mockProjects([project("p1"), project("p2")]);
  });

  afterEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  it("moves a valid ?project= into the path and keeps the other query params", async () => {
    renderRedirect(
      <LegacySectionRedirect section="issues" />,
      "/issues?project=p2&state=closed&q=crash",
    );

    await expectRedirected("/projects/p2/issues?state=closed&q=crash");
  });

  it("redirects every section", async () => {
    for (const section of [
      "board",
      "versions",
      "documents",
      "screenshots",
      "logs",
    ] as const) {
      const { unmount } = renderRedirect(
        <LegacySectionRedirect section={section} />,
        `/${section}?project=p1`,
      );
      await expectRedirected(`/projects/p1/${section}`);
      unmount();
    }
  });

  it("falls back to the default project when ?project= is missing", async () => {
    renderRedirect(<LegacySectionRedirect section="versions" />, "/versions");

    await expectRedirected("/projects/p1/versions");
  });

  it("falls back when ?project= is not in the project list", async () => {
    renderRedirect(
      <LegacySectionRedirect section="documents" />,
      "/documents?project=ghost",
    );

    await expectRedirected("/projects/p1/documents");
  });

  it("prefers lastSelectedProjectId as the default project", async () => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: "p2" });

    renderRedirect(<LegacySectionRedirect section="board" />, "/board");

    await expectRedirected("/projects/p2/board");
  });

  it("falls back to the first non-shelved project over shelved ones", async () => {
    mockProjects([project("p0", true), project("p1")]);

    renderRedirect(<LegacySectionRedirect section="issues" />, "/issues");

    await expectRedirected("/projects/p1/issues");
  });

  it("falls back to the first project when everything is shelved", async () => {
    mockProjects([project("p0", true)]);

    renderRedirect(
      <LegacySectionRedirect section="screenshots" />,
      "/screenshots",
    );

    await expectRedirected("/projects/p0/screenshots");
  });

  it("redirects to /projects when there is no project at all", async () => {
    mockProjects([]);

    renderRedirect(<LegacySectionRedirect section="issues" />, "/issues");

    await expectRedirected("/projects");
  });

  it("renders a placeholder without redirecting while the list is loading", async () => {
    mockProjects([], true);

    renderRedirect(
      <LegacySectionRedirect section="issues" />,
      "/issues?project=p1",
    );

    // 列表未就绪：停在原地渲染占位，避免弹到错误项目
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/issues?project=p1",
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/issues?project=p1",
    );
  });
});

describe("旧版日志运行详情重定向", () => {
  beforeEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
    mockProjects([project("p1"), project("p2")]);
  });

  afterEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  it("keeps the runId path segment and the other query params", async () => {
    renderRedirect(
      <LegacyLogRunRedirect />,
      "/logs/run-9?project=p2&level=error",
    );

    await expectRedirected("/projects/p2/logs/run-9?level=error");
  });

  it("uses the default project when ?project= is missing", async () => {
    renderRedirect(<LegacyLogRunRedirect />, "/logs/run-9");

    await expectRedirected("/projects/p1/logs/run-9");
  });

  it("redirects to /projects without any project", async () => {
    mockProjects([]);

    renderRedirect(<LegacyLogRunRedirect />, "/logs/run-9?level=error");

    await expectRedirected("/projects");
  });
});
