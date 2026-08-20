import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { HTTPError } from "ky";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: () => ({
    data: { items: [{ id: "proj-1", name: "Demo" }] },
    isLoading: false,
  }),
}));

const mockState: { runError: unknown } = { runError: null };

vi.mock("@/lib/hooks/use-logs", () => ({
  useLogRun: () => ({
    data: mockState.runError
      ? undefined
      : {
          project_id: "proj-1",
          run_id: "run-1",
          source: "smux",
          description: "阶段说明",
          entry_count: 1,
          first_entry_at: "2026-07-01T00:00:00Z",
          last_entry_at: "2026-07-01T01:00:00Z",
          created_at: "2026-07-01T00:00:00Z",
          updated_at: "2026-07-01T01:00:00Z",
        },
    isLoading: false,
    isError: Boolean(mockState.runError),
    error: mockState.runError,
  }),
  useInfiniteLogEntries: () => ({
    data: { pages: [{ items: [], total: 0, page: 1, page_size: 50 }] },
    isLoading: false,
    isError: false,
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isFetchingNextPage: false,
  }),
  useDeleteLogRun: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/lib/store/project-preference-store", () => ({
  useProjectPreferenceStore: () => ({
    lastSelectedProjectId: "proj-1",
    setLastSelectedProjectId: vi.fn(),
  }),
}));

vi.mock("@/lib/copy", () => ({
  copyWithToast: vi.fn(),
}));

async function renderRunDetail(search = "") {
  const { default: LogRunDetailPage } = await import(
    "@/routes/logs/$runId"
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/logs/run-1${search}`]}>
        <Routes>
          <Route path="/logs/:runId" element={<LogRunDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function levelSelectTrigger() {
  return screen.getAllByRole("combobox")[1];
}

describe("LogRunDetailPage level select label", () => {
  it("shows 全部级别 instead of all when no level filter", async () => {
    await renderRunDetail("?project=proj-1");

    await waitFor(() => {
      expect(levelSelectTrigger()).toHaveTextContent("全部级别");
    });
    expect(levelSelectTrigger()).not.toHaveTextContent(/^all$/);
  });

  it("shows Error instead of error when level=error", async () => {
    await renderRunDetail("?project=proj-1&level=error");

    await waitFor(() => {
      expect(levelSelectTrigger()).toHaveTextContent("Error");
    });
    expect(levelSelectTrigger()).not.toHaveTextContent(/^error$/);
  });
});

describe("LogRunDetailPage not found", () => {
  afterEach(() => {
    mockState.runError = null;
  });

  it("shows a 返回日志列表 button when the run does not exist", async () => {
    const error = Object.create(HTTPError.prototype) as HTTPError;
    Object.defineProperty(error, "response", {
      value: new Response(null, { status: 404 }),
    });
    mockState.runError = error;

    await renderRunDetail("?project=proj-1");

    await waitFor(() => {
      expect(screen.getByText("运行不存在")).toBeInTheDocument();
    });
    expect(
      screen.getByRole("button", { name: "返回日志列表" }),
    ).toBeInTheDocument();
  });
});
