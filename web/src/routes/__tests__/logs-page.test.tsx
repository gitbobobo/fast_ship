import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import LogsPage from "@/routes/logs/index";

const { listRunsMock, copyWithToastMock } = vi.hoisted(() => ({
  listRunsMock: vi.fn(),
  copyWithToastMock: vi.fn(),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: () => ({
    data: { items: [{ id: "proj-1", name: "Demo" }] },
    isLoading: false,
  }),
}));

vi.mock("@/lib/hooks/use-logs", () => ({
  useLogRuns: () => ({
    data: {
      items: [
        {
          project_id: "proj-1",
          run_id: "run-1",
          source: "smux",
          description: "阶段说明",
          entry_count: 3,
          first_entry_at: "2026-07-01T00:00:00Z",
          last_entry_at: "2026-07-01T01:00:00Z",
          created_at: "2026-07-01T00:00:00Z",
          updated_at: "2026-07-01T01:00:00Z",
        },
      ],
      total: 1,
      page: 1,
      page_size: 50,
    },
    isLoading: false,
    isError: false,
  }),
  useDeleteLogRun: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useClearProjectLogs: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/lib/api/logs", () => ({
  logApi: {
    listRuns: (...args: unknown[]) => listRunsMock(...args),
  },
}));

vi.mock("@/lib/copy", () => ({
  copyWithToast: (...args: unknown[]) => copyWithToastMock(...args),
}));

vi.mock("@/lib/store/project-preference-store", () => ({
  useProjectPreferenceStore: () => ({
    lastSelectedProjectId: "proj-1",
    setLastSelectedProjectId: vi.fn(),
  }),
}));

function renderLogs() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/logs?project=proj-1"]}>
        <Routes>
          <Route path="/logs" element={<LogsPage />} />
          <Route
            path="/logs/:runId"
            element={<div data-testid="log-detail">detail</div>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("LogsPage run list", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders run list instead of cross-run entry stream", async () => {
    renderLogs();

    await waitFor(() => {
      expect(screen.getByTestId("log-run-list")).toBeInTheDocument();
    });
    expect(screen.getByText("阶段说明")).toBeInTheDocument();
    expect(screen.getByText("复制运行 ID")).toBeInTheDocument();
    expect(screen.queryByPlaceholderText("搜索消息内容")).not.toBeInTheDocument();
  });

  it("opens run detail when clicking the run description", async () => {
    renderLogs();

    await waitFor(() => {
      expect(screen.getByText("阶段说明")).toBeInTheDocument();
    });

    fireEvent.click(screen.getByText("阶段说明"));

    expect(screen.getByTestId("log-detail")).toBeInTheDocument();
  });

  it("does not open run detail when copying the run id", async () => {
    renderLogs();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "复制运行 ID" })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("button", { name: "复制运行 ID" }));

    expect(copyWithToastMock).toHaveBeenCalledWith("run-1", "已复制运行 ID");
    expect(screen.queryByTestId("log-detail")).not.toBeInTheDocument();
    expect(screen.getByTestId("log-run-list")).toBeInTheDocument();
  });

  it("does not open run detail when clicking delete", async () => {
    renderLogs();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "删除" })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole("button", { name: "删除" }));

    expect(screen.queryByTestId("log-detail")).not.toBeInTheDocument();
    expect(screen.getByTestId("log-run-list")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "删除该运行日志？" }),
    ).toBeInTheDocument();
  });
});
