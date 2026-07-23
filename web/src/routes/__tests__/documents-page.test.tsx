import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";
import DocumentsPage from "@/routes/documents/index";

const { createMock, updateMock } = vi.hoisted(() => ({
  createMock: vi.fn(),
  updateMock: vi.fn(),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: () => ({
    data: { items: [{ id: "proj-1", name: "Demo" }] },
    isLoading: false,
  }),
}));

vi.mock("@/lib/hooks/use-documents", () => ({
  useDocuments: () => ({
    data: {
      items: [
        {
          id: "doc-1",
          project_id: "proj-1",
          parent_id: null,
          title: "根文档",
          created_at: "2026-07-22T00:00:00Z",
          updated_at: "2026-07-22T00:00:00Z",
        },
      ],
      total: 1,
    },
    isLoading: false,
    isError: false,
  }),
  useDocument: (docId: string) => ({
    data:
      docId === "doc-1"
        ? {
            id: "doc-1",
            project_id: "proj-1",
            parent_id: null,
            title: "根文档",
            body: "hello",
            created_at: "2026-07-22T00:00:00Z",
            updated_at: "2026-07-22T00:00:00Z",
          }
        : undefined,
    isLoading: false,
    isError: false,
  }),
  useCreateDocument: () => ({
    mutateAsync: createMock,
    isPending: false,
  }),
  useUpdateDocument: () => ({
    mutateAsync: updateMock,
    isPending: false,
  }),
  useDeleteDocument: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
}));

vi.mock("@/components/ui/markdown-editor", () => ({
  MarkdownEditor: ({
    value,
    onChange,
  }: {
    value?: string;
    onChange?: (value: string) => void;
  }) => (
    <textarea
      data-testid="markdown-editor"
      value={value}
      onChange={(e) => onChange?.(e.target.value)}
    />
  ),
}));

vi.mock("@/lib/store/project-preference-store", () => ({
  useProjectPreferenceStore: () => ({
    lastSelectedProjectId: "proj-1",
    setLastSelectedProjectId: vi.fn(),
  }),
}));

function renderDocuments(entry = "/documents?project=proj-1&doc=doc-1") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/documents" element={<DocumentsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("DocumentsPage", () => {
  beforeEach(() => {
    createMock.mockReset();
    updateMock.mockReset();
    createMock.mockResolvedValue({
      data: {
        id: "doc-2",
        project_id: "proj-1",
        parent_id: null,
        title: "未命名文档",
        body: "",
        created_at: "2026-07-22T00:00:00Z",
        updated_at: "2026-07-22T00:00:00Z",
      },
    });
    updateMock.mockResolvedValue({
      data: {
        id: "doc-1",
        project_id: "proj-1",
        parent_id: null,
        title: "根文档",
        body: "hello world",
        created_at: "2026-07-22T00:00:00Z",
        updated_at: "2026-07-22T01:00:00Z",
      },
    });
  });

  it("renders tree and editor for selected document", async () => {
    renderDocuments();

    await waitFor(() => {
      expect(screen.getByTestId("documents-workspace")).toBeInTheDocument();
    });
    expect(screen.getByTestId("document-tree")).toBeInTheDocument();
    expect(screen.getByTestId("document-editor")).toBeInTheDocument();
    expect(screen.getByTestId("document-title-input")).toHaveValue("根文档");
  });

  it("creates a document and allows saving edits", async () => {
    const user = userEvent.setup();
    renderDocuments("/documents?project=proj-1");

    await waitFor(() => {
      expect(screen.getByText("新建根文档")).toBeInTheDocument();
    });

    await user.click(screen.getByText("新建根文档"));
    await waitFor(() => {
      expect(createMock).toHaveBeenCalled();
    });

    renderDocuments();
    await waitFor(() => {
      expect(screen.getByTestId("document-title-input")).toBeInTheDocument();
    });

    const editor = screen.getByTestId("markdown-editor");
    await user.clear(editor);
    await user.type(editor, "hello world");
    await user.click(screen.getByTestId("document-save-button"));

    await waitFor(() => {
      expect(updateMock).toHaveBeenCalledWith({
        docId: "doc-1",
        payload: {
          title: "根文档",
          body: "hello world",
        },
      });
    });
  });
});
