import { useState } from "react";
import { Loader2, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ApiRequestError } from "@/lib/api/client";
import type { Bot, BotConnectionCheck } from "@/lib/api/bots";
import { CONTEXT_SOURCE_LABELS } from "./bot-metrics";
import {
  useCreateBot,
  useTestBotConnection,
  useUpdateBot,
} from "@/lib/hooks/use-bots";

interface BotFormState {
  name: string;
  persona_prompt: string;
  base_url: string;
  model: string;
  api_key: string;
}

const emptyForm: BotFormState = {
  name: "",
  persona_prompt: "",
  base_url: "",
  model: "",
  api_key: "",
};

function botToForm(bot: Bot): BotFormState {
  // key 永不回填：编辑时留空表示保留已存密钥。
  return {
    name: bot.name,
    persona_prompt: bot.persona_prompt,
    base_url: bot.base_url,
    model: bot.model,
    api_key: "",
  };
}

function errorText(err: unknown, fallback: string) {
  return err instanceof ApiRequestError && err.message ? err.message : fallback;
}

/** 一次「测试连接」随行的模型能力，连同它当时测试的目标一起记录：
 * 目标再变，这份能力就不再代表当前表单。 */
interface TestedCapability {
  base_url: string;
  model: string;
  context_window?: number;
  context_source?: string;
  context_detail?: string;
}

/** 地址比较与缓存键同一口径：TrimSpace + 去尾部 /。 */
function normalizeURL(url: string) {
  return url.trim().replace(/\/+$/, "");
}

function CheckResult({ check }: { check: BotConnectionCheck }) {
  return (
    <li className="flex items-start gap-2 text-sm">
      <span
        className={check.passed ? "text-emerald-600 dark:text-emerald-400" : "text-destructive"}
        aria-label={check.passed ? "通过" : "未通过"}
      >
        {check.passed ? "✓" : "✕"}
      </span>
      <span>
        {check.name}
        {check.detail && (
          <span className="block text-xs text-muted-foreground">{check.detail}</span>
        )}
      </span>
    </li>
  );
}

/**
 * 创建/编辑机器人：人设、OpenAI 兼容地址、模型与 API Key。
 * key 不回填；编辑时空值表示保留，已配置状态只展示末四位。
 * 「测试连接」可测未保存配置，也可基于已存 key 覆盖表单的地址/模型。
 * 窗口能力展示跟随当前表单目标：目标与已存值一致显示已存能力，
 * 「测试连接」成功后显示本次测试返回的能力，目标变了即隐藏旧展示。
 * 关闭即卸载：未保存的表单与测试结果不跨开关保留。
 */
export function BotFormDialog({
  open,
  editing,
  onClose,
  onSaved,
}: {
  open: boolean;
  editing: Bot | null;
  onClose: () => void;
  /** 创建/编辑成功后回调（父组件负责跳转或刷新提示）。 */
  onSaved?: (bot: Bot) => void;
}) {
  if (!open) return null;
  return (
    <BotFormDialogInner
      key={editing?.id ?? "new"}
      editing={editing}
      onClose={onClose}
      onSaved={onSaved}
    />
  );
}

