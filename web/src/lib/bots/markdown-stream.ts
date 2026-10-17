// 流式正文里的未闭合围栏：模型边输出边渲染时，「``` 还没收到收尾」的代码块
// 会先被 CommonMark 当成延续到文末的代码块渲染，闭合符到达后再跳变回正文。
// 这里把「最后一个未闭合围栏起始行之后的内容」拆出来降级为纯文本，闭合后
// 由调用方自动升级回 Markdown 渲染。

export interface IncompleteFenceSplit {
  /** 已闭合（或不含围栏）的前缀，可安全交给 Markdown 渲染。 */
  complete: string;
  /** 最后一个未闭合围栏起始行起的原文；null 表示没有未闭合围栏。 */
  openTail: string | null;
}

// 围栏起始行：行首缩进 ≤3 空格，≥3 个反引号或波浪线；开围栏可带信息串，
// 但反引号围栏的信息串不得再含反引号（否则按 CommonMark 它不是围栏）。
const OPEN_RE = /^ {0,3}(`{3,}|~{3,})(.*)$/;

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
 * 按 CommonMark 围栏规则扫描文本，返回未闭合围栏的切分点。
 * 完整闭合时 openTail 为 null；非流式场景不要调用——未闭合围栏在
 * CommonMark 里本来就是合法的「延续到文末的代码块」。
 */
export function splitIncompleteFence(text: string): IncompleteFenceSplit {
  let inFence: { char: "`" | "~"; len: number } | null = null;
  let openLineStart = -1;
  let offset = 0;

  for (const rawLine of text.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (inFence) {
      if (closingRun(line, inFence.char, inFence.len)) inFence = null;
    } else {
      const m = OPEN_RE.exec(line);
      if (m) {
        const marker = m[1];
        const info = m[2] ?? "";
        // 反引号开围栏的信息串含反引号时整行不是围栏（只是普通段落文本）。
        if (marker[0] === "~" || !info.includes("`")) {
          inFence = { char: marker[0] as "`" | "~", len: marker.length };
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
