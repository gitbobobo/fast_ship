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

/**
 * 只剥掉前 n 个 blockquote 标记，其余内容原样保留，返回剩下内容的视图与
 * 实际剥掉的深度。用于 HTML 块内按「块所在容器的深度」取块内容的判定视图：
 * 容器标记不属于块内容，但不能多剥（块内容里的裸 `>` 就是字节本身）。
 */
function stripNBlockquoteMarkers(
  line: string,
  n: number,
): { view: string; depth: number } {
  let view = line;
  let depth = 0;
  while (depth < n) {
    const m = BLOCKQUOTE_MARKER_RE.exec(view);
    if (!m) break;
    view = view.slice(m[0].length);
    depth += 1;
  }
  return { view, depth };
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
// 没有上标与脚注区）。

// 列表标记：`-` `*` `+`，或有序列表 `1.`/`1)`；标记后必须跟空白或行尾
// （CommonMark 的口径，`-x` 不是列表标记而是正文）。
const LIST_MARKER_RE = /^(?:[-*+]|\d{1,9}[.)])(?:[ \t]+|$)/;

/**
 * 剥掉行首的块容器前缀（行首空白、blockquote 标记、列表标记），返回容器内
 * 的行内容。块容器（引用块、列表项）里的链接/脚注定义按 CommonMark 同样对
 * 全文生效，所以要剥掉容器才认得出 `> [x]: url`、`- [x]: url`、`1. [x]: url`
 * 这类定义行；反复剥离以覆盖嵌套组合（`> > [x]: url`、`- > [x]: url`）。
 * 连行首空白也一并剥掉，于是顶层 4 空格缩进代码块里的同名行（`    [x]: url`）
 * 也会被当成定义：这是保守方向——多退回一次只损失增量优化，不算白名单。
 */
function stripContainerPrefixes(line: string): string {
  let rest = line;
  for (;;) {
    const next = rest
      .replace(/^[ \t]+/, "")
      .replace(BLOCKQUOTE_MARKER_RE, "")
      .replace(LIST_MARKER_RE, "");
    if (next === rest) return rest;
    rest = next;
  }
}

// 剥掉容器前缀之后，以 `[...]:` 开头就是定义行。正文里的 `see [x]: y` 这类
// 前缀不是空白/容器标记，剥不掉、也就不命中。
const REFERENCE_DEFINITION_RE = /^\[[^\]\n]+\]:/;

/**
 * 进行中的 HTML 块类型 1–5。块内容是原样文本（块内的 `>`、`-->` 是字节
 * 本身，不是容器标记），但块所在容器的 blockquote 标记不属于块内容：结束
 * 判定要按块的容器深度剥掉标记，再在剩下的内容里找结束序列。这样既不会被
 * 容器标记误判成结束序列（`> <!DOCTYPE html` 里的 `>`），也不会漏掉容器内
 * 真实的结束序列（`> </pre>`）。
 */
interface HtmlBlock {
  /** 结束序列。 */
  closer: string;
  /** 结束序列是否大小写不敏感（原始文本标签按 CommonMark 不敏感）。 */
  ignoreCase: boolean;
  /** 开块行的 blockquote 深度；块内各行剥掉同样多的标记后判定。 */
  depth: number;
}

/** 在「已按块的容器深度剥过标记」的内容视图里找结束序列。 */
function htmlBlockCloses(view: string, block: HtmlBlock): boolean {
  return block.ignoreCase
    ? view.toLowerCase().includes(block.closer)
    : view.includes(block.closer);
}

/**
 * HTML 块类型 1–5 的起始行判定，返回块状态。这些块的内容是原始文本、不被
 * 空白行打断，所以块内的空白行不能当切分点（切开会把块内文本重新解释成
 * 普通段落）。起始行自身已含结束序列时块在该行就结束，返回 null。类型 6/7
 *（普通标签）以空白行结束，无需处理。
 *
 * 判定用剥过 blockquote 标记的检测视图 `view`（`> <pre>` 这种在容器里开块
 * 的行也要认得出），起始行的自闭合判定同样看在视图上：原始行里的容器标记
 * 不属于块内容，拿原始行判会把 `> <!DOCTYPE html` 当场判成已闭合。
 */