function BotFormDialogInner({
  editing,
  onClose,
  onSaved,
}: {
  editing: Bot | null;
  onClose: () => void;
  onSaved?: (bot: Bot) => void;
}) {
  const create = useCreateBot();
  const update = useUpdateBot(editing?.id ?? "");
  const testConnection = useTestBotConnection();
  const [form, setForm] = useState<BotFormState>(() =>
    editing ? botToForm(editing) : emptyForm,
  );
  const [checks, setChecks] = useState<BotConnectionCheck[] | null>(null);
  const [tested, setTested] = useState<TestedCapability | null>(null);

  const patch = (next: Partial<BotFormState>) =>
    setForm((previous) => ({ ...previous, ...next }));

  const busy = create.isPending || update.isPending;
  const testing = testConnection.isPending;

  // 能力展示只跟随当前表单目标（F8）：测试结果属于它测试时的地址+模型，
  // 已存能力属于已保存的地址+模型；目标变了就不再展示旧值。
  const formURL = normalizeURL(form.base_url);
  const formModel = form.model.trim();
  let shownCapability: TestedCapability | null = null;
  if (
    tested &&
    normalizeURL(tested.base_url) === formURL &&
    tested.model === formModel
  ) {
    shownCapability = tested;
  } else if (
    editing &&
    (editing.context_window ?? 0) > 0 &&
    normalizeURL(editing.base_url) === formURL &&
    editing.model.trim() === formModel
  ) {
    shownCapability = {
      base_url: editing.base_url,
      model: editing.model,
      context_window: editing.context_window,
      context_source: editing.context_source,
      context_detail: editing.context_detail,
    };
  }

  const validate = (): string | null => {
    if (!form.name.trim()) return "请填写名称";
    if (!form.base_url.trim()) return "请填写 API 地址";
    if (!form.model.trim()) return "请填写模型名称";
    if (!editing && !form.api_key.trim()) return "创建机器人需要填写 API Key";
    return null;
  };

  const handleTest = async () => {
    if (!form.base_url.trim() || !form.model.trim()) {
      toast.error("测试前请填写 API 地址与模型");
      return;
    }
    testConnection.reset();
    setChecks(null);
    try {
      // 表单填了 key 就直接测；没填且在编辑时，复用已存 key 测表单的地址/模型。
      const result = await testConnection.mutateAsync({
        ...(editing ? { bot_id: editing.id } : {}),
        base_url: form.base_url.trim(),
        model: form.model.trim(),
        ...(form.api_key.trim() ? { api_key: form.api_key.trim() } : {}),
      });
      setChecks(result.checks);
      // 记录本次测试返回的能力与目标：弹窗只展示「属于当前表单目标」的能力。
      setTested({
        base_url: form.base_url.trim(),
        model: form.model.trim(),
        context_window: result.context_window,
        context_source: result.context_source,
        context_detail: result.context_detail,
      });
      if (!result.ok) toast.error("连接测试未全部通过");
    } catch (err) {
      toast.error(errorText(err, "连接测试失败"));
    }
  };

  const handleSubmit = async () => {
    const problem = validate();
    if (problem) {
      toast.error(problem);
      return;
    }
    try {
      if (editing) {
        const saved = await update.mutateAsync({
          name: form.name.trim(),
          persona_prompt: form.persona_prompt.trim(),
          base_url: form.base_url.trim(),
          model: form.model.trim(),
          // 指针语义：空串表示保留已存 key，不随请求发送。
          ...(form.api_key.trim() ? { api_key: form.api_key.trim() } : {}),
        });
        toast.success("机器人已更新，配置对下个运行生效");
        onSaved?.(saved);
      } else {
        const saved = await create.mutateAsync({
          name: form.name.trim(),
          persona_prompt: form.persona_prompt.trim(),
          base_url: form.base_url.trim(),
          model: form.model.trim(),
          api_key: form.api_key.trim(),
        });
        toast.success("机器人已创建");
        onSaved?.(saved);
      }
      onClose();
    } catch (err) {
      toast.error(errorText(err, "保存失败"));
    }
  };

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑机器人" : "新建机器人"}</DialogTitle>
          <DialogDescription>
            内置智能体通过 OpenAI 兼容接口对话，用白名单工具操作 Fast Ship。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="bot-name">名称</Label>
            <Input
              id="bot-name"
              value={form.name}
              onChange={(e) => patch({ name: e.target.value })}
              placeholder="例如：发布助手"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="bot-persona">人设提示词</Label>
            <Textarea
              id="bot-persona"
              value={form.persona_prompt}
              onChange={(e) => patch({ persona_prompt: e.target.value })}
              placeholder="这个机器人负责什么、说话风格、边界…"
              className="min-h-20"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="bot-base-url">API 地址</Label>
            <Input
              id="bot-base-url"
              value={form.base_url}
              onChange={(e) => patch({ base_url: e.target.value })}
              placeholder="https://api.example.com/v1"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="bot-model">模型</Label>
            <Input
              id="bot-model"
              value={form.model}
              onChange={(e) => patch({ model: e.target.value })}
              placeholder="例如：gpt-4o-mini"
            />
            {shownCapability != null &&
              (shownCapability.context_window ?? 0) > 0 && (
                <p
                  data-testid="bot-context-window"
                  className="text-xs text-muted-foreground"
                >
                  上下文窗口{" "}
                  {(
                    shownCapability.context_window ?? 0
                  ).toLocaleString("en-US")}{" "}
                  tokens
                  {shownCapability.context_source &&
                    ` · 来源：${CONTEXT_SOURCE_LABELS[shownCapability.context_source] ?? shownCapability.context_source}`}
                  {shownCapability.context_detail &&
                    `（${shownCapability.context_detail}）`}
                </p>
              )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="bot-api-key">API Key</Label>
            <Input
              id="bot-api-key"
              type="password"
              autoComplete="off"
              value={form.api_key}
              onChange={(e) => patch({ api_key: e.target.value })}
              placeholder={editing?.api_key_last4 ? "留空保留已配置的密钥" : "sk-…"}
            />
            {editing?.api_key_last4 && !form.api_key && (
              <p className="flex items-center gap-1 text-xs text-muted-foreground">
                <ShieldCheck className="h-3.5 w-3.5" />
                已配置（末四位 {editing.api_key_last4}），不会回显明文
              </p>
            )}
          </div>

          {testConnection.isError && (
            <p role="alert" className="text-xs text-destructive">
              {errorText(testConnection.error, "连接测试失败")}
            </p>
          )}
          {checks && (
            <ul
              data-testid="bot-connection-checks"
              className="space-y-1.5 rounded-lg border bg-muted/30 px-3 py-2.5"
            >
              {checks.map((check) => (
                <CheckResult key={check.name} check={check} />
              ))}
            </ul>
          )}
        </div>

        <DialogFooter className="gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={busy || testing}
            onClick={() => void handleTest()}
          >
            {testing && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            测试连接
          </Button>
          <Button
            type="button"
            disabled={busy || testing}
            onClick={() => void handleSubmit()}
          >
            {busy && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
