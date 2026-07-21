import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { HeaderActions } from "@/components/layout/header-actions";
import { SettingsPageShell } from "@/routes/settings/layout";
import { useAuthStore } from "@/lib/store/auth-store";
import { authApi } from "@/lib/api/auth";
import { toast } from "sonner";
import { useEffect, useRef, useState } from "react";
import { Camera } from "lucide-react";

const profileSchema = z.object({
  username: z.string().min(2, "用户名至少 2 个字符"),
  email: z.email("请输入有效的邮箱"),
});

type ProfileInput = z.infer<typeof profileSchema>;

export default function ProfilePage() {
  const { user, setUser, token } = useAuthStore();
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<ProfileInput>({
    resolver: zodResolver(profileSchema),
    defaultValues: {
      username: user?.username || "",
      email: user?.email || "",
    },
  });

  useEffect(() => {
    reset({
      username: user?.username || "",
      email: user?.email || "",
    });
  }, [reset, user]);

  const onSubmit = async (data: ProfileInput) => {
    try {
      const res = await authApi.updateMe(data);
      setUser(res.data);
      toast.success("个人信息已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const handleAvatarClick = () => {
    fileInputRef.current?.click();
  };

  const handleAvatarChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;

    const allowedTypes = ["image/jpeg", "image/png", "image/gif", "image/webp"];
    if (!allowedTypes.includes(file.type)) {
      toast.error("仅支持 jpg、png、gif、webp 格式的图片");
      return;
    }

    if (file.size > 5 * 1024 * 1024) {
      toast.error("头像文件大小不能超过 5MB");
      return;
    }

    setUploading(true);
    try {
      const formData = new FormData();
      formData.append("file", file);
      const res = await authApi.uploadAvatar(formData);
      setUser(res.data);
      toast.success("头像已更新");
    } catch {
      toast.error("头像上传失败");
    } finally {
      setUploading(false);
      if (fileInputRef.current) {
        fileInputRef.current.value = "";
      }
    }
  };

  const avatarSrc = user?.avatar_url && token
    ? `${user.avatar_url}?token=${token}`
    : undefined;
  const initial = user?.username?.charAt(0).toUpperCase() || "U";

  return (
    <SettingsPageShell
      actions={
        <HeaderActions
          primary={
            <Button type="submit" form="profile-form" size="sm" disabled={isSubmitting}>
              {isSubmitting ? "保存中..." : "保存"}
            </Button>
          }
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">个人信息</h2>
          <p className="text-sm text-muted-foreground">修改你的头像、用户名和邮箱</p>
        </div>

        <div className="flex flex-col items-start gap-4">
          <div className="relative">
            <Avatar
              className="h-20 w-20 cursor-pointer"
              onClick={handleAvatarClick}
            >
              {avatarSrc && <AvatarImage src={avatarSrc} alt={user?.username} />}
              <AvatarFallback className="bg-primary text-primary-foreground text-2xl font-medium">
                {initial}
              </AvatarFallback>
            </Avatar>
            <button
              type="button"
              onClick={handleAvatarClick}
              disabled={uploading}
              className="absolute -right-1 -bottom-1 flex h-7 w-7 items-center justify-center rounded-full bg-background border shadow-sm hover:bg-accent disabled:opacity-50"
            >
              <Camera className="h-3.5 w-3.5" />
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept="image/jpeg,image/png,image/gif,image/webp"
              className="hidden"
              onChange={handleAvatarChange}
            />
          </div>
          {uploading && (
            <p className="text-xs text-muted-foreground">上传中...</p>
          )}
        </div>

        <form id="profile-form" onSubmit={handleSubmit(onSubmit)} className="space-y-4 max-w-xl">
          <div className="space-y-2">
            <Label htmlFor="username">用户名</Label>
            <Input id="username" {...register("username")} />
            {errors.username && (
              <p className="text-xs text-destructive">
                {errors.username.message}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="email">邮箱</Label>
            <Input id="email" type="email" {...register("email")} />
            {errors.email && (
              <p className="text-xs text-destructive">
                {errors.email.message}
              </p>
            )}
          </div>
        </form>
      </div>
    </SettingsPageShell>
  );
}
