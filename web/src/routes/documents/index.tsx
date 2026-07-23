import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import {
  ChevronRight,
  FilePlus2,
  FileText,
  FolderPlus,
  Inbox,
  Save,
  Trash2,
} from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { useProjects } from "@/lib/hooks/use-projects";
import {
  useCreateDocument,
  useDeleteDocument,
  useDocument,
  useDocuments,
  useUpdateDocument,
} from "@/lib/hooks/use-documents";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { getActiveProjectId } from "@/routes/board/lib/utils";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

type TreeNode = DocumentListItem & { children: TreeNode[] };

function buildTree(items: DocumentListItem[]): TreeNode[] {
  const map = new Map<string, TreeNode>();
  for (const item of items) {
    map.set(item.id, { ...item, children: [] });
  }
  const roots: TreeNode[] = [];
  for (const item of items) {
    const node = map.get(item.id)!;
    if (item.parent_id && map.has(item.parent_id)) {
      map.get(item.parent_id)!.children.push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
}

function collectSubtreeIds(
  items: DocumentListItem[],
  rootId: string,
): Set<string> {
  const childrenMap = new Map<string, string[]>();
  for (const item of items) {
    if (!item.parent_id) continue;
    const list = childrenMap.get(item.parent_id) ?? [];
    list.push(item.id);
    childrenMap.set(item.parent_id, list);
  }
  const result = new Set<string>();
  const stack = [rootId];
  while (stack.length) {
    const id = stack.pop()!;
    result.add(id);
    for (const child of childrenMap.get(id) ?? []) {
      stack.push(child);
    }
  }
  return result;
}

function DocumentTreeNode({
  node,
  depth,
  selectedId,
  onSelect,
}: {
  node: TreeNode;
  depth: number;
  selectedId: string;
  onSelect: (id: string) => void;
}) {
  const [expanded, setExpanded] = useState(true);
  const hasChildren = node.children.length > 0;

  return (
    <div>
      <button
        type="button"
        className={cn(
          "flex w-full items-center gap-1 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent",
          selectedId === node.id && "bg-accent font-medium",
        )}
        style={{ paddingLeft: `${depth * 12 + 8}px` }}
        onClick={() => onSelect(node.id)}
        data-testid={`document-tree-node-${node.id}`}
      >
        <span
          className={cn(
            "inline-flex h-4 w-4 shrink-0 items-center justify-center",
            !hasChildren && "opacity-0",
          )}
          onClick={(e) => {
            e.stopPropagation();
            if (hasChildren) setExpanded((v) => !v);
          }}
        >
          <ChevronRight
            className={cn("h-3.5 w-3.5 transition-transform", expanded && "rotate-90")}
          />
        </span>
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate">{node.title}</span>
      </button>
      {expanded &&
        node.children.map((child) => (
          <DocumentTreeNode
            key={child.id}
            node={child}
            depth={depth + 1}
            selectedId={selectedId}
            onSelect={onSelect}
          />
        ))}
    </div>
  );
}

function confirmIfDirty(isDirty: boolean, message: string): boolean {
  if (!isDirty) return true;
  return window.confirm(message);
}

export default function DocumentsPage() {
  const { data: projectsData, isLoading: projectsLoading } = useProjects();
  const projects = useMemo(() => projectsData?.items ?? [], [projectsData]);

  const [searchParams, setSearchParams] = useSearchParams();
  const urlProjectId = searchParams.get("project");
  const urlDocId = searchParams.get("doc") ?? "";
  const { lastSelectedProjectId, setLastSelectedProjectId } =
    useProjectPreferenceStore();

  const [selectedProjectId, setSelectedProjectId] = useState<string>(
    () => urlProjectId ?? lastSelectedProjectId ?? "",
  );

  const activeProjectId = useMemo(
    () => getActiveProjectId(projects, selectedProjectId, urlProjectId),
    [projects, selectedProjectId, urlProjectId],
  );

  const activeProject = useMemo(
    () => projects.find((p) => p.id === activeProjectId),
    [projects, activeProjectId],
  );

  useEffect(() => {
    if (!projectsLoading && activeProjectId !== selectedProjectId) {
      setSelectedProjectId(activeProjectId);
      setLastSelectedProjectId(activeProjectId || null);
    }
  }, [
    projectsLoading,
    activeProjectId,
    selectedProjectId,
    setLastSelectedProjectId,
  ]);

  const {
    data: listData,
    isLoading: listLoading,
    isError: listError,
  } = useDocuments(activeProjectId);

  const items = useMemo(() => listData?.items ?? [], [listData]);
  const tree = useMemo(() => buildTree(items), [items]);

  const selectedDocId = urlDocId;
  const {
    data: detail,
    isLoading: detailLoading,
    isError: detailError,
  } = useDocument(selectedDocId);

  const createDocument = useCreateDocument(activeProjectId);
  const updateDocument = useUpdateDocument(activeProjectId);
  const deleteDocument = useDeleteDocument(activeProjectId);

  const [editTitle, setEditTitle] = useState("");
  const [editBody, setEditBody] = useState("");
  const [editParentId, setEditParentId] = useState<string>("");
  const [loadedKey, setLoadedKey] = useState("");

  const resetEditorState = () => {
    setEditTitle("");
    setEditBody("");
    setEditParentId("");
    setLoadedKey("");
  };

  const isDirty = useMemo(() => {
    if (!detail || detail.id !== selectedDocId) return false;
    const parent = editParentId || null;
    return (
      editTitle !== detail.title ||
      editBody !== detail.body ||
      parent !== (detail.parent_id ?? null)
    );
  }, [detail, selectedDocId, editTitle, editBody, editParentId]);

  useEffect(() => {
    if (!detail || detail.id !== selectedDocId) return;
    const key = `${detail.id}:${detail.updated_at}`;
    if (key === loadedKey) return;
    const sameDocLoaded = loadedKey.startsWith(`${selectedDocId}:`);
    if (sameDocLoaded && isDirty) return;
    setEditTitle(detail.title);
    setEditBody(detail.body);
    setEditParentId(detail.parent_id ?? "");
    setLoadedKey(key);
  }, [detail, selectedDocId, loadedKey, isDirty]);

  useEffect(() => {
    if (!isDirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [isDirty]);

  const parentOptions = useMemo(() => {
    if (!selectedDocId) return items;
    const excluded = collectSubtreeIds(items, selectedDocId);
    return items.filter((item) => !excluded.has(item.id));
  }, [items, selectedDocId]);

  const updateSearchParams = (updates: Record<string, string | null>) => {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (!value) next.delete(key);
      else next.set(key, value);
    }
    setSearchParams(next, { replace: true });
  };

  const handleProjectChange = (value: string | null) => {
    if (
      !confirmIfDirty(
        isDirty,
        "当前文档有未保存的修改，切换项目将丢弃这些修改。是否继续？",
      )
    ) {
      return;
    }
    const nextValue = value ?? "";
    setSelectedProjectId(nextValue);
    setLastSelectedProjectId(nextValue || null);
    resetEditorState();
    const next = new URLSearchParams();
    if (nextValue) next.set("project", nextValue);
    setSearchParams(next, { replace: true });
  };

  const handleSelectDoc = (id: string) => {
    if (id === selectedDocId) return;
    if (
      !confirmIfDirty(
        isDirty,
        "当前文档有未保存的修改，切换将丢弃这些修改。是否继续？",
      )
    ) {
      return;
    }
    resetEditorState();
    updateSearchParams({
      project: activeProjectId || null,
      doc: id,
    });
  };

  const handleCreate = async (parentId?: string) => {
    if (!activeProjectId) {
      toast.error("请先选择项目");
      return;
    }
    if (
      !confirmIfDirty(
        isDirty,
        "当前文档有未保存的修改，创建新文档将丢弃这些修改。是否继续？",
      )
    ) {
      return;
    }
    try {
      const res = await createDocument.mutateAsync({
        title: "未命名文档",
        body: "",
        parent_id: parentId ?? null,
      });
      resetEditorState();
      updateSearchParams({
        project: activeProjectId,
        doc: res.data.id,
      });
      toast.success("已创建文档");
    } catch {
      toast.error("创建失败，请稍后重试");
    }
  };

  const handleSave = async () => {
    if (!selectedDocId || !detail) return;
    const title = editTitle.trim();
    if (!title) {
      toast.error("标题不能为空");
      return;
    }
    const payload: {
      title: string;
      body: string;
      parent_id?: string | null;
    } = {
      title,
      body: editBody,
    };
    const nextParent = editParentId || null;
    if (nextParent !== (detail.parent_id ?? null)) {
      payload.parent_id = nextParent;
    }
    try {
      const res = await updateDocument.mutateAsync({
        docId: selectedDocId,
        payload,
      });
      setLoadedKey(`${res.data.id}:${res.data.updated_at}`);
      setEditTitle(res.data.title);
      setEditBody(res.data.body);
      setEditParentId(res.data.parent_id ?? "");
      toast.success("已保存");
    } catch {
      toast.error("保存失败，请稍后重试");
    }
  };

  const handleDelete = async () => {
    if (!selectedDocId) return;
    try {
      await deleteDocument.mutateAsync(selectedDocId);
      resetEditorState();
      updateSearchParams({
        project: activeProjectId || null,
        doc: null,
      });
      toast.success("已删除文档");
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  let mainContent: ReactNode;
  if (!activeProjectId) {
    mainContent = (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 text-muted-foreground">
        <Inbox className="h-10 w-10" />
        <p>请先选择项目</p>
      </div>
    );
  } else {
    mainContent = (
      <div
        className="flex min-h-0 flex-1 overflow-hidden border-t"
        data-testid="documents-workspace"
      >
        <aside
          className="flex w-72 shrink-0 flex-col border-r bg-muted/20"
          data-testid="document-tree"
        >
          <div className="flex items-center gap-1 border-b p-2">
            <Button
              size="sm"
              variant="outline"
              className="flex-1"
              onClick={() => void handleCreate()}
              disabled={createDocument.isPending}
            >
              <FilePlus2 className="mr-1 h-4 w-4" />
              新建根文档
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!selectedDocId || createDocument.isPending}
              onClick={() => void handleCreate(selectedDocId)}
              title="在选中节点下新建"
            >
              <FolderPlus className="h-4 w-4" />
            </Button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-2">
            {listLoading ? (
              <div className="space-y-2">
                <Skeleton className="h-6 w-full" />
                <Skeleton className="h-6 w-5/6" />
              </div>
            ) : listError ? (
              <p className="px-2 text-sm text-destructive">加载文档列表失败</p>
            ) : tree.length === 0 ? (
              <p className="px-2 py-6 text-center text-sm text-muted-foreground">
                暂无文档，点击上方按钮创建
              </p>
            ) : (
              tree.map((node) => (
                <DocumentTreeNode
                  key={node.id}
                  node={node}
                  depth={0}
                  selectedId={selectedDocId}
                  onSelect={handleSelectDoc}
                />
              ))
            )}
          </div>
        </aside>

        <section className="flex min-w-0 flex-1 flex-col">
          {!selectedDocId ? (
            <div className="flex flex-1 flex-col items-center justify-center gap-2 text-muted-foreground">
              <FileText className="h-10 w-10" />
              <p>选择左侧文档开始编辑</p>
            </div>
          ) : detailLoading ? (
            <div className="space-y-3 p-4">
              <Skeleton className="h-8 w-1/2" />
              <Skeleton className="h-40 w-full" />
            </div>
          ) : detailError || !detail ? (
            <div className="flex flex-1 items-center justify-center text-sm text-destructive">
              文档不存在或无权访问
            </div>
          ) : (
            <div className="flex min-h-0 flex-1 flex-col" data-testid="document-editor">
              <div className="flex flex-wrap items-center gap-2 border-b p-3">
                <Input
                  value={editTitle}
                  onChange={(e) => setEditTitle(e.target.value)}
                  className="max-w-md font-medium"
                  placeholder="文档标题"
                  data-testid="document-title-input"
                />
                <Select
                  value={editParentId || "__root__"}
                  onValueChange={(value) =>
                    setEditParentId(value === "__root__" || !value ? "" : value)
                  }
                >
                  <SelectTrigger className="w-48">
                    <SelectValue placeholder="父文档">
                      {editParentId
                        ? (parentOptions.find((p) => p.id === editParentId)
                            ?.title ?? editParentId)
                        : "根文档"}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__root__">根文档</SelectItem>
                    {parentOptions.map((item) => (
                      <SelectItem key={item.id} value={item.id}>
                        {item.title}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  size="sm"
                  onClick={() => void handleSave()}
                  disabled={updateDocument.isPending || !isDirty}
                  data-testid="document-save-button"
                >
                  <Save className="mr-1 h-4 w-4" />
                  保存
                </Button>
                <AlertDialog>
                  <AlertDialogTrigger
                    render={
                      <Button
                        size="sm"
                        variant="destructive"
                        disabled={deleteDocument.isPending}
                      >
                        <Trash2 className="mr-1 h-4 w-4" />
                        删除
                      </Button>
                    }
                  />
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>删除文档？</AlertDialogTitle>
                      <AlertDialogDescription>
                        将删除「{detail.title}」及其全部子文档，此操作不可恢复。
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>取消</AlertDialogCancel>
                      <AlertDialogAction onClick={() => void handleDelete()}>
                        确认删除
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
              <div className="min-h-0 flex-1 overflow-y-auto p-3">
                <MarkdownEditor
                  value={editBody}
                  onChange={setEditBody}
                  placeholder="使用 Markdown 编写文档正文"
                  className="min-h-[420px]"
                />
              </div>
            </div>
          )}
        </section>
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col">
      <Header
        title="文档"
        actions={
          <HeaderActions
            primary={
              <Select
                value={activeProjectId || undefined}
                onValueChange={handleProjectChange}
                disabled={projectsLoading || projects.length === 0}
              >
                <SelectTrigger
                  className="w-48"
                  data-testid="documents-project-select"
                >
                  <SelectValue placeholder="选择项目">
                    {activeProject?.name}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {projects.map((project) => (
                    <SelectItem key={project.id} value={project.id}>
                      {project.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            }
          />
        }
      />
      {mainContent}
    </div>
  );
}
