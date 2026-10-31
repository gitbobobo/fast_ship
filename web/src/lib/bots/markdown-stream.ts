// 流式正文的增量渲染：模型边输出边渲染时，每批 `text_delta` 都会重渲染整段
// 回复。这里把全文切成两部分——已完成的块前缀交给 memo 化的 MarkdownBlock
// 逐块渲染（稳定块不再重解析），只让仍在增长的尾部重走 Markdown 解析；另有
// 「未闭合围栏降级」：围栏还没收到收尾时，从围栏起始行起的原文按纯文本展示，
// 否则 CommonMark 会把还没写完的代码块当成延续到文末的代码块渲染，闭合符
// 到达后再跳变回正文。
//
// 切分点与「未闭合围栏起点」共用同一份逐行扫描：两套扫描器各自维护围栏状态
// 时，围栏与切分点会得出相反结论（INT-176 第三轮反例）。同一份状态机里，
// 围栏在容器深度下降时结束（引用块结束，里面的围栏也随之结束）。
//
// 覆盖范围刻意只到围栏、空白行、列表与引用这些必要构造：逐行剥掉容器标记后
// 以 `[` 或 `<` 开头的行（引用/脚注定义、原始 HTML 块）一出现就整段退回、不
// 分块。手写扫描器一旦去精确识别这两类语法，就是在写第二套 CommonMark 解释
// 器：定义标签可以跨行（`[foo\nbar]: url`）、可以含转义右括号
//（`[a\]b]: url`），HTML 块有 7 种起始与各自的反结束规则，反例是开放的。
// 退回的代价只是这类回复失去增量优化，换来的是渲染结果与非流式完全一致。

export interface IncompleteFenceSplit {
  /** 已闭合（或不含围栏）的前缀，可安全交给 Markdown 渲染。 */
  complete: string;
  /** 最后一个未闭合围栏起始行起的原文；null 表示没有未闭合围栏。 */
  openTail: string | null;
}

export interface StreamingMarkdownSplit {
  /** 已完成的块前缀，按安全切分点切开；拼接即稳定前缀，可从尾部之前缓存。 */
  segments: string[];
  /** 需要每帧重新解析的尾部 Markdown（未闭合围栏之前的部分）。 */
  tailMarkdown: string;
  /** 未闭合围栏起始行起的原文，降级为纯文本；null 表示没有未闭合围栏。 */
  tailPlain: string | null;
}

