import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";
import LogsPage from "@/routes/logs/index";

const { listBatchesMock, copyWithToastMock } = vi.hoisted(() => ({
  listBatchesMock: vi.fn(),
  copyWithToastMock: vi.fn(),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: () => ({
    data: { items: [{ id: "proj-1", name: "Demo" }] },
    isLoading: false,
  }),
}));

vi.mock("@/lib/hooks/use-logs", () => ({
  useLogBatches: () => ({
    data: {
      items: [
        {
          id: "batch-1",
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
  useDeleteLogBatch: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useClearProjectLogs: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/lib/api/logs", () => ({
  logApi: {
    listBatches: (...args: unknown[]) => listBatchesMock(...args),
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
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("LogsPage batch list", () => {
  it("renders batch list instead of cross-batch entry stream", async () => {
    renderLogs();

    await waitFor(() => {
      expect(screen.getByTestId("log-batch-list")).toBeInTheDocument();
    });
    expect(screen.getByText("阶段说明")).toBeInTheDocument();
    expect(screen.getByText("复制批次 ID")).toBeInTheDocument();
    expect(screen.queryByPlaceholderText("搜索消息内容")).not.toBeInTheDocument();
  });
});
