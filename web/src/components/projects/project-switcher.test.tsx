import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ProjectSwitcher } from "./project-switcher";

const projects: Project[] = [
  {
    id: "fast-ship",
    user_id: "user-1",
    name: "Fast Ship",
    description: "项目管理工具",
    github_owner: "Bobo",
    github_repo: "fast_ship",
    has_github_token: false,
    has_github_pr_token: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  },
  {
    id: "notes",
    user_id: "user-1",
    name: "Notes",
    description: "团队知识库",
    github_owner: "",
    github_repo: "",
    has_github_token: false,
    has_github_pr_token: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  },
];

function renderSwitcher() {
  const onValueChange = vi.fn();
  render(
    <ProjectSwitcher
      projects={projects}
      value="fast-ship"
      onValueChange={onValueChange}
    />,
  );
  return { onValueChange };
}

describe("ProjectSwitcher", () => {
  it("触发器显示当前项目名并透传宽度与 data 属性", () => {
    render(
      <ProjectSwitcher
        projects={projects}
        value="fast-ship"
        onValueChange={vi.fn()}
        className="w-48"
        data-testid="documents-project-select"
      />,
    );

    const trigger = screen.getByRole("button", { name: "切换项目：Fast Ship" });
    expect(trigger).toHaveTextContent("Fast Ship");
    expect(trigger).toHaveClass("w-48");
    expect(trigger).toHaveAttribute("data-testid", "documents-project-select");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("没有选中项目时显示 placeholder", () => {
    render(
      <ProjectSwitcher
        projects={projects}
        value=""
        onValueChange={vi.fn()}
        placeholder="选择项目"
      />,
    );
    expect(screen.getByRole("button", { name: "切换项目：选择项目" })).toHaveTextContent(
      "选择项目",
    );
  });

  it("打开后列出全部项目卡片、标记当前项目并聚焦搜索框", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    const popup = await screen.findByRole("dialog", { name: "切换项目" });
    expect(within(popup).getAllByRole("button")).toHaveLength(2);
    expect(within(popup).getByRole("button", { name: /Fast Ship 项目管理工具/ }))
      .toHaveAttribute("aria-current", "true");
    expect(within(popup).getByRole("button", { name: /Notes 团队知识库/ }))
      .not.toHaveAttribute("aria-current");
    await waitFor(() => expect(screen.getByRole("textbox", { name: "搜索项目" })).toHaveFocus());
  });

  it("按 Issue 总数降序排列卡片，缺省按 0 且同计数保持传入顺序", async () => {
    const user = userEvent.setup();
    const unsorted: Project[] = [
      { ...projects[0], id: "few", name: "Few", issue_count: 3 },
      { ...projects[1], id: "none", name: "No Count" },
      { ...projects[0], id: "many", name: "Many", issue_count: 42 },
      { ...projects[1], id: "zero", name: "Zero", issue_count: 0 },
    ];
    render(
      <ProjectSwitcher projects={unsorted} value="" onValueChange={vi.fn()} />,
    );

    await user.click(screen.getByRole("button", { name: "切换项目：请选择项目" }));
    const popup = await screen.findByRole("dialog", { name: "切换项目" });
    expect(within(popup).getAllByRole("button").map((card) => card.textContent)).toEqual([
      expect.stringContaining("Many"),
      expect.stringContaining("Few"),
      expect.stringContaining("No Count"),
      expect.stringContaining("Zero"),
    ]);
    // 排序不得原地修改 props 数组
    expect(unsorted.map((p) => p.id)).toEqual(["few", "none", "many", "zero"]);
  });

  it("二级文字优先显示描述，无描述时回退仓库地址", async () => {
    const user = userEvent.setup();
    const mixed: Project[] = [
      { ...projects[0], id: "with-desc", name: "With Desc", description: "有描述" },
      { ...projects[0], id: "repo-only", name: "Repo Only", description: "" },
      { ...projects[0], id: "blank", name: "Blank", description: "   " },
      { ...projects[1], id: "empty", name: "Empty", description: "" },
    ];
    render(<ProjectSwitcher projects={mixed} value="" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "切换项目：请选择项目" }));
    const popup = await screen.findByRole("dialog", { name: "切换项目" });
    // 有描述时即使关联了仓库也显示描述
    expect(
      within(popup).getByRole("button", { name: /With Desc 有描述/ }),
    ).toBeInTheDocument();
    // 无描述或仅空白字符时回退到仓库 slug
    expect(
      within(popup).getByRole("button", { name: /Repo Only Bobo\/fast_ship/ }),
    ).toBeInTheDocument();
    expect(
      within(popup).getByRole("button", { name: /Blank Bobo\/fast_ship/ }),
    ).toBeInTheDocument();
  });

  it("搜索命中多个项目时回车选中计数最高者", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const matched: Project[] = [
      { ...projects[0], id: "low", name: "Match Low", issue_count: 1 },
      { ...projects[1], id: "high", name: "Match High", issue_count: 9 },
    ];
    render(<ProjectSwitcher projects={matched} value="" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "切换项目：请选择项目" }));
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "match");
    await user.keyboard("{Enter}");
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("high");
  });

  it("issue_count 全部缺省时维持传入顺序", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    const popup = await screen.findByRole("dialog", { name: "切换项目" });
    expect(within(popup).getAllByRole("button").map((card) => card.textContent)).toEqual([
      expect.stringContaining("Fast Ship"),
      expect.stringContaining("Notes"),
    ]);
  });

  it.each([
    ["nOtEs", /Notes/, /Fast Ship/],
    ["知识", /Notes/, /Fast Ship/],
    ["BOBO", /Fast Ship/, /Notes/],
    ["FAST_SHIP", /Fast Ship/, /Notes/],
    ["bobo/FAST_SHIP", /Fast Ship/, /Notes/],
  ])("按名称、描述和仓库过滤：%s", async (query, matchedName, excludedName) => {
    const user = userEvent.setup();
    renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), query);
    const popup = screen.getByRole("dialog", { name: "切换项目" });
    expect(within(popup).getAllByRole("button")).toHaveLength(1);
    expect(within(popup).getByRole("button", { name: matchedName })).toBeInTheDocument();
    expect(within(popup).queryByRole("button", { name: excludedName })).not.toBeInTheDocument();
  });

  it("搜索 / 只命中真实仓库 slug，不会匹配缺仓库的项目", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "/");
    const popup = screen.getByRole("dialog", { name: "切换项目" });
    expect(within(popup).getAllByRole("button")).toHaveLength(1);
    expect(within(popup).getByRole("button", { name: /Fast Ship/ })).toBeInTheDocument();
  });

  it("点击当前项目卡片只关闭面板，不触发回调", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.click(screen.getByRole("button", { name: /Fast Ship 项目管理工具/ }));
    expect(onValueChange).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("搜索后按回车选中首个匹配项", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "notes");
    await user.keyboard("{Enter}");
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("notes");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("搜索框为空时按回车不切换项目", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.keyboard("{Enter}");
    expect(onValueChange).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "切换项目" })).toBeInTheDocument();
  });

  it("输入法组合中按回车不选中项目", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    const input = screen.getByRole("textbox", { name: "搜索项目" });
    await user.type(input, "notes");
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    expect(onValueChange).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "切换项目" })).toBeInTheDocument();
  });

  it("过滤无结果显示无匹配项目", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "不存在");
    expect(screen.getByText("无匹配项目")).toBeInTheDocument();
    expect(within(screen.getByRole("dialog")).queryByRole("button")).not.toBeInTheDocument();
  });

  it("点击卡片触发回调并关闭面板", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();
    const trigger = screen.getByRole("button", { name: "切换项目：Fast Ship" });

    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: /Notes 团队知识库/ }));
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("notes");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("Esc 关闭面板，再打开时重置搜索", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();
    const trigger = screen.getByRole("button", { name: "切换项目：Fast Ship" });

    await user.click(trigger);
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "Notes");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(onValueChange).not.toHaveBeenCalled();

    await user.click(trigger);
    expect(screen.getByRole("textbox", { name: "搜索项目" })).toHaveValue("");
    expect(within(screen.getByRole("dialog")).getAllByRole("button")).toHaveLength(2);
  });

  it("点击外部关闭面板", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    await user.click(screen.getByRole("button", { name: "切换项目：Fast Ship" }));
    await user.click(document.body);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("禁用时无法打开面板", async () => {
    const user = userEvent.setup();
    render(
      <ProjectSwitcher
        projects={projects}
        value="fast-ship"
        onValueChange={vi.fn()}
        disabled
      />,
    );

    const trigger = screen.getByRole("button", { name: "切换项目：Fast Ship" });
    expect(trigger).toBeDisabled();
    await user.click(trigger);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
