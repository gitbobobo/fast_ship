import { useQuery } from "@tanstack/react-query";
import { dashboardApi } from "@/lib/api/dashboard";

export function useDashboardOverview() {
  return useQuery({
    queryKey: ["dashboard", "overview"],
    queryFn: async () => {
      const res = await dashboardApi.overview();
      return res.data;
    },
  });
}
