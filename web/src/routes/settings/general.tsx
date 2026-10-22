import { Monitor, Sun, Moon, Globe } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { OptionSelect } from "@/components/ui/option-select";
import { SettingsPageShell } from "@/routes/settings/layout";
import { useThemeStore, type Theme } from "@/lib/store/theme-store";
import { useAuthStore } from "@/lib/store/auth-store";
import { authApi } from "@/lib/api/auth";
import {
  formatTimeInZone,
  TIMEZONE_GROUP_COMMON,
  TIMEZONE_GROUP_DIVIDER,
  TIMEZONE_GROUP_SEPARATOR,
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
  // 生效时区由服务端算好（用户设置 → 部署配置 → UTC）。没有它时不要用本机
  // 时区冒充「部署配置的时区」——本机与部署不一致时名字和时间都是错的
  // （INT-138 复审 P1）。
  const effectiveTimezone = user?.effective_timezone ?? "";
  const displayTimezone = currentTimezone || effectiveTimezone;

  // 长期开着的标签页可能停在「user 里还没有 effective_timezone」的旧对象上
  // （布局只在 user 为空时拉 /auth/me）：进这个页面时补拉一次写回 store。
  useEffect(() => {
    if (!user || user.effective_timezone !== undefined) return;
    let cancelled = false;
    authApi
      .me()
      .then((res) => {
        if (!cancelled) useAuthStore.getState().setUser(res.data);
      })
      .catch(() => {
        // 后台补齐失败不影响页面：下面按「字段缺失」的保守分支显示。
      });
    return () => {
      cancelled = true;
    };
  }, [user]);

  const timezoneSelectOptions = useMemo(() => {
    // 分组标题不可选：只用来把「常用」和完整列表分开。
    const groupHeader = (label: string) => ({
      value: label,
      label,
      disabled: true,
      // 分组标题不可选，但不该因此变淡：SelectItem 的 disabled 会叠 50% 透明度，
      // 抵消掉再用与卡片次要文字同档的 muted-foreground（≈4.7:1）。
      className: "opacity-100 data-disabled:opacity-100",
      itemLabel: (
        <span className="text-xs font-medium text-muted-foreground">
          {label}
        </span>
      ),
    });
    const grouped = timezoneOptions(currentTimezone).map((option) => {
      if (option.value === TIMEZONE_GROUP_COMMON) {
        return { ...groupHeader("常用时区"), value: TIMEZONE_GROUP_COMMON };
      }
      if (option.value === TIMEZONE_GROUP_SEPARATOR) {
        return { ...groupHeader("全部时区"), value: TIMEZONE_GROUP_SEPARATOR };
      }
      return option;
    });
    return [
      { value: DEFAULT_TIMEZONE, label: "默认（跟随部署配置）" },
      // 分隔线：默认项会清空设置、性质与具体时区不同，单独成一组。
      {
        value: TIMEZONE_GROUP_DIVIDER,
        label: "—",
        disabled: true,
        className: "opacity-100 data-disabled:opacity-100 py-0",
        itemLabel: <span className="-mx-2 block h-px bg-border" />,
      },
      ...grouped,
    ];
  }, [currentTimezone]);

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
          {/* 图标一列；右侧再分「文字列 + 下拉」：标题、说明、信息行都在同一个
              文字列里，右边界受同一约束，不会伸到下拉框下方。 */}
          <div className="flex items-start gap-3">
            <Globe className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
            <div className="flex w-full items-start justify-between gap-4">
              <div className="min-w-0 flex-1 space-y-3">
                <div>
                  <p className="font-medium">时区</p>
                  <p className="text-sm text-muted-foreground">
                    机器人理解时间、新建定时任务和夜间整理都按此时区
                  </p>
                </div>

                <div className="space-y-1 text-sm">
                  {/* 显式选了时区时，下拉就是事实来源，这里只补一个该时区的当前
                      时间；跟随部署配置时前端拿不到部署配置的时区，就只说明生效
                      规则（未配置时 UTC 是后端回退链的终点），不摆一个会被误读成
                      「机器人用的时间」的本机时钟。 */}
                  {currentTimezone ? (
                    <p className="text-muted-foreground">
                      该时区当前时间：
                      <span className="text-foreground">
                        {formatTimeInZone(now, displayTimezone)}
                      </span>
                    </p>
                  ) : displayTimezone ? (
                    <>
                      <p className="text-muted-foreground">
                        当前生效时区：
                        <span className="text-foreground">
                          {displayTimezone}（来自部署配置）
                        </span>
                      </p>
                      <p className="text-muted-foreground">
                        该时区当前时间：
                        <span className="text-foreground">
                          {formatTimeInZone(now, displayTimezone)}
                        </span>
                      </p>
                    </>
                  ) : (
                    // 生效时区还没拿到：只说规则，不拿本机时区冒充。
                    <p className="text-muted-foreground">
                      机器人按部署配置的时区计算，部署未配置时区时为 UTC
                    </p>
                  )}
                </div>
              </div>

              <OptionSelect
                value={currentTimezone || DEFAULT_TIMEZONE}
                onValueChange={handleTimezoneChange}
                options={timezoneSelectOptions}
                placeholder="选择时区"
                aria-label="时区"
                disabled={savingTimezone}
                className="w-48 shrink-0"
                // 触发器保持窄（让左侧说明一行放得下），面板单独放宽：
                // 「America/Argentina/Buenos_Aires (UTC-3)」这类长名连同偏移量
                // 一起显示，不把关键的 (UTC±N) 截掉。
                contentClassName="min-w-80 max-w-96"
              />
            </div>
          </div>
        </div>
      </div>
    </SettingsPageShell>
  );
}
