import {
  isValidElement,
  memo,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy } from "lucide-react";
import { cn, copyToClipboard } from "@/lib/utils";
import {
  splitIncompleteFence,
  splitStableBlocks,
} from "@/lib/bots/markdown-stream";

/** 从 code 元素的 children 里抽出纯文本（保留缩进与换行）。 */
function codeText(children: ReactNode): string {
  if (typeof children === "string") return children;
  if (typeof children === "number") return String(children);
  if (Array.isArray(children)) return children.map(codeText).join("");
  if (isValidElement<{ children?: ReactNode }>(children)) {
    return codeText(children.props.children);
  }
  return "";
}

/** 复制按钮：成功短暂显示「已复制」，失败保持原状不向用户抛错。 */
function CodeCopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    },
    [],
  );

  return (
    <button
      type="button"
      className="flex items-center gap-1 rounded px-1 py-0.5 text-muted-foreground transition-colors hover:bg-background hover:text-foreground"
      onClick={() => {
        void (async () => {
          try {
            await copyToClipboard(text);
            setCopied(true);
            if (timerRef.current) clearTimeout(timerRef.current);
            timerRef.current = setTimeout(() => setCopied(false), 1500);
          } catch {
            // 剪贴板不可用（权限/非安全上下文）时静默兜底，不打断阅读
          }
        })();
      }}
    >
      {copied ? (
        <>
          <Check className="h-3 w-3" /> 已复制
        </>
      ) : (
        <>
          <Copy className="h-3 w-3" /> 复制
        </>
      )}
    </button>
  );
}

/**
 * 围栏代码块：语言标签在左（取自 code 的 language-xxx class，无则显示
 * 「代码」），复制按钮在右。不做语法高亮，与仓库其它 Markdown 面一致。
 */
function BotCodeBlock({ children }: { children?: ReactNode }) {
  let lang = "";
  let raw = "";
  if (isValidElement<{ className?: string; children?: ReactNode }>(children)) {
    const m = /language-([\w+-]+)/.exec(children.props.className ?? "");
    lang = m?.[1] ?? "";
    raw = codeText(children.props.children);
  }
  return (
    <div className="overflow-hidden rounded-md border border-border">
      <div className="flex items-center justify-between bg-muted/60 px-3 py-1 text-xs">
        <span className="font-medium text-muted-foreground">{lang || "代码"}</span>
        <CodeCopyButton text={raw} />
      </div>
      {/* github-markdown-css 给 pre 自带外边距与圆角；头部栏贴边时去掉。 */}
      <pre style={{ margin: 0, borderTopLeftRadius: 0, borderTopRightRadius: 0 }}>
        {children}
      </pre>
    </div>
  );
}

// remark 配置提到模块级：所有 MarkdownBlock 共用同一组插件与组件映射，
// 插件与映射引用稳定，模块级组件才能被 memo 可靠跳过。
const REMARK_PLUGINS = [remarkGfm];
const DISALLOWED_ELEMENTS = ["img"];
const MARKDOWN_COMPONENTS: Components = {
  a: ({ node: _node, href, children, ...props }) => (
    <a
      {...props}
      href={href}
      target="_blank"
      rel="noopener noreferrer nofollow"
    >
      {children}
    </a>
  ),
  pre: ({ node: _node, children }) => <BotCodeBlock>{children}</BotCodeBlock>,
};

/**
 * 单块 Markdown 渲染（既有那套 ReactMarkdown 配置）。
 * memo 是关键（INT-176）：稳定块渲染后，后续帧即使重建它、只要 text
 * 字符串不变就被跳过，整棵子树不重渲染也不重解析。
 */
const MarkdownBlock = memo(function MarkdownBlock({ text }: { text: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={REMARK_PLUGINS}
      disallowedElements={DISALLOWED_ELEMENTS}
      components={MARKDOWN_COMPONENTS}
    >
      {text}
    </ReactMarkdown>
  );
});

/**
 * 空白判定：只有空格、制表符与换行算空白（CommonMark 的空白行口径）。
 * 不能用 String.trim：它把 NBSP 等 Unicode 空白也当空白，会让「只有 NBSP 的
 * 一行」被判空而整块不渲染，内容直接丢掉。
 */
