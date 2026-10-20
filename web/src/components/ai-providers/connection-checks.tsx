/**
 * 「测试连接」逐项检查结果列表：AI 提供商设置页与机器人表单共用。
 * 两处的 check 类型同构（{name, passed, detail?}），渲染只认这个形状。
 */
export interface ConnectionCheckItem {
  name: string;
  passed: boolean;
  /** 失败原因（截断；不含密钥），通过时服务端省略。 */
  detail?: string;
}

function CheckRow({ check }: { check: ConnectionCheckItem }) {
  return (
    <li className="flex items-start gap-2 text-sm">
      <span
        className={
          check.passed
            ? "text-emerald-600 dark:text-emerald-400"
            : "text-destructive"
        }
        aria-label={check.passed ? "通过" : "未通过"}
      >
        {check.passed ? "✓" : "✕"}
      </span>
      <span>
        {check.name}
        {check.detail && (
          <span className="block text-xs text-muted-foreground">
            {check.detail}
          </span>
        )}
      </span>
    </li>
  );
}

/**
 * 调用方负责「列表与被测目标绑定」：目标变了就不要再把这份结果
 * 渲染进来（各表单按自己的 targetKey 口径判断）。
 */
export function ConnectionChecks({
  checks,
  "data-testid": testId,
}: {
  checks: ConnectionCheckItem[];
  "data-testid"?: string;
}) {
  return (
    <ul
      data-testid={testId}
      className="space-y-1.5 rounded-lg border bg-muted/30 px-3 py-2.5"
    >
      {checks.map((check) => (
        <CheckRow key={check.name} check={check} />
      ))}
    </ul>
  );
}
