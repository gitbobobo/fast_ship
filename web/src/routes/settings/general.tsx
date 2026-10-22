import { Monitor, Sun, Moon, Globe } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { OptionSelect } from "@/components/ui/option-select";
import { SettingsPageShell } from "@/routes/settings/layout";
import { useThemeStore, type Theme } from "@/lib/store/theme-store";
import { useAuthStore } from "@/lib/store/auth-store";
import { authApi } from "@/lib/api/auth";
import {
  browserTimezone,
  formatTimeInZone,
  timezoneOptions,
} from "@/lib/utils/timezone";

const themes: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
  { value: "system", label: "跟随系统", icon: Monitor },
];

// 清空时区的入口：OptionSelect 里 "" 表示未选中，所以用哨兵值映射回空串。
const DEFAULT_TIMEZONE = "__deployment_default__";

export default function GeneralPage() {
  const { theme, setTheme } = useThemeStore();
  const user = useAuthStore((state) => state.user);
  const [now, setNow] = useState(() => new Date());
  const [savingTimezone, setSavingTimezone] = useState(false);

  // 展示该时区下的当前时间，30 秒刷新一次（用户看的就是机器人的「几点」）。
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 30_000);
    return () => clearInterval(timer);
  }, []);

  const currentTheme = themes.find((t) => t.value === theme);
  const Icon = currentTheme?.icon ?? Monitor;

  const currentTimezone = user?.timezone ?? "";
  // 未声明时区时按浏览器时区展示时间，同时说明机器人实际按部署配置执行。
  const displayTimezone = currentTimezone || browserTimezone() || "UTC";

  const timezoneSelectOptions = useMemo(
    () => [
      { value: DEFAULT_TIMEZONE, label: "默认（跟随部署配置）" },
      ...timezoneOptions(currentTimezone),
    ],
    [currentTimezone],
  );

  /** 选中即保存：空串回落到部署配置，其余为 IANA 名称。 */
  const handleTimezoneChange = async (value: string | null) => {
    const timezone = !value || value === DEFAULT_TIMEZONE ? "" : value;
    setSavingTimezone(true);
    try {
      const res = await authApi.updateMe({ timezone });
      useAuthStore.getState().setUser(res.data);
      toast.success(
        timezone ? "时区已更新" : "时区已改为跟随部署配置",
      );
    } catch {
      toast.error("时区更新失败");
    } finally {
      setSavingTimezone(false);
    }
  };

  return (
    <SettingsPageShell>
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">通用</h2>
          <p className="text-sm text-muted-foreground">管理应用的基本设置</p>
        </div>

        <div className="flex items-center justify-between rounded-lg border p-4">
          <div className="flex items-center gap-3">
            <Icon className="h-5 w-5 text-muted-foreground" />
            <div>
              <p className="font-medium">主题</p>
              <p className="text-sm text-muted-foreground">
                选择浅色、深色或跟随系统主题
              </p>
            </div>
          </div>
          <OptionSelect
            value={theme}
            onValueChange={(value) => setTheme(value as Theme)}
            options={themes.map((t) => ({
              value: t.value,
              label: t.label,
              itemLabel: (
                <div className="flex items-center gap-2">
                  <t.icon className="h-4 w-4" />
                  {t.label}
                </div>
              ),
            }))}
            placeholder="选择主题"
            className="w-32"
          />
        </div>

        <div className="rounded-lg border p-4">
          <div className="flex items-center justify-between gap-4">
            <div className="flex items-center gap-3">
              <Globe className="h-5 w-5 text-muted-foreground" />
              <div>
                <p className="font-medium">时区</p>
                <p className="text-sm text-muted-foreground">
                  机器人的定时任务与运行时间按此时区计算
                </p>
              </div>
            </div>
            <OptionSelect
              value={currentTimezone || DEFAULT_TIMEZONE}
              onValueChange={handleTimezoneChange}
              options={timezoneSelectOptions}
              placeholder="选择时区"
              aria-label="时区"
              disabled={savingTimezone}
              className="w-64"
            />
          </div>

          <div className="mt-3 space-y-1 pl-8 text-sm">
            <p className="text-muted-foreground">
              当前生效时区：
              <span className="text-foreground">
                {currentTimezone || "跟随部署配置"}
              </span>
              {!currentTimezone && (
                <span className="ml-2 text-xs">（未设置）</span>
              )}
            </p>
            <p className="text-muted-foreground">
              当前时间：
              <span className="text-foreground">
                {formatTimeInZone(now, displayTimezone)}
              </span>
              {!currentTimezone && (
                <span className="ml-2 text-xs">（按本机时区显示）</span>
              )}
            </p>
          </div>
        </div>
      </div>
    </SettingsPageShell>
  );
}
