import { forwardRef } from "react";
import { Link } from "react-router";
import { Unlink } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useEnvironments } from "@/lib/hooks/use-threads";
import {
  useBindT3Project,
  useProjectT3Bindings,
  useT3EnvironmentProjects,
  useUnbindT3Project,
} from "@/lib/hooks/use-t3-bindings";
import { bindingInvalidReason } from "@/lib/issue-launch";
import { actionErrorText, ENVIRONMENT_STATUS_LABELS } from "@/lib/threads";
import { cn } from "@/lib/utils";

const NONE_VALUE = "__none__";

/**
 * 项目设置里的「T3 绑定」节：每个环境一行，下拉选择该环境已发现的
 * T3 项目即写入绑定；已绑定行可解绑。绑定快照与最新发现不一致、
 * 或环境不可达时标红提示重新绑定。
 */
export const T3BindingsSection = forwardRef<
  HTMLElement,
  { projectId: string }
>(function T3BindingsSection({ projectId }, ref) {
  const environmentsQuery = useEnvironments();
  const discoveryQuery = useT3EnvironmentProjects();
  const bindingsQuery = useProjectT3Bindings(projectId);
  const bind = useBindT3Project(projectId);
  const unbind = useUnbindT3Project(projectId);

  const environments = environmentsQuery.data ?? [];
  const discovery = discoveryQuery.data ?? [];
  const bindings = bindingsQuery.data ?? [];
  const busy = bind.isPending || unbind.isPending;

  const handleSelect = async (environmentId: string, next: string) => {
    try {
      if (next === NONE_VALUE) {
        await unbind.mutateAsync(environmentId);
        toast.success("已解绑");
      } else {
        await bind.mutateAsync({ environmentId, t3ProjectId: next });
        toast.success("已绑定");
      }
    } catch (error) {
      toast.error(actionErrorText(error, "操作失败，请重试"));
    }
  };

  return (
    <section ref={ref} aria-label="T3 绑定" className="space-y-2">
      <div>
        <h3 className="text-sm font-medium">T3 绑定</h3>
        <p className="text-xs text-muted-foreground">
          「启动线程」按这里的绑定把问题派发到对应环境的 T3 项目。
        </p>
      </div>

      {environmentsQuery.isLoading || discoveryQuery.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-9 rounded-lg" />
          <Skeleton className="h-9 rounded-lg" />
        </div>
      ) : environmentsQuery.isError ? (
        <p role="alert" className="text-xs text-destructive">
          环境列表加载失败
        </p>
      ) : environments.length === 0 ? (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">
          还没有连接 T3 环境，
          <Link
            to="/settings/environments"
            className="underline underline-offset-2 hover:text-foreground"
          >
            去设置配对
          </Link>
        </p>
      ) : (
        <div className="divide-y rounded-lg border">
          {discoveryQuery.isError && (
            <p role="alert" className="px-3 py-2 text-xs text-destructive">
              T3 项目发现失败，仅展示已保存的绑定
            </p>
          )}
          {environments.map((environment) => {
            const discovered = discovery.find(
              (entry) => entry.environment_id === environment.id,
            );
            const binding = bindings.find(
              (item) => item.environment_id === environment.id,
            );
            const invalidReason = binding
              ? bindingInvalidReason(binding, environment, discovered)
              : null;
            const selectDisabled =
              busy || environment.status !== "ok" || discoveryQuery.isError;
            // 失效绑定的旧项目 id 不在发现列表里，仍要作为一项渲染出来（标红）
            const options = discovered?.projects ?? [];
            const showStaleOption =
              binding &&
              !options.some((p) => p.id === binding.t3_project_id);

            return (
              <div
                key={environment.id}
                className="flex items-center gap-2 px-3 py-2"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm">
                    {environment.name}
                    <span className="ml-1.5 text-xs text-muted-foreground">
                      {ENVIRONMENT_STATUS_LABELS[environment.status]}
                      {environment.access === "read-only" ? " · 只读" : ""}
                    </span>
                  </p>
                  {invalidReason && (
                    <p role="alert" className="text-xs text-destructive">
                      绑定已失效：{invalidReason}
                    </p>
                  )}
                  {!invalidReason && discovered?.error && (
                    <p className="text-xs text-muted-foreground">
                      {discovered.error}
                    </p>
                  )}
                </div>
                <Select
                  value={binding?.t3_project_id ?? NONE_VALUE}
                  onValueChange={(value) =>
                    void handleSelect(environment.id, value ?? NONE_VALUE)
                  }
                  disabled={selectDisabled}
                >
                  <SelectTrigger
                    size="sm"
                    className={cn(
                      "w-44",
                      invalidReason && "border-destructive text-destructive",
                    )}
                    aria-label={`${environment.name} 的 T3 项目`}
                  >
                    <SelectValue>
                      {binding
                        ? (binding.t3_project_title ?? binding.t3_project_id)
                        : "不绑定"}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE_VALUE}>不绑定</SelectItem>
                    {showStaleOption && binding && (
                      <SelectItem value={binding.t3_project_id}>
                        {binding.t3_project_title ?? binding.t3_project_id}
                        （已失效）
                      </SelectItem>
                    )}
                    {options.map((project) => (
                      <SelectItem
                        key={project.id}
                        value={project.id}
                        disabled={!project.workspace_root}
                      >
                        {project.workspace_root
                          ? project.title
                          : `${project.title}（缺工作区路径，无法绑定）`}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {binding && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`解绑 ${environment.name}`}
                    title="解绑"
                    disabled={busy}
                    onClick={() =>
                      void handleSelect(environment.id, NONE_VALUE)
                    }
                  >
                    <Unlink className="h-4 w-4" />
                  </Button>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
});
