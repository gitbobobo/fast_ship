import { useQuery } from "@tanstack/react-query";
import { integrationOptionsApi } from "@/lib/api/forge-integrations";

// GET /api/integrations（INT-167）：项目表单的集成选项，全部登录用户可读。
// 数据是部署级投影（无凭证等用户私有字段），缓存不按登录用户隔离，也不与
// admin 卡片的增删联动；组件侧自行做加载/失败降级。
export function useIntegrationOptions() {
  return useQuery({
    queryKey: ["integration-options", "list"],
    queryFn: async () => {
      const res = await integrationOptionsApi.list();
      return res.data ?? [];
    },
  });
}
