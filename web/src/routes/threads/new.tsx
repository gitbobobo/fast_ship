import { useState } from "react";
import { Link } from "react-router";
import { ArrowLeft, Settings } from "lucide-react";
import { Header } from "@/components/layout/header";
import { NewThreadForm } from "@/components/threads/new-thread-form";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useEnvironments } from "@/lib/hooks/use-threads";
import { canOperate } from "@/lib/threads-compose";

/**
 * 新建线程：独立 composer。发送首条消息时才创建线程；
 * 默认选第一个可操作的环境，只读环境在下拉里置灰。
 */
export default function NewThreadPage() {
  const { data: environments, isLoading, isError, refetch } = useEnvironments();
  const [picked, setPicked] = useState("");
  const [text, setText] = useState("");

  const selected =
    environments?.find((e) => e.id === picked && canOperate(e)) ??
    environments?.find(canOperate) ??
    environments?.[0];

  return (
    <>
      <Header title="新建线程" />
      <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-5 p-4 md:p-6">
        <Link
          to="/threads"
          className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" />
          全部线程
        </Link>
        {isLoading ? (
          <Skeleton className="h-40 rounded-2xl" />
        ) : !environments ? (
          <div className="py-16 text-center text-sm text-muted-foreground">
            <p>{isError ? "环境加载失败" : "暂无环境信息"}</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
              重试
            </Button>
          </div>
        ) : !selected ? (
          <div className="mx-auto flex max-w-sm flex-col items-center gap-3 py-16 text-center">
            <h2 className="text-base font-medium">还没有连接环境</h2>
            <p className="text-sm text-muted-foreground">
              先在设置中粘贴 T3 Code 的配对链接，添加环境后即可新建线程。
            </p>
            <Button render={<Link to="/settings/environments" />}>
              <Settings className="mr-1.5 h-4 w-4" />
              去设置配对
            </Button>
          </div>
        ) : (
          <NewThreadForm
            key={selected.id}
            environment={selected}
            environments={environments}
            onEnvironmentChange={setPicked}
            text={text}
            onTextChange={setText}
          />
        )}
      </div>
    </>
  );
}
