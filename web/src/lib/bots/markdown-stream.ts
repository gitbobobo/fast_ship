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
// 以 `[` 或 `<` 开头的行（引用/脚注定义、原始 HTML 块；任务列表项与行首链接行
// 例外，它们的 `]` 后不是 `:`，不可能是定义）、围栏开在列表项里的行、
// 缩进 1–3 的围栏行（可能开在列表项内容里，列表容器不建模）、以及含裸 `\r`
//（不跟 `\n` 的回车）的文本，一出现就整段退回、不分块（任务列表项 `- [ ] x`
// 是正文，例外）。手写扫描器一旦去精确识别这两类语法，就是在写第二套
// CommonMark 解释器：定义标签可以跨行（`[foo\nbar]: url`）、可以含转义右括号
//（`[a\]b]: url`），HTML 块有 7 种起始与各自的反结束规则，反例是开放的。
// 退回的代价只是这类回复失去增量优化，换来的是渲染结果与非流式完全一致。

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
// 缩进 1–3 的命中不当作围栏，而是整段退回（见 scanSource）。
const OPEN_RE = /^ {0,3}(`{3,}|~{3,})(.*)$/;

// blockquote 容器标记：≤3 空格缩进 + `>` + 可选一个空格；反复剥离即支持
// 嵌套（`> > ``` `、`>>``` `）。剥离结果只用于围栏判定，不改变切分位置。
const BLOCKQUOTE_MARKER_RE = /^ {0,3}> ?/;

// 列表标记：`-` `*` `+`，或有序列表 `1.`/`1)`；标记后必须跟空白或行尾
// （CommonMark 的口径，`-x` 不是列表标记而是正文）。
const LIST_MARKER_RE = /^(?:[-*+]|\d{1,9}[.)])(?:[ \t]+|$)/;

// 任务列表项：列表标记后跟 `[ ]`/`[x]`/`[X]`，且复选框后必须是空白或行尾。
// `- [x]: url` 的复选框后紧跟 `:`，那是引用定义而不是任务项，仍要退回。
const TASK_ITEM_RE = /^ {0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+\[[ xX]\](?:[ \t]|$)/;

// 还没收完的复选框前缀（`- [`、`- [x`、`- [ `）。退回判定对正在增长的最后一行
// 也生效：只认完整复选框的话，逐 token 流式时每一帧都会撞上一个中间态，整份
// 回复反复退回再恢复，`segments` 归零、稳定块被重建。因此只对**还没换行完结**
// 的行放行这个宽口：一行收完后仍不是完整任务项（`- [abc] task`、`- [ ]x`）就
// 照旧退回；`- [x` 后接 `y]: url` 这种跨行定义标签也在那里拦下。
const TASK_ITEM_PREFIX_RE =
  /^ {0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+\[[ xX]?\]?(?:[ \t]|$)/;

// 行首链接行：`[文本](url)`、`[文本][标]`、`[文本][]`（`]` 后允许空白再跟 `(`/`[`）。
// 它们不可能是链接引用/脚注定义——定义的 `]` 后面必然是 `:`，所以这类行可以
// 豁免退回，让常见的链接列表回复也保住增量优化。快捷引用（单独一行的 `[文本]`）
// 与跨行定义标签不匹配这里，照旧退回。
const LINK_LINE_RE = /^\[(?:\\.|[^\]\\\n])*\][ \t]*(?:\(|\[)/;

// 开在列表项里的围栏：`- ``` `、`1. ~~~`、`> 1. ``` `（判定跑在剥过
// blockquote 的视图上）。内容列取决于列表标记宽度，不建模，命中即整段退回。
const LIST_ITEM_FENCE_RE =
  /^ {0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+(?:`{3,}|~{3,})/;

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
 * 行首空白占多少列。空格记 1 列，制表符补到下一个 4 的倍数——CommonMark 的
 * tab stop 就是 4 列，`\t` 与 4 空格同义。只看 `view`（已剥掉 blockquote
 * 标记）的前导空白，与围栏判定同一口径。用来判定缩进代码块（≥4 列），也用
 * 来把缩进 1–3 的围栏行排除在围栏之外（>0 即整段退回，见 scanSource）。
 */
function indentColumns(view: string): number {
  let columns = 0;
  for (const ch of view) {
    if (ch === " ") columns += 1;
    else if (ch === "\t") columns += 4 - (columns % 4);
    else break;
  }
  return columns;
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
  // 标记后只认空格与制表符（CommonMark 的闭围栏口径）：`trim()` 会连 NBSP、
  // U+3000、U+2028、VT/FF、U+FEFF 一起剥掉，把 ```` ```\u00a0 ```` 误判成闭围栏。
  return /^[ \t]*$/.test(line.slice(i));
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

/**
 * 行首 `[` 是不是任务列表项（而非引用/脚注定义）。`complete` 为假表示这行还在
 * 增长、后面还会长字符：只有这种时候才用宽口接受没收完的复选框前缀，否则
 * `- [` 这类半成品会把定义放过去。
 */
function isTaskItem(view: string, complete: boolean): boolean {
  if (TASK_ITEM_RE.test(view)) return true;
  return !complete && TASK_ITEM_PREFIX_RE.test(view);
}

/** 一次逐行扫描的结果；切分点与围栏降级都只从这一份结果读。 */
interface SourceScan {
  /** 安全切分点（原文偏移，升序）；退回时无意义。 */
  cuts: number[];
  /**
   * 命中扫描器不建模的构造（行首 `[`/`<`、列表项内围栏、缩进 1–3 的围栏、
   * 裸 `\r`）：调用方整段退回，不再分块。
   */
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
 * 的行不再需要在这里排除：它已经在更早处触发整段退回（任务列表项除外，它
 * 以列表标记开头，本来就在排除之列）。
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
 * 空白算正文。缩进代码块不建模起止语义，只用「当前行是否在缩进代码块里」这
 * 一个布尔量保守地少切一刀：缩进按列数算（tab = 4 列，见 indentColumns），
 * 非空行按自身缩进列数重算，空白行**不重置**它——CommonMark 里空白行不结束
 * 缩进代码块，代码块「只有空格的行」（去掉缩进后仍有空格）是代码内容，切开
 * 会把这些空格剥出代码块。因此缩进代码块之后的空白行一律不作候选，直到出现
 * 缩进 <4 的非空行；代价是缩进块之后紧邻的那个空行也少切一刀，只损失一点
 * 增量优化。
 */
function scanSource(text: string): SourceScan {
  let inFence: { char: "`" | "~"; len: number; depth: number } | null = null;
  let fenceOpenStart: number | null = null;
  // 最近一个位于围栏外的已完结空白行起点；连续空白行取最后一个。
  let lastBlank: number | null = null;
  // 当前行是否处于缩进代码块（≥4 列缩进）内，用于让代码块里的空白行不进
  // 候选。不建模缩进代码块的起止语义，只保守地少切一刀（见函数注释）。
  let indentedCode = false;
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
    // 裸 `\r`（不跟 `\n`）在 CommonMark 里也算换行，但这里按 `\n` 切行，于是
    // `prefix\r<!--`、`Middle\r\r[x]: url`、`` ```\rcode `` 能把 HTML 块起点、
    // 引用定义与围栏藏在一行中间，绕过上面那些行首退回判定。等价于对全文做
    // `/\r(?!\n)/`：命中就整段退回，代价只是这类文本失去增量优化。
    if (line.includes("\r")) bail = true;
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
        // 列表项里的围栏不在扫描范围内（内容列取决于列表标记宽度），交给整段
        // 解析：`1. ```\n\t\n正文` 被切开会渲染成空代码块。
        if (LIST_ITEM_FENCE_RE.test(view)) bail = true;
        else if (head.startsWith("<")) bail = true;
        // 剥掉容器前缀后以 `[` 开头：引用/脚注定义、任务列表项，或行首链接行。
        // 后两种剥离后同样以 `[` 开头，却是常见回复形态（待办列表、链接列表），
        // 不该整段失去增量优化。定义的 `]` 后必是 `:`，所以任务项（`]` 后是空白/
        // 行尾）与链接行（`]` 后是 `(`/`[`）都不可能是定义；快捷引用（单独一行的
        // `[文本]`）与跨行定义标签仍走这里的退回。
        else if (
          head.startsWith("[") &&
          !isTaskItem(view, complete) &&
          !LINK_LINE_RE.test(head)
        ) {
          bail = true;
        }
      }
      const m = OPEN_RE.exec(view);
      if (m) {
        const marker = m[1];
        const info = m[2] ?? "";
        // 围栏只认剥掉 blockquote 标记后的顶格：缩进 1–3 的围栏行可能开在
        // 列表项内容里（`- 运行：\n  ```bash`），内容列取决于列表标记宽度，
        // 列表容器不建模，因此整段退回。代价是「顶层但缩进 2/3」的围栏也失去
        // 增量优化。
        if (indentColumns(view) > 0) bail = true;
        // 反引号开围栏的信息串含反引号时整行不是围栏（只是普通段落文本）。
        else if (marker[0] === "~" || !info.includes("`")) {
          inFence = { char: marker[0] as "`" | "~", len: marker.length, depth };
          fenceOpenStart = offset;
        }
      }
    }

    if (complete) {
      const blank = /^[ \t]*$/.test(line);
      // 缩进代码块只由非空行的自身缩进（列数）重算：空白行不结束缩进代码块，
      // 所以它保留上一行的结论，代码块内的空白行因此不会成为候选。
      if (!blank) indentedCode = indentColumns(view) >= 4;
      if (blank) {
        // 围栏内与缩进代码块内的空白行不是候选：块内内容是原样字节，不存在
        // 块边界（缩进块里那几行空格切出去就丢了）。
        if (!inFence && !indentedCode) lastBlank = offset;
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