function htmlBlockStart(view: string, depth: number): HtmlBlock | null {
  // 与围栏判定同口径：行首缩进 ≤3 空格才算块起始。
  let indent = 0;
  while (indent < 4 && view[indent] === " ") indent += 1;
  if (indent > 3) return null;
  const rest = view.slice(indent);

  for (const [start, closer] of [
    [/^<!--/, "-->"],
    [/^<\?/, "?>"],
    [/^<!\[CDATA\[/, "]]>"],
    [/^<![A-Za-z]/, ">"],
  ] as const) {
    if (!start.test(rest)) continue;
    const block: HtmlBlock = { closer, ignoreCase: false, depth };
    return htmlBlockCloses(view, block) ? null : block;
  }

  // 原始文本标签：标签名后须是空白、`>` 或行尾，避免把 `<prelude` 当块。
  // 结束标签的匹配按 CommonMark 大小写不敏感。
  const raw = /^<(script|pre|style|textarea)(?:[\s>]|$)/i.exec(rest);
  if (raw) {
    const block: HtmlBlock = {
      closer: `</${raw[1].toLowerCase()}>`,
      ignoreCase: true,
      depth,
    };
    return htmlBlockCloses(view, block) ? null : block;
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

/** 一次逐行扫描的结果；splitStableBlocks 只用这一份结果。 */
interface BlockScan {
  /** 安全切分点（原文偏移，升序）。 */
  cuts: number[];
  /** 全文是否出现引用定义行；为 true 时调用方整段退回，cuts 不再有意义。 */
  hasReferenceDefinition: boolean;
}

/**
 * 全文唯一一次逐行扫描：同时得出安全切分点与「是否出现引用定义行」。
 * 两者都在行维度上判定，合并成一次扫描即可，省掉一遍全文字符串数组分配
 * （每帧都要跑，这是流式渲染剩下的唯一全量成本）。
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
 * 引用定义行例外——它连正在增长的最后一行也要判（`[x]: http` 一出现就是
 * 定义，不必等行收完）。
 *
 * 两类「空白行也不切」的例外：围栏代码块与 HTML 块类型 1–5 内部的空白行
 * 不结束块，切开会改变块内文本的解释；空白行判定只认空格与制表符
 *（CommonMark 的空白行口径），NBSP 等 Unicode 空白算正文。
 */
function scanBlocks(text: string): BlockScan {
  let inFence: { char: "`" | "~"; len: number; depth: number } | null = null;
  // 进行中的 HTML 块；非 null 时块内空白行不是候选切分点。
  let htmlBlock: HtmlBlock | null = null;
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
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    // 定义行对全文生效（块容器里的定义也是文档级作用域，先剥容器前缀再
    // 判定），含正在增长的最后一行都要判；一旦命中，调用方必定整段退回，
    // 切分点已无意义，直接结束扫描。保守判断：代码围栏里的同名行也算定义
    //（不看围栏状态）。
    if (REFERENCE_DEFINITION_RE.test(stripContainerPrefixes(line))) {
      return { cuts: [], hasReferenceDefinition: true };
    }
    if (!complete) break;
    // 与 splitIncompleteFence 同一套围栏状态机：容器视图上判定开/闭，
    // blockquote 深度配对、反引号围栏信息串含反引号不算围栏。
    const { view, depth } = stripBlockquoteMarkers(line);
    if (inFence) {
      if (depth === inFence.depth && closingRun(view, inFence.char, inFence.len)) {
        inFence = null;
      }
    } else {
      // HTML 块内只等结束序列。块内容是原样文本，但要先按块的容器深度剥掉
      // blockquote 标记（容器标记不属于内容）；容器先结束则块也随之结束，
      // 这一行不属于该块，落回普通行判定。
      let closedHere = false;
      if (htmlBlock) {
        const inner = stripNBlockquoteMarkers(line, htmlBlock.depth);
        if (inner.depth < htmlBlock.depth) {
          htmlBlock = null;
        } else {
          closedHere = htmlBlockCloses(inner.view, htmlBlock);
          if (closedHere) htmlBlock = null;
        }
      }
      // 块在本行闭合时，本行不再充当围栏/HTML 块的起始（与既有行为一致）。
      if (!htmlBlock && !closedHere) {
        const m = OPEN_RE.exec(view);
        if (m) {
          const marker = m[1];
          const info = m[2] ?? "";
          if (marker[0] === "~" || !info.includes("`")) {
            inFence = {
              char: marker[0] as "`" | "~",
              len: marker.length,
              depth,
            };
          }
        } else {
          htmlBlock = htmlBlockStart(view, depth);
        }
      }
    }

    if (/^[ \t]*$/.test(line)) {
      // 围栏与 HTML 块内的空白行不是候选：块内内容是原样字节，不存在块边界。
      if (!inFence && !htmlBlock) lastBlank = offset;
    } else if (lastBlank !== null) {
      const ordered = /^(\d+)[.)]/.exec(line);
      if (
        line[0] !== " " &&
        line[0] !== "\t" &&
        line[0] !== ">" &&
        line[0] !== "-" &&
        line[0] !== "*" &&
        line[0] !== "+" &&
        // 引用定义行已由 splitStableBlocks 的全文退回兜住（含容器内的定义，
        // 见 stripContainerPrefixes），这里保留同一排除，切分规则不依赖退回
        // 路径的实现细节。
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
  return { cuts, hasReferenceDefinition: false };
}

/**
 * 把流式全文切成「已完整的块前缀 stable」与「每次都要重解析的尾部 tail」，
 * 让流式渲染只需重解析正在生成的块（解析总量从二次降为线性）。`segments`
 * 是 stable 按切分点切出的块，供逐块渲染并按块跳过未变化的前缀。
 *
 * 每帧仍对累计全文做一次逐行扫描（见 scanBlocks）：只有一次，且不解析
 * Markdown，只扫行与几个正则——这是剩下的唯一全量成本。
 */
export function splitStableBlocks(text: string): StableBlockSplit {
  // 引用定义是文档级作用域（见 REFERENCE_DEFINITION_RE）：全文出现定义行就
  // 不分块，整段交给老路径。代价是这类回复失去增量优化，换来的是引用/脚注
  // 渲染正确——保守退回，不是白名单。
  const { cuts, hasReferenceDefinition } = scanBlocks(text);
  if (hasReferenceDefinition) {
    return { stable: "", segments: [], tail: text };
  }
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
