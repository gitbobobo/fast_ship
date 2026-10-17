import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useModelFavoritesStore } from "@/lib/store/model-favorites-store";
import type {
  ModelPick,
  ProviderChoice,
  ResolvedModel,
} from "@/lib/threads-compose";
import { ModelMultiPicker, ModelPicker } from "./model-picker";

const PROVIDERS: ProviderChoice[] = [
  {
    instanceId: "codex",
    name: "Codex",
    models: [
      { slug: "gpt-5", name: "GPT-5", isDefault: true, descriptors: [] },
      {
        slug: "gpt-5-mini",
        name: "GPT-5 Mini",
        isDefault: false,
        descriptors: [],
      },
    ],
  },
  {
    instanceId: "claude",
    name: "Claude",
    models: [
      {
        slug: "opus-5",
        name: "Claude Opus 5",
        isDefault: true,
        descriptors: [],
      },
    ],
  },
];

function resolvedFor(instanceId: string, slug: string): ResolvedModel {
  const provider = PROVIDERS.find((p) => p.instanceId === instanceId);
  const model = provider?.models.find((m) => m.slug === slug);
  if (!provider || !model) throw new Error("bad fixture");
  return { provider, model, efforts: {} };
}

const pickOf = (instanceId: string, model: string): ModelPick => ({
  instanceId,
  model,
  efforts: {},
});

const trigger = () => screen.getByRole("button", { name: "模型" });

async function openPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(trigger());
  return await screen.findByRole("listbox");
}

describe("ModelPicker", () => {
  beforeEach(() => {
    useModelFavoritesStore.setState({ favorites: [] });
  });

  it("触发器显示选中模型名", () => {
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    expect(trigger()).toHaveTextContent("GPT-5");
  });

  it("无 resolved 模型显示「默认模型」，disabled 生效", () => {
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={{ efforts: {} }}
        onPick={vi.fn()}
        disabled
      />,
    );
    expect(trigger()).toHaveTextContent("默认模型");
    expect(trigger()).toBeDisabled();
  });

  it("打开面板默认列出当前 provider 的模型，行内带 provider 名", async () => {
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    const listbox = await openPanel(user);
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(2);
    expect(options[0]).toHaveTextContent("GPT-5");
    expect(options[0]).toHaveTextContent("Codex");
    expect(options[1]).toHaveTextContent("GPT-5 Mini");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options[1]).toHaveAttribute("aria-selected", "false");
  });

  it("有可用收藏时面板默认落在收藏 rail", async () => {
    useModelFavoritesStore.setState({ favorites: ["claude::opus-5"] });
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    const listbox = await openPanel(user);
    expect(screen.getByRole("button", { name: "收藏" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Claude Opus 5");
  });

  it("rail 切换到其他 provider 后列出其模型", async () => {
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    const listbox = await openPanel(user);
    await user.click(screen.getByRole("button", { name: "Claude" }));
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Claude Opus 5");
    expect(options[0]).toHaveTextContent("Claude");
  });

  it("搜索跨所有 provider 过滤", async () => {
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    const listbox = await openPanel(user);
    await user.type(screen.getByLabelText("搜索模型"), "claude");
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Claude Opus 5");
  });

  it("点行调用 onPick({instanceId, model, efforts:{}}) 并关闭面板", async () => {
    const onPick = vi.fn();
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={onPick}
      />,
    );
    const listbox = await openPanel(user);
    await user.click(
      within(listbox).getByRole("option", { name: /GPT-5 Mini/ }),
    );
    expect(onPick).toHaveBeenCalledWith({
      instanceId: "codex",
      model: "gpt-5-mini",
      efforts: {},
    });
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });

  it("收藏切换后模型出现在「收藏」rail", async () => {
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    let listbox = await openPanel(user);
    await user.click(
      screen.getByRole("button", { name: "收藏 GPT-5 Mini" }),
    );
    expect(useModelFavoritesStore.getState().favorites).toEqual([
      "codex::gpt-5-mini",
    ]);
    // 点收藏星不触发行选中也不关面板
    expect(within(listbox).getAllByRole("option")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "收藏" }));
    const options = within(listbox).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("GPT-5 Mini");
    // 再点一次取消收藏，收藏 rail 回到空态
    await user.click(
      screen.getByRole("button", { name: "取消收藏 GPT-5 Mini" }),
    );
    expect(screen.getByText("没有匹配的模型")).toBeInTheDocument();
    listbox = screen.getByRole("listbox");
    expect(within(listbox).queryAllByRole("option")).toHaveLength(0);
  });

  it("搜索无结果显示空态", async () => {
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
      />,
    );
    await openPanel(user);
    await user.type(screen.getByLabelText("搜索模型"), "不存在");
    expect(screen.getByText("没有匹配的模型")).toBeInTheDocument();
  });

  it("leadingOption 点击调用 onSelect 并关闭，不受搜索影响", async () => {
    const onSelect = vi.fn();
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
        leadingOption={{ label: "项目默认", selected: false, onSelect }}
      />,
    );
    // 未选中时触发器仍显示 resolved 模型名
    expect(trigger()).toHaveTextContent("GPT-5");
    const listbox = await openPanel(user);
    await user.type(screen.getByLabelText("搜索模型"), "zzz");
    const option = within(listbox).getByRole("option", { name: "项目默认" });
    expect(option).toBeInTheDocument();
    await user.click(option);
    expect(onSelect).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
  });

  it("leadingOption.selected 时触发器显示其 label", () => {
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={vi.fn()}
        leadingOption={{
          label: "项目默认",
          selected: true,
          onSelect: vi.fn(),
        }}
      />,
    );
    expect(trigger()).toHaveTextContent("项目默认");
  });

  it("⌘1 直选第一条可见行", async () => {
    const onPick = vi.fn();
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={onPick}
      />,
    );
    await openPanel(user);
    fireEvent.keyDown(window, { key: "1", metaKey: true });
    expect(onPick).toHaveBeenCalledWith({
      instanceId: "codex",
      model: "gpt-5",
      efforts: {},
    });
  });

  it("方向键移动高亮，Enter 选中", async () => {
    const onPick = vi.fn();
    const user = userEvent.setup();
    render(
      <ModelPicker
        providers={PROVIDERS}
        resolved={resolvedFor("codex", "gpt-5")}
        onPick={onPick}
      />,
    );
    await openPanel(user);
    fireEvent.keyDown(window, { key: "ArrowDown" });
    fireEvent.keyDown(window, { key: "Enter" });
    expect(onPick).toHaveBeenCalledWith({
      instanceId: "codex",
      model: "gpt-5-mini",
      efforts: {},
    });
  });
});