// 围栏起始行：行首缩进 ≤3 空格，≥3 个反引号或波浪线；开围栏可带信息串，
// 但反引号围栏的信息串不得再含反引号（否则按 CommonMark 它不是围栏）。
const OPEN_RE = /^ {0,3}(`{3,}|~{3,})(.*)$/;

// blockquote 容器标记：≤3 空格缩进 + `>` + 可选一个空格；反复剥离即支持
// 嵌套（`> > ``` `、`>>``` `）。剥离结果只用于围栏判定，不改变切分位置。
const BLOCKQUOTE_MARKER_RE = /^ {0,3}> ?/;

// 列表标记：`-` `*` `+`，或有序列表 `1.`/`1)`；标记后必须跟空白或行尾
// （CommonMark 的口径，`-x` 不是列表标记而是正文）。
const LIST_MARKER_RE = /^(?:[-*+]|\d{1,9}[.)])(?:[ \t]+|$)/;

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

/**
 * 剥掉行首的块容器前缀（行首空白、blockquote 标记、列表标记），返回容器内的
 * 行内容。引用/脚注定义与原始 HTML 在容器里同样生效（`> [x]: url`、
 * `- <div>`），所以要剥掉容器才认得出它们；反复剥离以覆盖嵌套组合
 *（`> > [x]: url`、`- > [x]: url`）。行首空白也一并剥掉，于是 4 空格缩进
 * 代码块里的同名行（`    [x]: url`）也会触发退回：保守方向——多退回一次只
 * 损失增量优化。
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

/** 一次逐行扫描的结果；切分点与围栏降级都只从这一份结果读。 */
interface SourceScan {
  /** 安全切分点（原文偏移，升序）；退回时无意义。 */
  cuts: number[];
  /** 围栏外出现行首 `[`/`<`：调用方整段退回，不再分块。 */
  bail: boolean;
  /** 最后一个未闭合围栏的起始偏移；null 表示没有。 */
  fenceOpenStart: number | null;
}

/**
 * 全文唯一一次逐行扫描，同时给出安全切分点、未闭合围栏起点与退回判定。
 * 三者都在行维度上判定，合并成一次扫描省掉多遍全文字符串数组分配（每帧都要
 * 跑，这是流式渲染剩下的唯一全量成本）。
 *
 * 切分点只取围栏外的空白行，且要求该空白行的下一行已收完、顶格、首字符
 * 不属于 `>` `-` `*` `+`、也不是有序列表标记：这几类下一行意味着前后两段同属
 * 一个容器——`- a`/`1. a` 是同一个列表（拆开会出现两个 `<ul>`、有序编号
 * 重排），`> a` 是同一个引用块（拆开间距不同）。顶格的普通文本/标题/新围栏
 * 在空白行后一定是语义独立的新块，切分后的 DOM 与整段解析一致。行首 `[`
 * 的行不再需要在这里排除：它已经在更早处触发整段退回。
 *
 * 只有已换行完结的行能产生切分点：最后一行还在增长时，它是列表标记还是普通
 * 文本（`1` 会不会长成 `1.`）尚未可知，等它收完再判定，切分点才单调只增。
 * 围栏状态、容器深度与退回判定则对每一行都推进，含正在增长的最后一行——文本
 * 以 `"\n```"` 结尾时围栏已经打开，降级不能漏掉这一行。
 *
 * 末尾换行不产生额外的行：`text.split("\n")` 会在末尾换行后多给一个空串，
 * 但 `"a\n"` 在 CommonMark 里只有一行，那个空串不是文档里的一行。让它进循环
 * 会以深度 0 把引用块里还没闭合的围栏误判成「容器已结束」，同一段文本的降级
 * 就随末尾换行在「代码块」与「纯文本」之间来回跳。
 *
 * 空白行判定只认空格与制表符（CommonMark 的空白行口径），NBSP 等 Unicode
 * 空白算正文。
 */
function scanSource(text: string): SourceScan {
  let inFence: { char: "`" | "~"; len: number; depth: number } | null = null;
  let fenceOpenStart: number | null = null;
  // 最近一个位于围栏外的已完结空白行起点；连续空白行取最后一个。
  let lastBlank: number | null = null;
  const cuts: number[] = [];
  let bail = false;
  let offset = 0;

  const lines = text.split("\n");
  // 末尾换行后的空串不是一行（见函数注释），先摘掉再逐行扫描。
  const endsWithNewline = text.endsWith("\n");
  if (endsWithNewline) lines.pop();
  for (let index = 0; index < lines.length; index += 1) {
    const rawLine = lines[index];
    // 除最后一行外都已换行完结；最后一行在文本以换行结尾时也已完结。
    const complete = endsWithNewline || index < lines.length - 1;
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    const { view, depth } = stripBlockquoteMarkers(line);

    // 容器深度下降即容器结束：引用块里的围栏随容器一起结束，后续行不属于该
    // 代码块。裸空行深度为 0，会关闭深度 >0 的围栏；行首 `>` 的空引用行深度
    // 不变，不关闭（引用还在继续）。
    if (inFence && inFence.depth > depth) inFence = null;

    if (inFence) {
      // 闭围栏要与开围栏处在同一容器深度；否则顶层围栏内容里的
      // `> ``` ` 行会把它误闭合。
      if (depth === inFence.depth && closingRun(view, inFence.char, inFence.len)) {
        inFence = null;
      }
    } else {
      // 退回判定只对围栏外的行：代码块里的 `<div>`、`[x]: url` 是代码字节
      // 本身，不该让整份回复失去增量优化——围栏状态因此必须准确，这也是上面
      // 深度规则的用处。
      if (!bail) {
        const head = stripContainerPrefixes(line);
        if (head.startsWith("[") || head.startsWith("<")) bail = true;
      }
      const m = OPEN_RE.exec(view);
      if (m) {
        const marker = m[1];
        const info = m[2] ?? "";
        // 反引号开围栏的信息串含反引号时整行不是围栏（只是普通段落文本）。
        if (marker[0] === "~" || !info.includes("`")) {
          inFence = { char: marker[0] as "`" | "~", len: marker.length, depth };
          fenceOpenStart = offset;
        }
      }
    }

    if (complete) {
      if (/^[ \t]*$/.test(line)) {
        // 围栏内的空白行不是候选：块内内容是原样字节，不存在块边界。
        if (!inFence) lastBlank = offset;
      } else if (lastBlank !== null) {
        const ordered = /^(\d+)[.)]/.exec(line);
        if (
          line[0] !== " " &&
          line[0] !== "\t" &&
          line[0] !== ">" &&
          line[0] !== "-" &&
          line[0] !== "*" &&
          line[0] !== "+" &&
          !ordered
        ) {
          cuts.push(lastBlank);
        }
        // 该空白行的下一行已定格：无论是否安全，它都不再有翻案机会。
        lastBlank = null;
      }
    }
    offset += rawLine.length + 1;
  }
  return { cuts, bail, fenceOpenStart: inFence ? fenceOpenStart : null };
}

