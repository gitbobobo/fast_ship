import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProjectFormDialog } from "@/components/projects/project-form-dialog";
import { renderWithRoute } from "@/test/render";
import {
  useCreateProject,
  useProject,
  useProjects,
  useUpdateProject,
} from "@/lib/hooks/use-projects";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProject: vi.fn(),
  useCreateProject: vi.fn(),
  useUpdateProject: vi.fn(),
  useProjects: vi.fn(),
}));

// 无 GitHub 仓库的 internal 项目：repository_url 回填为空，
// 更新 payload 只剩 name/description，便于断言 description 字段本身。
const projectFixture: Project = {
  id: "proj-1",
  user_id: "user-1",
  name: "fast-ship",
  description: "旧描述",
  github_owner: "",
  github_repo: "",
  has_github_token: false,
  has_github_pr_token: false,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

function renderDialog(mode: "create" | "edit") {
  return renderWithRoute(
    <ProjectFormDialog
      open={true}
      onOpenChange={vi.fn()}
      mode={mode}
      projectId={mode === "edit" ? projectFixture.id : undefined}
    />,
    { path: "/projects", initialEntry: "/projects" },
  );
}

// INT-67 回归：编辑保存的描述须随请求发出（含清空时的空串），否则后端无从写入。
describe("ProjectFormDialog", () => {
  const createMutateAsync = vi.fn();
  const updateMutateAsync = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useProject).mockReturnValue({
      data: projectFixture,
      isLoading: false,
    } as unknown as ReturnType<typeof useProject>);
    vi.mocked(useCreateProject).mockReturnValue({
      mutateAsync: createMutateAsync,
    } as unknown as ReturnType<typeof useCreateProject>);
    vi.mocked(useUpdateProject).mockReturnValue({
      mutateAsync: updateMutateAsync,
    } as unknown as ReturnType<typeof useUpdateProject>);
    vi.mocked(useProjects).mockReturnValue({
      data: { items: [], total: 0, page: 1, page_size: 20 },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);
  });

  it("编辑模式打开时回填已有描述", async () => {
    renderDialog("edit");

    expect(await screen.findByLabelText("项目描述（可选）")).toHaveValue("旧描述");
  });

  it("编辑提交修改后的描述", async () => {
    const user = userEvent.setup();
    renderDialog("edit");

    const descriptionInput = await screen.findByLabelText("项目描述（可选）");
    await user.clear(descriptionInput);
    await user.type(descriptionInput, "新描述");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "新描述",
      }),
    );
  });

  it("清空描述提交时 payload 携带空串而非省略字段", async () => {
    const user = userEvent.setup();
    renderDialog("edit");

    const descriptionInput = await screen.findByLabelText("项目描述（可选）");
    await user.clear(descriptionInput);
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith({
        name: "fast-ship",
        description: "",
      }),
    );
  });

  it("新建模式提交非空描述", async () => {
    createMutateAsync.mockResolvedValue({ data: { id: "proj-9" } });
    const user = userEvent.setup();
    renderDialog("create");

    await user.type(screen.getByLabelText("项目名称"), "new-proj");
    await user.type(screen.getByLabelText("项目描述（可选）"), "初始描述");
    await user.click(screen.getByRole("button", { name: "创建项目" }));

    await waitFor(() =>
      expect(createMutateAsync).toHaveBeenCalledWith({
        name: "new-proj",
        description: "初始描述",
      }),
    );
  });
});
