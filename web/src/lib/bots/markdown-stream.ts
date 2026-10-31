// 流式正文里的未闭合围栏：模型边输出边渲染时，「``` 还没收到收尾」的代码块
// 会先被 CommonMark 当成延续到文末的代码块渲染，闭合符到达后再跳变回正文。
// 这里把「最后一个未闭合围栏起始行之后的内容」拆出来降级为纯文本，闭合后
// 由调用方自动升级回 Markdown 渲染。
// 扫描范围：行首围栏，以及 blockquote 容器内的围栏——逐行剥掉容器标记得到
// 「检测视图」，在视图上套用同一套围栏规则，并配对开/闭围栏的容器深度
//（顶层围栏不会被围栏内容里的 `> ``` ` 误闭合），切分偏移仍取原始行起点。
// 列表项缩进内的围栏不在扫描范围：其内容列取决于列表标记宽度，逐行扫描
// 给不出完整 CommonMark 语义，部分实现反而会引入新的误判，故保持已知边界。

export interface IncompleteFenceSplit {
  /** 已闭合（或不含围栏）的前缀，可安全交给 Markdown 渲染。 */
  complete: string;
  /** 最后一个未闭合围栏起始行起的原文；null 表示没有未闭合围栏。 */
  openTail: string | null;
}

// 围栏起始行：行首缩进 ≤3 空格，≥3 个反引号或波浪线；开围栏可带信息串，
// 但反引号围栏的信息串不得再含反引号（否则按 CommonMark 它不是围栏）。
const OPEN_RE = /^ {0,3}(`{3,}|~{3,})(.*)$/;

// blockquote 容器标记：≤3 空格缩进 + `>` + 可选一个空格；反复剥离即支持
// 嵌套（`> > ``` `、`>>``` `）。剥离结果只用于围栏判定，不改变切分位置。
const BLOCKQUOTE_MARKER_RE = /^ {0,3}> ?/;

/** 剥掉行首全部 blockquote 标记，返回用于围栏判定的「检测视图」与容器深度。 */
function stripBlockquoteMarkers(line: string): { view: string; depth: number } {
  let view = line;
  let depth = 0;
  for (;;) {
    const m = BLOCKQUOTE_MARKER_RE.exec(view);
    if (!m) return { view, depth };
    view = view.slice(m[0].length);
    depth += 1;
  }
}

/** 闭围栏判定：同一字符、长度 ≥ 开围栏、标记后只允许空白。 */
function closingRun(line: string, char: "`" | "~", minLen: number): boolean {
  let i = 0;
  while (i < line.length && line[i] === " " && i < 4) i += 1;
  if (i > 3) return false;
  let run = 0;
  while (line[i] === char) {
    run += 1;
    i += 1;
  }
  if (run < minLen) return false;
  return line.slice(i).trim() === "";
}

// 引用定义行（链接引用 `[x]: url`、GFM 脚注 `[^1]: note`）：CommonMark 允许
// 定义出现在文档任意位置，且对全文档生效——它是文档级作用域，把定义切到后面
// 的块里，前面的块单独解析时引用就成了裸文本（`See [x].` 不再成链接，脚注也
// 没有上标与脚注区）。≤3 空格缩进按 CommonMark 仍算定义行。
const REFERENCE_DEFINITION_RE = /^ {0,3}\[[^\]\n]+\]:/;

/** 全文是否含引用定义行；含则不能按块缓存（保守判断，代码围栏里的同名行也算）。 */
function hasReferenceDefinition(text: string): boolean {
  for (const rawLine of text.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (REFERENCE_DEFINITION_RE.test(line)) return true;
  }
  return false;
}

/** HTML 块的结束判定：行内出现结束序列即结束（CommonMark 类型 1–5）。 */
type HtmlBlockEnd = (view: string) => boolean;

/**
 * HTML 块类型 1–5 的起始行判定，返回「行内出现该序列即结束」的判定函数。
 * 这些块的内容是原始文本、不被空白行打断，所以块内的空白行不能当切分点
 * （切开会把块内文本重新解释成普通段落）。起始行自身已含结束序列时块在
 * 该行就结束，返回 null。类型 6/7（普通标签）以空白行结束，无需处理。
 */
