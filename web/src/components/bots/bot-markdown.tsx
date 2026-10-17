import {
  isValidElement,
  memo,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy } from "lucide-react";
import { cn, copyToClipboard } from "@/lib/utils";
import { splitIncompleteFence } from "@/lib/bots/markdown-stream";

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

/**
 * 机器人正文的 Markdown 渲染：GFM 子集（标题/列表/表格/代码/引用/链接/
 * 分隔线），不解析原始 HTML、不渲染图片、链接一律新窗口且不带 referrer。
 * streaming 时若存在未闭合围栏，把围栏起始行之后的内容降级为纯文本展示，
 * 避免代码块在流式过程中先出现再跳变；闭合后自动升级。user 消息仍走纯文本。
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
  const { complete, openTail } = streaming
    ? splitIncompleteFence(text)
    : { complete: text, openTail: null };

  return (
    <div
      className={cn(
        "markdown-body break-words text-sm [&_pre]:overflow-x-auto [&_table]:block [&_table]:overflow-x-auto",
        className,
      )}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        disallowedElements={["img"]}
        components={{
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
          pre: ({ node: _node, children }) => (
            <BotCodeBlock>{children}</BotCodeBlock>
          ),
        }}
      >
        {complete}
      </ReactMarkdown>
      {openTail !== null && (
        <div className="whitespace-pre-wrap">{openTail}</div>
      )}
    </div>
  );
});