function hasVisibleContent(text: string): boolean {
  return /[^ \t\r\n]/.test(text);
}

/**
 * 流式正文的块拆分（INT-176）：splitStableBlocks 把全文切成「已完整的块
 * 前缀 stable + 待定尾部 tail」，stable 再按切分点切成块，每块交给 memo 化
 * 的 MarkdownBlock——稳定块每帧重新声明但 text 不变，React 直接跳过，于是
 * 只重解析正在生成的 tail 与最后一块，整段回复的解析总量从二次降为线性。
 * 单帧的 Markdown 解析量只与当前块长度有关，但仍有一次对累计全文的逐行
 * 扫描（splitStableBlocks 内部只扫行与几个正则，不解析 Markdown）：那就是
 * 流式渲染剩下的全部全量成本，实测 27KB 回复按 40 字符一帧（671 帧）累计
 * 扫描约 28ms、均值 0.04ms/帧，不构成瓶颈。切分点只落在语义独立的块边界（见
 * splitStableBlocks 注释），因此分块渲染的 DOM 与整段解析一致，由
 * 等价性测试锁定。
 *
 * 只在流式时有意义：非流式正文一次解析就够，不必再扫描切分点（同一条消息
 * 每次重渲染都会调用到这里，跳过扫描省掉一次不必要的全文遍历）。
 */
function useStreamingBlocks(text: string, streaming: boolean) {
  return useMemo(
    () =>
      streaming
        ? splitStableBlocks(text)
        : { segments: [] as string[], tail: text },
    [text, streaming],
  );
}

/**
 * 机器人正文的 Markdown 渲染：GFM 子集（标题/列表/表格/代码/引用/链接/
 * 分隔线），不解析原始 HTML、不渲染图片、链接一律新窗口且不带 referrer。
 * streaming 时只重解析尾部（INT-176）：稳定块前缀按块 memo 跳过重解析，
 * 未闭合围栏仍按既有行为降级为纯文本展示，闭合后自动升级；分块渲染与
 * 整段解析的 DOM 由等价性测试锁定一致。非流式保持单次全量渲染。user 消息仍走纯文本。
 */
export const BotMarkdown = memo(function BotMarkdown({
  text,
  streaming = false,
  className,
}: {
  text: string;
  streaming?: boolean;
  className?: string;
}) {
  const { segments, tail } = useStreamingBlocks(text, streaming);
  const { complete, openTail } = streaming
    ? splitIncompleteFence(tail)
    : { complete: text, openTail: null };

  // 拼接输出：整段解析时相邻顶层块之间夹一个 "\n" 文本节点（mdast→hast
  // 的 wrap），分块后在这里补回，DOM 才与整段解析逐字节一致。不含可见内容
  // 的块（切分点正好落在开头时的白空块）在 mdast 里不产出任何子节点，因此
  // 既不渲染它、也不补它前面的 "\n"——否则会多出悬空换行。稳定块由 memo
  // 跳过，这里新建的只是外层数组与换行文本。非流式不走分块，单次全量渲染。
  // 每个元素都带 key：同一层数组里混用有 key/无 key 的元素会触发 React 的
  // key 警告。
  const rendered: ReactNode[] = [];
  if (streaming) {
    segments.forEach((segment, index) => {
      if (!hasVisibleContent(segment)) return;
      if (rendered.length > 0) rendered.push("\n");
      // key 用 segment 下标：同一块的位置在增长过程中不变，memo 才能命中。
      rendered.push(<MarkdownBlock key={index} text={segment} />);
    });
    if (hasVisibleContent(complete)) {
      if (rendered.length > 0) rendered.push("\n");
      rendered.push(<MarkdownBlock key="tail" text={complete} />);
    }
  } else {
    rendered.push(<MarkdownBlock key="whole" text={complete} />);
  }
  if (openTail !== null) {
    rendered.push(
      <div key="open-tail" className="whitespace-pre-wrap">
        {openTail}
      </div>,
    );
  }

  return (
    <div
      className={cn(
        "markdown-body break-words text-sm [&_pre]:overflow-x-auto [&_table]:block [&_table]:overflow-x-auto",
        className,
      )}
    >
      {rendered}
    </div>
  );
});
