import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus } from "lucide-react";
import { GitHubTokenHelpDialog } from "@/components/projects/github-token-help-dialog";
import { GithubTokenCreateDialog } from "@/components/settings/github-token-create-dialog";
import { T3BindingsSection } from "@/components/projects/t3-bindings-section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { OptionSelect, withMissingOption } from "@/components/ui/option-select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  projectSchema,
  projectEditSchema,
  type ProjectInput,
  type ProjectEditInput,
} from "@/lib/utils/validators";
import {
  useProject,
  useCreateProject,
  useUpdateProject,
} from "@/lib/hooks/use-projects";
import { useGithubTokens } from "@/lib/hooks/use-github-tokens";
import { useIntegrationOptions } from "@/lib/hooks/use-integration-options";
import { projectApi } from "@/lib/api/projects";
import { errorText } from "@/lib/api/client";
import {
  parseRepoUrl,
  hasGitHubRepo,
  normalizeCodeRepoRef,
  codeRepoSlugFromSaved,
  ownerRepoFromInput,
  codeRepoUrlFromSlug,
} from "@/lib/utils/github";
import { toast } from "sonner";

interface ProjectFormDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mode: "create" | "edit";
  projectId?: string;
  /** 深链（/projects?edit=<id>&section=t3）：打开后滚动到对应节。 */
  section?: string;
}

const emptyTokenOption = "__none__";
const defaultIntegrationId = "github-default";

// 接口加载中/失败时的保底选项（契约保证首条恒为内置 github-default）：
// 选择器至少给出内置 GitHub，github 仓的新增与编辑不被集成列表故障卡住。
const fallbackIntegrationOptions: IntegrationOption[] = [
  {
    id: defaultIntegrationId,
    kind: "github",
    name: "内置 GitHub",
    base_url: "https://github.com",
    builtin: true,
  },
];

// Token 引用选择器：选项来自用户级 Token 库，可空；「新建 Token」打开共用
// 的创建对话框，成功后自动选中新记录。
function TokenSelect({
  id,
  label,
  value,
  tokens,
  isLoading,
  onChange,
  onCreateClick,
}: {
  id: string;
  label: string;
  value: string;
  tokens: GitHubToken[];
  isLoading: boolean;
  onChange: (tokenID: string) => void;
  onCreateClick: () => void;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}（可选）</Label>
      <div className="flex gap-2">
        <OptionSelect
          value={value || emptyTokenOption}
          onValueChange={(next) => onChange(next === emptyTokenOption || next === null ? "" : next)}
          options={[
            { value: emptyTokenOption, label: "不使用" },
            ...tokens.map((token) => ({
              value: token.id,
              label: token.title,
              itemLabel: `${token.title}（${token.token_prefix}••••）`,
            })),
          ]}
          valueLabel={
            value
              ? (tokens.find((token) => token.id === value)?.title ?? "已选 Token")
              : "不使用"
          }
          id={id}
          className="w-full"
          size="default"
        />
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={`新建 ${label}`}
          onClick={onCreateClick}
        >
          <Plus className="h-4 w-4" />
        </Button>
      </div>
      {isLoading && (
        <p className="text-xs text-muted-foreground">正在加载 Token 列表...</p>
      )}
      {!isLoading && tokens.length === 0 && (
        <p className="text-xs text-muted-foreground">
          还没有 Token，可点击 + 新建
        </p>
      )}
    </div>
  );
}

