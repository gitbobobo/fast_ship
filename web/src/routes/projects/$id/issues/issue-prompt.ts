export interface IssuePromptComment {
  author: string;
  body: string;
}

export interface IssuePromptInput {
  id: string;
  title: string;
  body: string;
  labels: string[];
  comments: IssuePromptComment[];
}

function escapeXml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

export function buildIssuePrompt(input: IssuePromptInput): string {
  const labelsXml = input.labels.length
    ? `\n<labels>${escapeXml(input.labels.join(", "))}</labels>`
    : "";
  const commentsXml = input.comments.length
    ? `\n<comments>\n${input.comments
        .map(
          (comment) =>
            `<comment author="${escapeXml(comment.author)}">${escapeXml(comment.body)}</comment>`,
        )
        .join("\n")}\n</comments>`
    : "";

  return `There is a project issue on the \`Fast Ship\` platform that needs to be handled:

IMPORTANT RULES:
- Do NOT modify the issue status (open/closed) without explicit user permission.
- Do NOT commit or push code without explicit user permission.

HANDLING FLOW (follow in order):

1. Count the distinct problems in this issue. If it contains MULTIPLE UNRELATED
   problems, STOP and ask the user which one to handle this round, then proceed
   with only that one.
2. Classify the chosen problem into one of two types, based on its title, body,
   labels, and comments:
   - BUG: a defect — something broken or behaving incorrectly.
   - NOT A BUG: a feature request, question, documentation, refactor, or anything else.
3. If NOT A BUG: assess priority from implementation difficulty vs. benefit
   (High / Medium / Low with a one-line reason), and give a concrete implementation
   recommendation. DO NOT write or modify any code.
4. If BUG: when feasible, first add a failing test that reproduces the bug, then
   find the root cause, then fix it so the test passes. If the bug cannot be
   reproduced with a unit test (e.g. UI-only, environment, or config), briefly
   explain why, then find the root cause and fix it.

<id>${escapeXml(input.id)}</id>
<title>${escapeXml(input.title)}</title>${labelsXml}
<body>${escapeXml(input.body)}</body>${commentsXml}`;
}
