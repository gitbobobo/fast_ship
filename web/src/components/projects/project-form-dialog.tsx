import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, TriangleAlert } from "lucide-react";
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
  hostFromRepoInput,
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
    name: "GitHub（内置）",
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
  // slug 经 useEffect 回填：首帧渲染时还是 ""——记回填完成的项目 id，
  // 未完成前不渲染「修改代码仓库将停用 PR 审查」警示，否则空串与已存
  // code_repo_url 必然不等、警示闪一帧才消失。
  const [slugHydratedId, setSlugHydratedId] = useState<string | null>(null);
  // 保存时服务端报「集成不存在/已删除」的字段级回显：toast 之外把错误钉在
  // 选择器上，并重拉列表让失效 id 落到「原绑定集成（已失效）」占位——选中
  // 态继续显示已删名称会误导用户改错字段。
  const [integrationError, setIntegrationError] = useState("");

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
        integrationList.map((option) => {
          // 管理员起的名字里可能带半角括号，与内置名的全角混排；展示层统一
          // 全角，并吃掉括号前遗留的空格（"Forgejo (内部)" → "Forgejo（内部）"）。
          // 地址只显主机（次要样式）——name（url） 两组括号连读太挤。
          const displayName = option.name.replace(/ ?\(([^)]*)\)/g, "（$1）");
          let host = option.base_url;
          try {
            host = new URL(option.base_url).host;
          } catch {
            // 基址非 URL 时原样展示，管理侧保存时已校验过。
          }
          return {
            value: option.id,
            label: displayName,
            itemLabel: (
              <span className="flex items-baseline gap-2">
                <span>{displayName}</span>
                <span className="text-xs text-muted-foreground">{host}</span>
              </span>
            ),
          };
        }),
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
  // selectedIntegration 解析不出（加载中/失败，forge 绑定不在保底列表）时
  // 不判 repoChanged——空合成与已存 URL 必然不等，打开弹框就误警示。
  const repoChanged =
    isEdit &&
    !!selectedIntegration &&
    normalizeCodeRepoRef(
      codeRepoUrlFromSlug(selectedIntegration, ownerRepoFromInput(codeRepoSlug)),
    ) !==
      normalizeCodeRepoRef(project?.code_repo_url ?? "");
  const integrationChanged = isEdit && codeIntegrationId !== savedIntegrationId;
  const showRepoChangeWarning =
    isEdit &&
    slugHydratedId === projectId &&
    !!project?.code_repo_url &&
    (repoChanged || integrationChanged);

  // 所选集成 base_url 的 origin 键（scheme+host，缺省端口由 URL 归一，
  // 尾点与后端主机键同口径剥除）。
  const integrationOriginKey = (): string => {
    if (!selectedIntegration) return "";
    try {
      const u = new URL(selectedIntegration.base_url);
      // 与后端 SplitHostPort 后剥点同序：端口前尾点（host.:8443）也归一。
      return `${u.protocol}//${u.host.replace(/\.+:/g, ":").replace(/\.+$/, "")}`;
    } catch {
      return "";
    }
  };

  // 输入显式主机的 scheme+host 键：比对带 scheme——服务端按 scheme 补默认
  // 端口区分主机键（http→:80、https→:443），http/https 同主机是不同绑定。
  // 裸主机段无 scheme 时借集成的——合成地址反正用它。
  const inputOriginKey = (raw: string): string => {
    let host = hostFromRepoInput(raw);
    if (host === "") return "";
    const i = raw.trim().indexOf("://");
    const scheme =
      i === -1
        ? integrationOriginKey().split(":")[0] || "https"
        : raw.trim().slice(0, i).toLowerCase();
    // 裸 host:port 无输入 scheme 可剥默认端口（hostFromRepoInput 只认输入
    // 自带的 scheme），按推导出的 scheme 再剥一次，与后端 normalizeCode-
    // RepoRef 同口径——forge.example.com:443 对 https 集成是同一主机键。
    const defaultPort = scheme === "https" ? ":443" : scheme === "http" ? ":80" : "";
    if (defaultPort !== "" && host.endsWith(defaultPort)) {
      host = host.slice(0, -defaultPort.length);
    }
    return `${scheme}://${host}`;
  };

  // 合成提交用的 code_repository_url；解析不出（输入非 owner/repo 两段，或
  // 所选集成不在列表里）就 setError 中止提交——不静默丢弃也不猜主机。
  const synthesizeRepoUrl = (): string | null => {
    const parts = ownerRepoFromInput(codeRepoSlug);
    // 显式主机必须与所选集成同 origin——解析会剥掉主机段，不比对会把
    // forgejo-B 的完整地址静默绑到 forgejo-A 下。
    const inputKey = inputOriginKey(codeRepoSlug);
    const integrationKey = integrationOriginKey();
    // 输入带显式主机时，集成主机解析不出不能静默跳过比对——回退通用错误，
    // 跨主机错绑不放行。
    const hostMismatch = inputKey !== "" && inputKey !== integrationKey;
    if (!selectedIntegration || !parts.owner || !parts.repo || hostMismatch) {
      setError("code_repository_url", {
        message:
          hostMismatch && integrationKey !== ""
            ? `仓库地址 ${inputKey} 与所选集成（${integrationKey}）不一致`
            : hostMismatch
              ? "所选代码仓库集成的地址不可用，请重新选择集成"
              : selectedIntegration
                ? "格式：owner/repo（或完整仓库地址）"
                : "所选代码仓库集成不可用，请重新选择",
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
      setIntegrationError("");
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
      setSlugHydratedId(projectId ?? null);
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
      setSlugHydratedId(null);
      setIntegrationError("");
      reset({
        name: "",
        description: "",
        repository_url: "",
        public_github_token_id: "",
        code_github_token_id: "",
      });
    }
  }, [isEdit, project, projectId, open, reset]);

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
          if (bindingBroken && clearBrokenBinding) {
            // 显式清除失效绑定：即使 slug 从结构化字段预填了旧值也按清空
            // 提交——勾选语义优先于输入框内容。
            payload.code_repository_url = null;
          } else if (codeRepoSlug.trim() === "") {
            // 悬空绑定（bindingBroken）留空视为未改动，不携带该字段：只保存
            // 其它字段不再把悬空绑定静默改绑回 github 同名仓库。解除走显式
            // 「清除绑定」勾选（上面已先判）。普通空输入仍是显式清空——当前
            // 本就没有代码仓（code_repo_url 为 null）时同样不携带。
            if (!bindingBroken && currentRepoUrl !== "") {
              payload.code_repository_url = null;
            } else if (codeIntegrationId !== savedIntegrationId) {
              // 选了别的集成但 slug 留空：改动无法表达（API 不收单独的
              // 集成字段），保存会静默回弹选择器——就地报错要求填写。
              setError("code_repository_url", {
                message: "更换代码仓集成时需填写 owner/repo",
              });
              return;
            }
          } else {
            // 绑定等价（集成 id + owner/repo 归一相同）直接不携带——集成列表
            // 还在加载/加载失败时 selectedIntegration 解析不出，合成会中止
            // 提交；此时用户只改项目名等无关字段也应能保存。只有真改动才要求
            // 选项解析成功。
            const savedParts =
              project.code_github_owner && project.code_github_repo
                ? { owner: project.code_github_owner, repo: project.code_github_repo }
                : codeRepoSlugFromSaved(currentRepoUrl);
            const inputParts = ownerRepoFromInput(codeRepoSlug);
            // 显式 origin 不一致不算「未改动」——owner/repo 同名而
            // scheme+host 不同会让 bindingUnchanged 吞掉主机校验旁路，
            // 先判 origin 再走 synthesizeRepoUrl 的报错路径。
            const inputKey = inputOriginKey(codeRepoSlug);
            const bindingUnchanged =
              codeIntegrationId === savedIntegrationId &&
              !(inputKey !== "" && inputKey !== integrationOriginKey()) &&
              !!inputParts.owner &&
              !!inputParts.repo &&
              inputParts.owner.toLowerCase() === savedParts.owner.toLowerCase() &&
              inputParts.repo.toLowerCase() === savedParts.repo.toLowerCase();
            if (!bindingUnchanged) {
              const synthesized = synthesizeRepoUrl();
              if (synthesized === null) return;
              const next = normalizeCodeRepoRef(synthesized);
              const current = normalizeCodeRepoRef(currentRepoUrl);
              if (next === "" || next !== current) {
                payload.code_repository_url = synthesized;
              }
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
        } else if (codeIntegrationId !== defaultIntegrationId) {
          // 选了非内置集成但 slug 留空：API 不收单独的集成字段，创建出来
          // 会静默绑回 github——与编辑路径同口径就地报错要求填写。
          setError("code_repository_url", {
            message: "更换代码仓集成时需填写 owner/repo",
          });
          return;
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
      const msg = errorText(
        err,
        isEdit ? "更新失败，请检查输入" : "创建失败，请检查输入",
      );
      // 集成在打开弹窗后被删/失效：重试不会成功——把错误钉到选择器上，
      // 并重拉选项让失效 id 落到「已失效」占位而不是继续显示旧名。不再
      // 另发 toast：字段级红字与 toast 文案一字不差，顶部 toast 还会压住
      // 弹窗标题栏——同一错误在相邻位置出现两次没有增量信息。
      if (/集成.*(不存在|已删除|失效|不可用)/.test(msg)) {
        setIntegrationError(msg);
        void refetchIntegrations();
      } else {
        toast.error(msg);
      }
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[90vh] flex-col overflow-hidden sm:max-w-lg">
        {/* 底栏有 border-t 分隔，标题栏同侧补 border-b——内容滚动时被裁
            到一半的字段标签不会直接贴着标题。负外边距让分隔线贯通弹窗宽。 */}
        <DialogHeader className="-mx-4 border-b border-foreground/10 px-4 pb-3">
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
          <form
            onSubmit={handleSubmit(onSubmit)}
            className="flex min-h-0 flex-col"
          >
            {/* 滚动区与固定底栏分行：sticky 底栏会相对滚动容器内容盒贴底，
                容器下 padding 处透出滚动内容；独立行无叠压。负外边距让
                滚动条贴弹窗边缘、底栏 bleed 到圆角边。 */}
            <div className="-mx-4 min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-3">
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
              <Label htmlFor="project-code_integration_id">代码仓库集成</Label>
              <OptionSelect
                value={codeIntegrationId}
                onValueChange={(next) => {
                  setCodeIntegrationId(next ?? defaultIntegrationId);
                  setIntegrationError("");
                }}
                options={integrationSelectOptions}
                id="project-code_integration_id"
                className="w-full"
                size="default"
                invalid={integrationError !== ""}
                alignItemWithTrigger={false}
              />
              {integrationsLoading && (
                <p className="text-xs text-muted-foreground">
                  正在加载集成列表...
                </p>
              )}
              {integrationsError && (
                <p className="flex items-center gap-1.5 text-xs text-amber-700 dark:text-amber-400">
                  <TriangleAlert className="h-3.5 w-3.5 shrink-0" />
                  <span>
                    集成列表加载失败，暂仅提供 GitHub（内置）
                    {" ·"}
                    <Button
                      type="button"
                      variant="link"
                      size="sm"
                      className="ml-1 h-auto rounded-md px-2 py-1.5 underline underline-offset-2 hover:bg-foreground/10"
                      onClick={() => void refetchIntegrations()}
                    >
                      重试
                    </Button>
                  </span>
                </p>
              )}
              {integrationError !== "" && (
                <p className="text-xs text-destructive">{integrationError}</p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="project-code_repository_url">
                {selectedIntegration == null
                  ? "代码仓库"
                  : selectedIntegration.kind === "forgejo"
                    ? "代码仓库（该集成下必填）"
                    : "代码仓库（可选，默认同公开仓库）"}
              </Label>
              <Input
                id="project-code_repository_url"
                placeholder="owner/repo"
                value={codeRepoSlug}
                onChange={(e) => setCodeRepoSlug(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {selectedIntegration == null
                  ? "选择集成后确认该仓库"
                  : selectedIntegration.kind === "forgejo"
                    ? "该集成上的 owner/repo，PR 审查在此进行"
                    : "PR 审查盯的仓库；留空则审查公开仓库"}
              </p>
              {showRepoChangeWarning && (
                <div className="flex items-start gap-2 rounded-md border border-amber-300/70 bg-amber-50 px-3 py-2 dark:border-amber-500/40 dark:bg-amber-950/40">
                  <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-700 dark:text-amber-400" />
                  <p className="text-xs leading-5 text-amber-900 dark:text-amber-200">
                    修改代码仓库将自动停用 PR 审查，保存后需重新开启
                  </p>
                </div>
              )}
              {bindingBroken && (
                <div className="space-y-1">
                  <p className="text-xs text-amber-700 dark:text-amber-400">
                    原绑定的代码仓库集成已失效；留空将保留现状，填写新地址可重新绑定
                  </p>
                  <label className="flex items-center gap-2 text-xs text-amber-700 dark:text-amber-400">
                    <input
                      type="checkbox"
                      checked={clearBrokenBinding}
                      onChange={(e) => setClearBrokenBinding(e.target.checked)}
                    />
                    清除失效的代码仓库绑定（回落到公开仓库）
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
            </div>

            <div className="-mx-4 -mb-4 mt-3 flex items-center gap-3 rounded-b-xl border-t border-foreground/10 bg-popover px-4 py-3">
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
