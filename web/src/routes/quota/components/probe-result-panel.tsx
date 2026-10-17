import { errorText } from "@/lib/quota";

/**
 * /api/quota/test 探测结果面板：成功显示套餐名与窗口数；
 * 失败优先显示服务端返回的用户可读原因（其次是请求层异常），
 * 有 detail 诊断（HTTP 状态码等）时一并展示。
 * 添加/编辑对话框与卡片测试对话框共用。
 */
export function ProbeResultPanel({
  result,
  error,
  isError,
}: {
  /** 探测接口返回的 data；请求失败或尚未探测时为 undefined */
  result: QuotaProbe | undefined;
  /** mutation 的请求层异常 */
  error: unknown;
  isError: boolean;
}) {
  if (!result && !isError) return null;

  return (
    <div
      data-testid="quota-probe-result"
      className={
        result?.status === "ok"
          ? "rounded-md border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-800 dark:text-emerald-300"
          : "rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
      }
    >
      {result?.status === "ok" ? (
        <>
          <p className="font-medium">凭据有效</p>
          <p className="mt-0.5">
            {result.plan_name
              ? `套餐：${result.plan_name}`
              : "已连通，但未返回套餐名"}
            {result.windows.length > 0 &&
              ` · ${result.windows.length} 个额度窗口`}
          </p>
        </>
      ) : (
        <>
          <p className="font-medium">测试失败</p>
          <p className="mt-0.5">
            {result?.error || errorText(error, "网络或服务异常")}
          </p>
          {result?.detail && (
            <p className="mt-0.5 opacity-80">{result.detail}</p>
          )}
        </>
      )}
    </div>
  );
}
