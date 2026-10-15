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
import { OptionSelect } from "@/components/ui/option-select";
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
import { projectApi } from "@/lib/api/projects";
import { parseRepoUrl, hasGitHubRepo } from "@/lib/utils/github";
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

  const schema = isEdit ? projectEditSchema : projectSchema;
  type FormData = ProjectInput | ProjectEditInput;

  const {
    control,
    register,
    handleSubmit,
    reset,
    setValue,
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
      reset({
        name: project.name,
        description: project.description || "",
        repository_url: hasGitHubRepo(project)
          ? `https://github.com/${project.github_owner}/${project.github_repo}`
          : "",
        public_github_token_id: project.public_github_token_id || "",
        code_github_token_id: project.code_github_token_id || "",
      });
    } else if (!isEdit && open) {
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
    } catch {
      toast.error(isEdit ? "更新失败，请检查输入" : "创建失败，请检查输入");
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
