import type { components } from "@/types/api.gen";
import { api, decodeApiError } from "./client";
import { THREAD_REQUEST_TIMEOUT_MS } from "./threads";

function options(signal?: AbortSignal) {
  return { signal, timeout: THREAD_REQUEST_TIMEOUT_MS, retry: 0 } as const;
}

/** 契约类型直接引用 api.gen.ts（服务端响应就是 oapi-codegen 的同名结构）。 */
type Schemas = components["schemas"];

/** GET /api/environments/t3-projects 的逐环境条目；单环境失败只标在该条目上。 */
export type T3EnvironmentProjects = Schemas["T3EnvironmentProjects"];

export type T3DiscoveredProject = Schemas["T3DiscoveredProject"];

/** GET /api/projects/:id/t3-bindings 的绑定记录（附环境名与实时探测状态）。 */
export type ProjectT3Binding = Schemas["ProjectT3Binding"];

const bindingsPath = (projectId: string) =>
  `projects/${encodeURIComponent(projectId)}/t3-bindings`;

function itemsOf<T>(data: { items?: T[] } | T[] | null | undefined): T[] {
  if (!data) return [];
  return Array.isArray(data) ? data : (data.items ?? []);
}

export const t3BindingApi = {
  /** 各环境已发现的 T3 项目；响应体宽容处理 {items} 与裸数组两种形态。 */
  environmentProjects: (signal?: AbortSignal) =>
    decodeApiError(
      api
        .get("environments/t3-projects", options(signal))
        .json<
          ApiResponse<{ items?: T3EnvironmentProjects[] } | T3EnvironmentProjects[]>
        >(),
    ).then((res) => itemsOf(res.data)),

  list: (projectId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(bindingsPath(projectId), options(signal))
        .json<
          ApiResponse<{ items?: ProjectT3Binding[] } | ProjectT3Binding[]>
        >(),
    ).then((res) => itemsOf(res.data)),

  /** PUT 由服务端实时校验（40401=T3 项目不存在，50202=环境不可达）后写快照。 */
  bind: (projectId: string, environmentId: string, t3ProjectId: string) =>
    decodeApiError(
      api
        .put(`${bindingsPath(projectId)}/${encodeURIComponent(environmentId)}`, {
          json: { t3_project_id: t3ProjectId },
          ...options(),
        })
        .json<ApiResponse<ProjectT3Binding | null>>(),
    ).then((res) => res.data),

  unbind: (projectId: string, environmentId: string) =>
    decodeApiError(
      api
        .delete(
          `${bindingsPath(projectId)}/${encodeURIComponent(environmentId)}`,
          options(),
        )
        .json<ApiResponse<null>>(),
    ).then(() => undefined),
};