export function ProjectFormDialog({
  open,
  onOpenChange,
  mode,
  projectId,
  section,
}: ProjectFormDialogProps) {
  const navigate = useNavigate();
  const isEdit = mode === "edit";
  const { data: project, isLoading } = useProject(projectId ?? "");
  const createProject = useCreateProject();
  const updateProject = useUpdateProject(projectId ?? "");
  const { data: tokens, isLoading: tokensLoading } = useGithubTokens();
  const {
    data: integrationOptions,
    isLoading: integrationsLoading,
    isError: integrationsError,
    refetch: refetchIntegrations,
  } = useIntegrationOptions();

  const schema = isEdit ? projectEditSchema : projectSchema;
  type FormData = ProjectInput | ProjectEditInput;

  const {
    control,
    register,
    handleSubmit,
    reset,
    setValue,
    setError,
    formState: { errors, isSubmitting },
  } = useForm<FormData>({
    resolver: zodResolver(schema),
  });

  const repositoryUrl = useWatch({ control, name: "repository_url" }) || "";
  const publicTokenId = useWatch({ control, name: "public_github_token_id" }) || "";
  const codeTokenId = useWatch({ control, name: "code_github_token_id" }) || "";
  const { owner, repo } = parseRepoUrl(repositoryUrl);
  const tokenList = useMemo(() => tokens ?? [], [tokens]);

  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [creatingFor, setCreatingFor] = useState<"public" | "code">("public");
  const t3SectionRef = useRef<HTMLElement | null>(null);

  // 悬空绑定：绑定了代码仓集成但投影 code_repo_url 为空（集成行已删等）。
  // 回填与提交都按「保留现状」处理，不再回落 github 同名地址；界面上给出
  // 提示，填写新地址才重新绑定。解除悬空绑定走显式勾选——空输入本身无法
  // 区分「没动」与「清空」，每次无关保存都当清空发 null 会静默掉绑定。
  const bindingBroken = !!project?.code_integration_id && !project?.code_repo_url;
  const [clearBrokenBinding, setClearBrokenBinding] = useState(false);

  // 集成选择与 owner/repo 是表单内的独立状态：提交时按所选集成合成
  // code_repository_url（服务端按 URL 主机派生集成绑定，选择器只是合成
  // 辅助，本身不作为字段提交）。code_integration_id 为空串按内置处理。
  const [codeIntegrationId, setCodeIntegrationId] =
    useState(defaultIntegrationId);
  const [codeRepoSlug, setCodeRepoSlug] = useState("");

  // 接口未就绪/失败时降级用保底列表；失效绑定（id 不在列表里）交给
  // withMissingOption 显示占位项，不静默改绑回 github。
  const integrationList = integrationOptions ?? fallbackIntegrationOptions;
  const selectedIntegration = integrationList.find(
    (option) => option.id === codeIntegrationId,
  );
  // 占位项文案按数据态区分：加载中 ≠ 加载失败 ≠ 真失效。
  const missingIntegrationLabel = integrationsLoading
    ? "正在加载集成列表..."
    : integrationsError
      ? "集成列表不可用"
      : "原绑定集成（已失效）";
  const integrationSelectOptions = useMemo(
    () =>
      withMissingOption(
        integrationList.map((option) => ({
          value: option.id,
          label: option.name,
          itemLabel: `${option.name}（${option.base_url}）`,
        })),
        codeIntegrationId,
        missingIntegrationLabel,
      ),
    [integrationList, codeIntegrationId, missingIntegrationLabel],
  );

  // 修改提示：已有代码仓的项目，owner/repo 或集成选择相对已保存值有变化时
  // 提醒 PR 审查会被停用（服务端改绑即重置审查状态）；新建项目与无代码仓
  // 的项目不提示。等价判定沿用归一口径——scheme 有无、尾斜杠、.git、裸
  // owner/repo 形态都不算改动。
  const savedIntegrationId = project?.code_integration_id || defaultIntegrationId;
  const repoChanged =
    isEdit &&
    normalizeCodeRepoRef(
      selectedIntegration
        ? codeRepoUrlFromSlug(selectedIntegration, ownerRepoFromInput(codeRepoSlug))
        : "",
    ) !==
      normalizeCodeRepoRef(project?.code_repo_url ?? "");
  const integrationChanged = isEdit && codeIntegrationId !== savedIntegrationId;
  const showRepoChangeWarning =
    isEdit &&
    !!project?.code_repo_url &&
    (repoChanged || integrationChanged);

  // 合成提交用的 code_repository_url；解析不出（输入非 owner/repo 两段，或
  // 所选集成不在列表里）就 setError 中止提交——不静默丢弃也不猜主机。
  const synthesizeRepoUrl = (): string | null => {
    const parts = ownerRepoFromInput(codeRepoSlug);
    if (!selectedIntegration || !parts.owner || !parts.repo) {
      setError("code_repository_url", {
        message: selectedIntegration
          ? "格式：owner/repo（或完整仓库地址）"
          : "所选代码仓集成不可用，请重新选择",
      });
      return null;
    }
    return codeRepoUrlFromSlug(selectedIntegration, parts);
  };

  useEffect(() => {
    if (open && isEdit && section === "t3") {
      // 等弹框动画与内容就位后再滚动
      const frame = window.requestAnimationFrame(() =>
        t3SectionRef.current?.scrollIntoView({ block: "start" }),
      );
      return () => window.cancelAnimationFrame(frame);
    }
  }, [open, isEdit, section, isLoading]);

  useEffect(() => {
    if (isEdit && project) {
      setClearBrokenBinding(false);
      // 集成选择按已保存绑定回填（悬空绑定的失效 id 原样保留，交给
      // withMissingOption 显示）；owner/repo 优先取结构化字段——带路径前缀
      // 的集成基址（https://forge.example/git/o/r）从 code_repo_url 剥主机
      // 会解析不出两段而退化成「未绑定」，只改项目名也会被当成清空提交。
      // 结构化字段缺失（旧行）再回落 URL 剥主机。
      setCodeIntegrationId(project.code_integration_id || defaultIntegrationId);
      const structured =
        project.code_github_owner && project.code_github_repo
          ? { owner: project.code_github_owner, repo: project.code_github_repo }
          : null;
      const parts = structured ?? codeRepoSlugFromSaved(project.code_repo_url ?? "");
      setCodeRepoSlug(parts.owner ? `${parts.owner}/${parts.repo}` : "");
      reset({
        name: project.name,
        description: project.description || "",
        // 回填服务端投影的 repository_url：github_host 语义（公共 github.com
        // vs 本部署 GHE）前端无从分辨，自拼 github.com 会把部署实例的项目在
        // 保存时静默改写成公共主机（R41 P2）。
        repository_url: project.repository_url ?? "",
        public_github_token_id: project.public_github_token_id || "",
        code_github_token_id: project.code_github_token_id || "",
      });
    } else if (!isEdit && open) {
      setCodeIntegrationId(defaultIntegrationId);
      setCodeRepoSlug("");
      reset({
        name: "",
        description: "",
        repository_url: "",
        public_github_token_id: "",
        code_github_token_id: "",
      });
    }
  }, [isEdit, project, open, reset]);

  const openCreateDialog = (purpose: "public" | "code") => {
    setCreatingFor(purpose);
    setCreateDialogOpen(true);
  };

  const handleTokenCreated = (token: GitHubToken) => {
    setValue(creatingFor === "public" ? "public_github_token_id" : "code_github_token_id", token.id);
  };

  const onSubmit = async (data: FormData) => {
    try {
      if (isEdit) {
        // 编辑：引用字段只在用户改动过时才提交（缺省保留现值）。
        const payload: Parameters<typeof projectApi.update>[1] = {
          name: data.name,
        };
        if (data.description !== undefined) {
          payload.description = data.description;
        }
        if (data.repository_url) payload.repository_url = data.repository_url;
        // 代码仓库同 Token 引用走三态：与现值等价则不携带 key，清空传 null
        // 回落公开仓库。输入先按所选集成合成完整 URL 再归一比较（见
        // normalizeCodeRepoRef）：scheme 有无与大小写、尾部斜杠与 .git、裸
        // owner/repo 形态都不算改动；跨主机的真改动归一出不同引用，不会被
        // 误判「未改动」而静默丢掉。
        if (project) {
          const currentRepoUrl = project.code_repo_url ?? "";
          if (codeRepoSlug.trim() === "") {
            // 悬空绑定（bindingBroken）留空视为未改动，不携带该字段：只保存
            // 其它字段不再把悬空绑定静默改绑回 github 同名仓库。解除走显式
            // 「清除绑定」勾选——勾选后按清空提交（回落公开仓库，集成绑定
            // 一并清）。普通空输入仍是显式清空——当前本就没有代码仓
            // （code_repo_url 为 null）时同样不携带。
            if ((bindingBroken && clearBrokenBinding) ||
              (!bindingBroken && currentRepoUrl !== "")) {
              payload.code_repository_url = null;
            }
          } else {
            const synthesized = synthesizeRepoUrl();
            if (synthesized === null) return;
            const next = normalizeCodeRepoRef(synthesized);
            const current = normalizeCodeRepoRef(currentRepoUrl);
            if (next === "" || next !== current) {
              payload.code_repository_url = synthesized;
            }
          }
        }
        if (project && data.public_github_token_id !== undefined) {
          const current = project.public_github_token_id || "";
          payload.public_github_token_id =
            data.public_github_token_id === current
              ? undefined
              : data.public_github_token_id || null;
        }
        if (project && data.code_github_token_id !== undefined) {
          const current = project.code_github_token_id || "";
          payload.code_github_token_id =
            data.code_github_token_id === current
              ? undefined
              : data.code_github_token_id || null;
        }
        await updateProject.mutateAsync(payload);
        toast.success("项目已更新");
        onOpenChange(false);
      } else {
        const createData = data as ProjectInput;
        const payload: Parameters<typeof projectApi.create>[0] = {
          name: createData.name,
        };
        if (createData.description) {
          payload.description = createData.description;
        }
        if (createData.repository_url) {
          payload.repository_url = createData.repository_url;
        }
        if (codeRepoSlug.trim() !== "") {
          const synthesized = synthesizeRepoUrl();
          if (synthesized === null) return;
          payload.code_repository_url = synthesized;
        }
        if (createData.public_github_token_id) {
          payload.public_github_token_id = createData.public_github_token_id;
        }
        if (createData.code_github_token_id) {
          payload.code_github_token_id = createData.code_github_token_id;
        }
        const res = await createProject.mutateAsync(payload);
        toast.success("项目创建成功");
        onOpenChange(false);
        navigate(`/issues?project=${res.data.id}`);
      }
    } catch (err) {
      // 服务端 40001 的具体原因（主机未匹配集成、端口无效、多余路径段等）
      // 透出来——吞成通用提示会让 Forgejo 绑定失败只剩「检查输入」无从定位。
      toast.error(
        errorText(err, isEdit ? "更新失败，请检查输入" : "创建失败，请检查输入"),
      );
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{isEdit ? "编辑项目" : "新建项目"}</DialogTitle>
        </DialogHeader>

        {isEdit && isLoading ? (
          <div className="py-4 space-y-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-20 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : (
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="project-name">项目名称</Label>
              <Input
                id="project-name"
                placeholder={isEdit ? undefined : "my-app"}
                {...register("name")}
              />
              {errors.name && (
                <p className="text-xs text-destructive">
                  {errors.name.message}
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="project-description">项目描述（可选）</Label>
              <Textarea
                id="project-description"
                placeholder="简单描述你的项目"
                {...register("description")}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="project-repository_url">仓库链接（可选）</Label>
              <div className="flex items-center justify-between">
                <p className="text-xs text-muted-foreground">
                  仓库与 Token 相互独立，可分开配置
                </p>
                {hasGitHubRepo({ github_owner: owner, github_repo: repo }) && (
                  <GitHubTokenHelpDialog owner={owner} repo={repo} />
                )}
              </div>
              <Input
                id="project-repository_url"
                placeholder="https://github.com/owner/repo 或 owner/repo"
                {...register("repository_url")}
              />
              {errors.repository_url && (
                <p className="text-xs text-destructive">
                  {errors.repository_url.message}
                </p>
              )}
            </div>

            <TokenSelect
              id="project-public_github_token_id"
              label="公开仓库 Token"
              value={publicTokenId}
              tokens={tokenList}
              isLoading={tokensLoading}
              onChange={(next) => setValue("public_github_token_id", next)}
              onCreateClick={() => openCreateDialog("public")}
            />
            <p className="text-xs text-muted-foreground -mt-2">
              用于 Issue 同步、GitHub 写操作、分支查询与发货；未配置时这些功能停用
            </p>

            <div className="space-y-2">
              <Label htmlFor="project-code_integration_id">代码仓集成</Label>
              <OptionSelect
                value={codeIntegrationId}
                onValueChange={(next) =>
                  setCodeIntegrationId(next ?? defaultIntegrationId)
                }
                options={integrationSelectOptions}
                id="project-code_integration_id"
                className="w-full"
                size="default"
              />
              {integrationsLoading && (
                <p className="text-xs text-muted-foreground">
                  正在加载集成列表...
                </p>
              )}
              {integrationsError && (
                <p className="text-xs text-muted-foreground">
                  集成列表加载失败，暂仅提供内置 GitHub
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    className="h-auto px-1"
                    onClick={() => void refetchIntegrations()}
                  >
                    重试
                  </Button>
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="project-code_repository_url">
                owner / repo（可选，默认同公开仓库）
              </Label>
              <Input
                id="project-code_repository_url"
                placeholder="owner/repo"
                value={codeRepoSlug}
                onChange={(e) => setCodeRepoSlug(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {selectedIntegration?.kind === "forgejo"
                  ? "PR 审查盯的仓库；webhook 与补漏轮询都走该 Forgejo 集成"
                  : "PR 审查盯的仓库；留空则审查公开仓库"}
              </p>
              {showRepoChangeWarning && (
                <p className="text-xs text-amber-700 dark:text-amber-400">
                  修改代码仓将自动停用 PR 审查，保存后需重新开启
                </p>
              )}
              {bindingBroken && (
                <div className="space-y-1">
                  <p className="text-xs text-amber-700 dark:text-amber-400">
                    原绑定的代码仓集成已失效；留空将保留现状，填写新地址可重新绑定
                  </p>
                  <label className="flex items-center gap-2 text-xs text-amber-700 dark:text-amber-400">
                    <input
                      type="checkbox"
                      checked={clearBrokenBinding}
                      onChange={(e) => setClearBrokenBinding(e.target.checked)}
                    />
                    清除失效的代码仓绑定（回落到公开仓库）
                  </label>
                </div>
              )}
              {errors.code_repository_url && (
                <p className="text-xs text-destructive">
                  {errors.code_repository_url.message}
                </p>
              )}
            </div>

            <TokenSelect
              id="project-code_github_token_id"
              label="代码仓库 Token"
              value={codeTokenId}
              tokens={tokenList}
              isLoading={tokensLoading}
              onChange={(next) => setValue("code_github_token_id", next)}
              onCreateClick={() => openCreateDialog("code")}
            />
            <p className="text-xs text-muted-foreground -mt-2">
              仅用于读取关联 PR（含其他仓库）；未配置时匿名读公共 PR
            </p>

            {isEdit && projectId && (
              <T3BindingsSection ref={t3SectionRef} projectId={projectId} />
            )}

            <div className="flex gap-3 pt-2">
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting
                  ? isEdit
                    ? "保存中..."
                    : "创建中..."
                  : isEdit
                    ? "保存"
                    : "创建项目"}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
              >
                取消
              </Button>
            </div>
          </form>
        )}
      </DialogContent>

      <GithubTokenCreateDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        onCreated={handleTokenCreated}
      />
    </Dialog>
  );
}
