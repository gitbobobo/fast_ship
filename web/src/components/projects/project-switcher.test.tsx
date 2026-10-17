import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
    public_github_token_id: "",
    code_github_token_id: "",
    issue_workflow_counts: { unset: 1, todo: 2, in_progress: 3, done: 4 },
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
    public_github_token_id: "",
    code_github_token_id: "",
    issue_workflow_counts: { unset: 0, todo: 5, in_progress: 0, done: 1 },
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

async function openPanel(
  user: ReturnType<typeof userEvent.setup>,
  triggerName = "切换项目：Fast Ship",
) {
  await user.click(screen.getByRole("button", { name: triggerName }));
  return screen.findByRole("dialog", { name: "切换项目" });
}

const listboxOf = (popup: HTMLElement) =>
  within(popup).getByRole("listbox", { name: "项目列表" });

describe("ProjectSwitcher", () => {
  it("触发器显示当前项目名与首字母徽标，透传宽度与 data 属性", () => {
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
    // 徽标取项目名首字符（多词名取首字母），背景色来自固定色板
    const badge = within(trigger).getByText("FS");
    expect(badge.className).toMatch(/bg-[a-z]+-500/);
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
    expect(
      screen.getByRole("button", { name: "切换项目：选择项目" }),
    ).toHaveTextContent("选择项目");
  });

  it("打开后列出项目列表、标记当前项目并聚焦搜索框", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    const popup = await openPanel(user);
    const options = within(listboxOf(popup)).getAllByRole("option");
    expect(options).toHaveLength(2);
    expect(options[0]).toHaveTextContent("Fast Ship");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options[0]).toHaveAttribute("data-highlighted", "true");
    expect(options[1]).toHaveTextContent("Notes");
    expect(options[1]).toHaveAttribute("aria-selected", "false");
    await waitFor(() =>
      expect(
        screen.getByRole("textbox", { name: "搜索项目" }),
      ).toHaveFocus(),
    );
  });

  it("行内渲染徽标、副标题与状态计数点，不渲染图标", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    const popup = await openPanel(user);
    const row = within(popup).getByRole("option", { name: /Fast Ship/ });

    // 图标已移除：行内除文本与圆点外不再有 SVG
    expect(row.querySelector("svg")).toBeNull();
    expect(within(row).getByText("FS")).toBeInTheDocument();
    expect(within(row).getByText("项目管理工具")).toBeInTheDocument();
    // 首行可见行右侧带 ⌘N/Ctrl+N 直选 chip
    expect(within(row).getByText(/^(⌘|Ctrl\+)1$/)).toBeInTheDocument();

    for (const [label, count] of [
      ["未设置", 1],
      ["待处理", 2],
      ["开发中", 3],
      ["已完成", 4],
    ] as const) {
      const segment = within(row).getByTitle(`${label} ${count}`);
      expect(segment).toHaveTextContent(String(count));
    }
  });

  it("徽标按项目名取 1–2 个字符，颜色按项目 id 稳定散列", async () => {
    const user = userEvent.setup();
    const named: Project[] = [
      { ...projects[0], id: "a", name: "Fast Ship" },
      { ...projects[0], id: "b", name: "notes" },
      { ...projects[0], id: "c", name: "知识库" },
    ];
    render(<ProjectSwitcher projects={named} value="" onValueChange={vi.fn()} />);

    const popup = await openPanel(user, "切换项目：请选择项目");
    const badges = {
      a: within(within(popup).getByRole("option", { name: /Fast Ship/ })).getByText("FS"),
      b: within(within(popup).getByRole("option", { name: /notes/ })).getByText("NO"),
      c: within(within(popup).getByRole("option", { name: /知识库/ })).getByText("知识"),
    };
    // 同一项目 id 每次渲染颜色一致（固定色板散列，不依赖渲染次序）
    for (const badge of Object.values(badges)) {
      expect(badge.className).toMatch(/bg-[a-z]+-500/);
    }
  });

  it("按开启状态 Issue 数降序排列，缺省按 0 且同计数保持传入顺序", async () => {
    const user = userEvent.setup();
    const counts = (
      n: Partial<NonNullable<Project["issue_workflow_counts"]>>,
    ) => ({
      unset: 0,
      todo: 0,
      in_progress: 0,
      done: 0,
      ...n,
    });
    // few 的 issue_count 总数虽高，但开启的只有 1 条——排序只看开启口径
    const unsorted: Project[] = [
      { ...projects[0], id: "few", name: "Few", issue_count: 99, issue_workflow_counts: counts({ unset: 1 }) },
      { ...projects[1], id: "none", name: "No Count", issue_workflow_counts: undefined },
      { ...projects[0], id: "many", name: "Many", issue_count: 7, issue_workflow_counts: counts({ todo: 3, in_progress: 4 }) },
      { ...projects[1], id: "zero", name: "Zero", issue_workflow_counts: counts({}) },
    ];
    render(
      <ProjectSwitcher projects={unsorted} value="" onValueChange={vi.fn()} />,
    );

    const popup = await openPanel(user, "切换项目：请选择项目");
    expect(
      within(listboxOf(popup))
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual([
      expect.stringContaining("Many"),
      expect.stringContaining("Few"),
      expect.stringContaining("No Count"),
      expect.stringContaining("Zero"),
    ]);
    // 排序不得原地修改 props 数组
    expect(unsorted.map((p) => p.id)).toEqual(["few", "none", "many", "zero"]);
  });

  it("issue_workflow_counts 全部缺省时维持传入顺序", async () => {
    const user = userEvent.setup();
    const noCounts: Project[] = [
      { ...projects[0], issue_workflow_counts: undefined },
      { ...projects[1], issue_workflow_counts: undefined },
    ];
    render(
      <ProjectSwitcher
        projects={noCounts}
        value="fast-ship"
        onValueChange={vi.fn()}
      />,
    );

    const popup = await openPanel(user);
    expect(
      within(listboxOf(popup))
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual([
      expect.stringContaining("Fast Ship"),
      expect.stringContaining("Notes"),
    ]);
  });

  it("副标题优先显示描述，无描述时回退仓库地址，都没有则省略", async () => {
    const user = userEvent.setup();
    const mixed: Project[] = [
      { ...projects[0], id: "with-desc", name: "With Desc", description: "有描述" },
      { ...projects[0], id: "repo-only", name: "Repo Only", description: "" },
      { ...projects[0], id: "blank", name: "Blank", description: "   " },
      { ...projects[1], id: "empty", name: "Empty", description: "" },
    ];
    render(<ProjectSwitcher projects={mixed} value="" onValueChange={vi.fn()} />);

    const popup = await openPanel(user, "切换项目：请选择项目");
    // 有描述时即使关联了仓库也显示描述
    const withDesc = within(popup).getByRole("option", { name: /With Desc/ });
    expect(within(withDesc).getByText("有描述")).toBeInTheDocument();
    expect(
      within(withDesc).queryByText("Bobo/fast_ship"),
    ).not.toBeInTheDocument();
    // 无描述或仅空白字符时回退到仓库 slug
    for (const name of [/Repo Only/, /Blank/]) {
      const row = within(popup).getByRole("option", { name });
      expect(within(row).getByText("Bobo/fast_ship")).toBeInTheDocument();
    }
    // 描述与仓库都缺省时副标题省略，行内只剩 4 个计数点 title
    const empty = within(popup).getByRole("option", { name: /Empty/ });
    expect(
      [...empty.querySelectorAll("[title]")].map((el) =>
        el.getAttribute("title"),
      ),
    ).toEqual(["未设置 0", "待处理 5", "开发中 0", "已完成 1"]);
  });

  it("直选 chip 只标到前 9 条可见行", async () => {
    const user = userEvent.setup();
    const many: Project[] = Array.from({ length: 11 }, (_, index) => ({
      ...projects[0],
      id: `p-${index + 1}`,
      name: `Project ${index + 1}`,
      issue_workflow_counts: undefined,
    }));
    render(<ProjectSwitcher projects={many} value="" onValueChange={vi.fn()} />);

    const popup = await openPanel(user, "切换项目：请选择项目");
    const options = within(listboxOf(popup)).getAllByRole("option");
    expect(options).toHaveLength(11);
    options.slice(0, 9).forEach((option, index) => {
      expect(
        within(option).getByText(new RegExp(`^(⌘|Ctrl\\+)${index + 1}$`)),
      ).toBeInTheDocument();
    });
    for (const option of options.slice(9)) {
      expect(
        within(option).queryByText(/(⌘|Ctrl\+)\d+/),
      ).not.toBeInTheDocument();
    }
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

    const popup = await openPanel(user);
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), query);
    const options = within(listboxOf(popup)).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent(matchedName);
    expect(options[0]).not.toHaveTextContent(excludedName);
  });

  it("搜索 / 只命中真实仓库 slug，不会匹配缺仓库的项目", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    const popup = await openPanel(user);
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "/");
    const options = within(listboxOf(popup)).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Fast Ship");
  });

  it("过滤无结果显示无匹配项目", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    const popup = await openPanel(user);
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "不存在");
    expect(screen.getByText("无匹配项目")).toBeInTheDocument();
    expect(within(popup).queryByRole("option")).not.toBeInTheDocument();
  });

  it("↑/↓ 环绕移动高亮，Enter 选中高亮行", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    const popup = await openPanel(user);
    const options = within(listboxOf(popup)).getAllByRole("option");
    expect(options[0]).toHaveAttribute("data-highlighted", "true");

    // 首行向上环绕到末行
    await user.keyboard("{ArrowUp}");
    expect(options[1]).toHaveAttribute("data-highlighted", "true");
    await user.keyboard("{ArrowDown}");
    expect(options[0]).toHaveAttribute("data-highlighted", "true");
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("notes");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("搜索后回车选中高亮的首个匹配项", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await openPanel(user);
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "notes");
    await user.keyboard("{Enter}");
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("notes");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("回车选中多个匹配项中计数最高者", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const matched: Project[] = [
      {
        ...projects[0],
        id: "low",
        name: "Match Low",
        issue_workflow_counts: { unset: 1, todo: 0, in_progress: 0, done: 0 },
      },
      {
        ...projects[1],
        id: "high",
        name: "Match High",
        issue_workflow_counts: { unset: 0, todo: 4, in_progress: 0, done: 0 },
      },
    ];
    render(
      <ProjectSwitcher projects={matched} value="" onValueChange={onValueChange} />,
    );

    await openPanel(user, "切换项目：请选择项目");
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "match");
    await user.keyboard("{Enter}");
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("high");
  });

  it("输入法组合中按回车不选中项目", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await openPanel(user);
    const input = screen.getByRole("textbox", { name: "搜索项目" });
    await user.type(input, "notes");
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    expect(onValueChange).not.toHaveBeenCalled();
    expect(
      screen.getByRole("dialog", { name: "切换项目" }),
    ).toBeInTheDocument();
  });

  it("长按 repeat 的按键不响应", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <ProjectSwitcher projects={projects} value="" onValueChange={onValueChange} />,
    );

    const popup = await openPanel(user, "切换项目：请选择项目");
    const options = within(listboxOf(popup)).getAllByRole("option");
    fireEvent.keyDown(window, { key: "ArrowDown", repeat: true });
    expect(options[0]).toHaveAttribute("data-highlighted", "true");
    fireEvent.keyDown(window, { key: "Enter", repeat: true });
    expect(onValueChange).not.toHaveBeenCalled();
  });

  it("⌘N 直选第 N 条可见行", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <ProjectSwitcher projects={projects} value="" onValueChange={onValueChange} />,
    );

    await openPanel(user, "切换项目：请选择项目");
    fireEvent.keyDown(window, { key: "2", metaKey: true });
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("notes");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("Ctrl+N 直选第 N 条可见行", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <ProjectSwitcher projects={projects} value="" onValueChange={onValueChange} />,
    );

    await openPanel(user, "切换项目：请选择项目");
    fireEvent.keyDown(window, { key: "1", ctrlKey: true });
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("fast-ship");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("数字键超出可见行数时不动作，但仍拦截浏览器默认行为", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    await openPanel(user);
    // 目标行越界时也要 preventDefault，否则 Chromium 会把 ⌘1..8 当成切换标签页
    const event = fireEvent.keyDown(window, { key: "9", metaKey: true });
    expect(event).toBe(false);
    expect(onValueChange).not.toHaveBeenCalled();
    expect(
      screen.getByRole("dialog", { name: "切换项目" }),
    ).toBeInTheDocument();
  });

  it("点击当前项目行只关闭面板，不触发回调", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();

    const popup = await openPanel(user);
    await user.click(within(popup).getByRole("option", { name: /Fast Ship/ }));
    expect(onValueChange).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("点击其它项目行触发回调并关闭面板", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();
    const trigger = screen.getByRole("button", { name: "切换项目：Fast Ship" });

    await user.click(trigger);
    const popup = await screen.findByRole("dialog", { name: "切换项目" });
    await user.click(within(popup).getByRole("option", { name: /Notes/ }));
    expect(onValueChange).toHaveBeenCalledExactlyOnceWith("notes");
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("Esc 关闭面板且事件不外传，重开时重置搜索与高亮", async () => {
    const user = userEvent.setup();
    const { onValueChange } = renderSwitcher();
    const trigger = screen.getByRole("button", { name: "切换项目：Fast Ship" });

    await user.click(trigger);
    await user.type(screen.getByRole("textbox", { name: "搜索项目" }), "Notes");

    // Esc 在捕获阶段被拦截：外层（如包裹的 Dialog）不应收到这次按键
    const keySpy = vi.fn();
    window.addEventListener("keydown", keySpy);
    try {
      await user.keyboard("{Escape}");
      expect(keySpy).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", keySpy);
    }
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(onValueChange).not.toHaveBeenCalled();

    await user.click(trigger);
    expect(screen.getByRole("textbox", { name: "搜索项目" })).toHaveValue("");
    const options = within(
      screen.getByRole("listbox", { name: "项目列表" }),
    ).getAllByRole("option");
    expect(options).toHaveLength(2);
    expect(options[0]).toHaveAttribute("data-highlighted", "true");
  });

  it("点击外部关闭面板", async () => {
    const user = userEvent.setup();
    renderSwitcher();

    await openPanel(user);
    await user.click(document.body);
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
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