describe("ModelMultiPicker", () => {
  beforeEach(() => {
    useModelFavoritesStore.setState({ favorites: [] });
  });

  it("空选显示「选择模型」，多选显示前两个名字与 +N", () => {
    const { rerender } = render(
      <ModelMultiPicker providers={PROVIDERS} selected={[]} onToggle={vi.fn()} />,
    );
    expect(trigger()).toHaveTextContent("选择模型");
    rerender(
      <ModelMultiPicker
        providers={PROVIDERS}
        selected={[
          pickOf("codex", "gpt-5"),
          pickOf("codex", "gpt-5-mini"),
          pickOf("claude", "opus-5"),
        ]}
        onToggle={vi.fn()}
      />,
    );
    expect(trigger()).toHaveTextContent("GPT-5、GPT-5 Mini +1");
  });

  it("点行调用 onToggle 且面板不关闭，行有选中态", async () => {
    const onToggle = vi.fn();
    const user = userEvent.setup();
    render(
      <ModelMultiPicker
        providers={PROVIDERS}
        selected={[pickOf("codex", "gpt-5")]}
        onToggle={onToggle}
      />,
    );
    const listbox = await openPanel(user);
    const options = within(listbox).getAllByRole("option");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    await user.click(options[1]);
    expect(onToggle).toHaveBeenCalledWith({
      instanceId: "codex",
      model: "gpt-5-mini",
      efforts: {},
    });
    // 面板保持打开
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  it("多选不响应数字键直选", async () => {
    const onToggle = vi.fn();
    const user = userEvent.setup();
    render(
      <ModelMultiPicker
        providers={PROVIDERS}
        selected={[]}
        onToggle={onToggle}
      />,
    );
    await openPanel(user);
    fireEvent.keyDown(window, { key: "1", metaKey: true });
    fireEvent.keyDown(window, { key: "1", ctrlKey: true });
    expect(onToggle).not.toHaveBeenCalled();
  });
});