/**
 * 把流式全文切成「已完成的块前缀」与「待重解析的尾部」，让流式渲染只需重解析
 * 正在生成的块（解析总量从二次降为线性）。`segments` 供逐块渲染并按块跳过
 * 未变化的前缀；`tailPlain` 是未闭合围栏的降级纯文本段。
 *
 * 每帧仍对累计全文做一次逐行扫描（见 scanSource）：只有一次，且不解析
 * Markdown，只扫行与几个正则——这是剩下的唯一全量成本。
 */
export function splitStreamingMarkdown(text: string): StreamingMarkdownSplit {
  const { cuts, bail, fenceOpenStart } = scanSource(text);
  // 退回：整段交给 Markdown 渲染，与非流式完全一致。退回路径不做围栏降级——
  // 降级只对「扫描器看懂了这份文本」的情况安全，而退回正是「没看懂」，此时
  // 降级反而会与整段解析不一致（`<style>\n```\nstill` 这类输入实测有差）。
  if (bail) return { segments: [], tailMarkdown: text, tailPlain: null };

  const stableEnd = cuts.length > 0 ? cuts[cuts.length - 1] : 0;
  const segments: string[] = [];
  let start = 0;
  for (const cut of cuts) {
    segments.push(text.slice(start, cut));
    start = cut;
  }
  // 开放围栏一定在最后一个切分点之后（围栏内的空白行不是切分点）；取 max 只是
  // 让「把同一段尾部渲染两遍」在逻辑上不可能发生。
  const plainStart = Math.max(stableEnd, fenceOpenStart ?? text.length);
  return {
    segments,
    tailMarkdown: text.slice(stableEnd, plainStart),
    tailPlain: plainStart < text.length ? text.slice(plainStart) : null,
  };
}

/**
 * 按 CommonMark 围栏规则扫描文本，返回未闭合围栏的切分点。实现是对
 * splitStreamingMarkdown 同一份扫描结果的薄包装，围栏状态没有第二套。
 * 完整闭合时 openTail 为 null；非流式场景不要用——未闭合围栏在 CommonMark
 * 里本来就是合法的「延续到文末的代码块」。
 */
export function splitIncompleteFence(text: string): IncompleteFenceSplit {
  const { fenceOpenStart } = scanSource(text);
  if (fenceOpenStart === null) return { complete: text, openTail: null };
  return {
    complete: text.slice(0, fenceOpenStart),
    openTail: text.slice(fenceOpenStart),
  };
}