function htmlBlockStart(view: string): HtmlBlockEnd | null {
  // 与围栏判定同口径：行首缩进 ≤3 空格才算块起始。
  let indent = 0;
  while (indent < 4 && view[indent] === " ") indent += 1;
  if (indent > 3) return null;
  const rest = view.slice(indent);

  const contains =
    (closer: string): HtmlBlockEnd =>
    (line) =>
      line.includes(closer);

  for (const [start, closer] of [
    [/^<!--/, "-->"],
    [/^<\?/, "?>"],
    [/^<!\[CDATA\[/, "]]>"],
    [/^<![A-Za-z]/, ">"],
  ] as const) {
    if (!start.test(rest)) continue;
    const ended = contains(closer);
    return ended(rest) ? null : ended;
  }

  // 原始文本标签：标签名后须是空白、`>` 或行尾，避免把 `<prelude` 当块。
  // 结束标签的匹配按 CommonMark 大小写不敏感。
  const raw = /^<(script|pre|style|textarea)(?:[\s>]|$)/i.exec(rest);
  if (raw) {
    const closer = `</${raw[1].toLowerCase()}>`;
    if (rest.toLowerCase().includes(closer)) return null;
    return (line) => line.toLowerCase().includes(closer);
  }
  return null;
}

export interface StableBlockSplit {
  /** 已可安全交给 Markdown 渲染并缓存的完整块前缀；随文本增长单调变长。 */
  stable: string;
  /** 尚未稳定的尾部：从最后一个安全切分点起（含该空白行）。 */
  tail: string;
  /** stable 按切分点切出的各个块，拼接即 stable；前缀块的文本随文本增长不变。 */
  segments: string[];
}

/**
 * 扫描出全部安全切分点（原文偏移，升序），splitStableBlocks 与块渲染共用。
 *
 * 切分点只取围栏外的空白行，且要求该空白行的下一行已收完、顶格、首字符
 * 不属于 `>` `-` `*` `+` `[`、也不是有序列表标记：这几类下一行意味着前后两段
 * 同属一个容器——`- a`/`1. a` 是同一个列表（拆开会出现两个 `<ul>`、有序
 * 编号重排），`> a` 是同一个引用块（拆开间距不同），`[x]: url` 是链接/脚注
 * 引用定义（切到前面会让引用失去定义、渲染成裸文本）——把它们排除后，顶格
 * 的普通文本/标题/新围栏在空白行后一定是语义独立的新块，切分后的 DOM 与
 * 整段解析完全一致。围栏开行首字符是 ` 或 ~，天然通过检查（新围栏本身
 * 就是独立块）。
 *
 * 只消费已换行完结的行：最后一行还在增长时，它是列表标记还是普通文本
 * （`1` 会不会长成 `1.`）尚未可知，等它收完再判定，切分点才单调只增。
 *
 * 两类「空白行也不切」的例外：围栏代码块与 HTML 块类型 1–5 内部的空白行
 * 不结束块，切开会改变块内文本的解释；空白行判定只认空格与制表符
 *（CommonMark 的空白行口径），NBSP 等 Unicode 空白算正文。
 */
function stableCutOffsets(text: string): number[] {
  let inFence: { char: "`" | "~"; len: number; depth: number } | null = null;
  // 进行中的 HTML 块结束判定；非 null 时块内空白行不是候选切分点。
  let htmlEnd: HtmlBlockEnd | null = null;
  // 最近一个位于块外（非围栏、非 HTML 块）的已完结空白行起点；连续空白行取最后一个。
  let lastBlank: number | null = null;
  const cuts: number[] = [];
  let offset = 0;

  const lines = text.split("\n");
  const last = lines.length - 1;
  for (let index = 0; index <= last; index += 1) {
    const rawLine = lines[index];
    // 数组末元素是末尾换行留下的空串时它已「完结」；否则最后一行仍在增长。
    const complete = index < last || text.endsWith("\n");
    if (!complete) break;
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    // 与 splitIncompleteFence 同一套围栏状态机：容器视图上判定开/闭，
    // blockquote 深度配对、反引号围栏信息串含反引号不算围栏。
    const { view, depth } = stripBlockquoteMarkers(line);
    if (inFence) {
      if (depth === inFence.depth && closingRun(view, inFence.char, inFence.len)) {
        inFence = null;
      }
    } else if (htmlEnd) {
      // HTML 块内：只等结束序列，围栏标记在这里是原始文本。
      if (htmlEnd(view)) htmlEnd = null;
    } else {
      const m = OPEN_RE.exec(view);
      if (m) {
        const marker = m[1];
        const info = m[2] ?? "";
        if (marker[0] === "~" || !info.includes("`")) {
          inFence = { char: marker[0] as "`" | "~", len: marker.length, depth };
        }
      } else {
        htmlEnd = htmlBlockStart(view);
      }
    }

    if (/^[ \t]*$/.test(line)) {
      // 围栏与 HTML 块内的空白行不是候选：块内内容是原样字节，不存在块边界。
      if (!inFence && !htmlEnd) lastBlank = offset;
    } else if (lastBlank !== null) {
      const ordered = /^(\d+)[.)]/.exec(line);
      if (
        line[0] !== " " &&
        line[0] !== "\t" &&
        line[0] !== ">" &&
        line[0] !== "-" &&
        line[0] !== "*" &&
        line[0] !== "+" &&
        // 引用定义行已由 splitStableBlocks 的全文退回兜住，这里保留同一排除，
        // 切分规则不依赖退回路径的实现细节。
        line[0] !== "[" &&
        !ordered
      ) {
        cuts.push(lastBlank);
      }
      // 该空白行的下一行已定格：无论是否安全，它都不再有翻案机会。
      lastBlank = null;
    }
    offset += rawLine.length + 1;
  }
  return cuts;
}

/**
 * 把流式全文切成「已完整的块前缀 stable」与「每次都要重解析的尾部 tail」，
 * 让流式渲染只需重解析正在生成的块（解析总量从二次降为线性）。`segments`
 * 是 stable 按切分点切出的块，供逐块渲染并按块跳过未变化的前缀。
 */
export function splitStableBlocks(text: string): StableBlockSplit {
  // 引用定义是文档级作用域（见 REFERENCE_DEFINITION_RE）：全文出现定义行就
  // 不分块，整段交给老路径。代价是这类回复失去增量优化，换来的是引用/脚注
  // 渲染正确——保守退回，不是白名单。
  if (hasReferenceDefinition(text)) {
    return { stable: "", segments: [], tail: text };
  }
  const cuts = stableCutOffsets(text);
  const stableEnd = cuts.length > 0 ? cuts[cuts.length - 1] : 0;
  const segments: string[] = [];
  let start = 0;
  for (const cut of cuts) {
    segments.push(text.slice(start, cut));
    start = cut;
  }
  return {
    stable: text.slice(0, stableEnd),
    tail: text.slice(stableEnd),
    segments,
  };
}

/**
 * 按 CommonMark 围栏规则扫描文本，返回未闭合围栏的切分点。
 * 完整闭合时 openTail 为 null；非流式场景不要调用——未闭合围栏在
 * CommonMark 里本来就是合法的「延续到文末的代码块」。
 */
export function splitIncompleteFence(text: string): IncompleteFenceSplit {
  let inFence: { char: "`" | "~"; len: number; depth: number } | null = null;
  let openLineStart = -1;
  let offset = 0;

  for (const rawLine of text.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    // 检测视图上判断开/闭围栏；openLineStart 始终记录原始文本里的行起点，
    // 容器前缀只参与判定、不影响切分位置。
    const { view, depth } = stripBlockquoteMarkers(line);
    if (inFence) {
      // 闭围栏要与开围栏处在同一容器深度；否则顶层围栏内容里的
      // `> ``` ` 行会把它误闭合。
      if (depth === inFence.depth && closingRun(view, inFence.char, inFence.len)) {
        inFence = null;
      }
    } else {
      const m = OPEN_RE.exec(view);
      if (m) {
        const marker = m[1];
        const info = m[2] ?? "";
        // 反引号开围栏的信息串含反引号时整行不是围栏（只是普通段落文本）。
        if (marker[0] === "~" || !info.includes("`")) {
          inFence = { char: marker[0] as "`" | "~", len: marker.length, depth };
          openLineStart = offset;
        }
      }
    }
    offset += rawLine.length + 1;
  }

  if (!inFence) return { complete: text, openTail: null };
  return {
    complete: text.slice(0, openLineStart),
    openTail: text.slice(openLineStart),
  };
}
