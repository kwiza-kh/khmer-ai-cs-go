"use client";

// Personal profile section — identity (read-only), editable contact details,
// and the linked sign-in methods.
import * as React from "react";
import { getProfile, putProfile, uploadAvatar, type UserProfile } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import {
  UserRound, Mail, Building2, Phone, Save, Loader2, Camera, Upload, Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";

export function ProfileCard() {
  const { t } = useI18n();
  const [profile, setProfile] = React.useState<UserProfile | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [displayName, setDisplayName] = React.useState("");
  const [jobTitle, setJobTitle] = React.useState("");
  const [phone, setPhone] = React.useState("");
  const [timezone, setTimezone] = React.useState("Asia/Phnom_Penh");
  const [avatarUrl, setAvatarUrl] = React.useState("");
  const [uploading, setUploading] = React.useState(false);
  const fileRef = React.useRef<HTMLInputElement | null>(null);

  const onPickFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ""; // allow re-picking the same file
    if (!file) return;
    if (file.size > 2 * 1024 * 1024) {
      toast.error(t("settings.avatarTooLarge"));
      return;
    }
    setUploading(true);
    try {
      const updated = await uploadAvatar(file);
      setProfile(updated);
      setAvatarUrl(updated.avatar_url || "");
      toast.success(t("settings.avatarSaved"));
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.avatarUploadFailed"));
    } finally {
      setUploading(false);
    }
  };

  const removeAvatar = async () => {
    setSaving(true);
    try {
      const updated = await putProfile({ avatar_url: "" });
      setProfile(updated);
      setAvatarUrl("");
      toast.success(t("settings.saved"));
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.saveFailed"));
    } finally {
      setSaving(false);
    }
  };

  const load = React.useCallback(() => {
    setLoading(true);
    getProfile()
      .then((p) => {
        setProfile(p);
        setDisplayName(p.display_name || "");
        setJobTitle(p.job_title || "");
        setPhone(p.phone || "");
        setTimezone(p.timezone || "Asia/Phnom_Penh");
        setAvatarUrl(p.avatar_url || "");
      })
      .catch((err: unknown) => toast.error((err as Error).message || t("settings.profileLoadFailed")))
      .finally(() => setLoading(false));
  }, [t]);

  React.useEffect(() => { load(); }, [load]);

  const save = async () => {
    setSaving(true);
    try {
      const updated = await putProfile({
        display_name: displayName,
        job_title: jobTitle,
        phone,
        timezone,
      });
      setProfile(updated);
      toast.success(t("settings.saved"));
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.saveFailed"));
    } finally {
      setSaving(false);
    }
  };

  const initial = (profile?.display_name || profile?.username || "?").trim().charAt(0).toUpperCase();

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <UserRound className="size-4" /> {t("settings.profile")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-5">
        {loading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" /> {t("common.loading")}
          </div>
        ) : !profile ? null : (
          <>
            {/* Identity header: avatar + name + role + sign-in methods */}
            <div className="flex items-center gap-4">
              <div className="flex size-14 shrink-0 items-center justify-center overflow-hidden rounded-full bg-primary/10 text-lg font-semibold text-primary ring-1 ring-border">
                {avatarUrl ? (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={avatarUrl} alt="" className="size-full object-cover" />
                ) : initial}
              </div>
              <div className="min-w-0">
                <p className="truncate text-sm font-semibold">
                  {profile.display_name || profile.username}
                </p>
                <p className="truncate text-xs text-muted-foreground">
                  {profile.job_title || t("settings.noJobTitle")}
                </p>
                <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                  <Badge
                    variant={profile.role === "platform_admin" || profile.role === "admin" ? "info" : "secondary"}
                    className="text-[10px]"
                  >
                    {profile.role}
                  </Badge>
                  {profile.has_google && <Badge variant="outline" className="text-[10px]">Google</Badge>}
                  {profile.has_password && (
                    <Badge variant="outline" className="text-[10px]">{t("settings.passwordLogin")}</Badge>
                  )}
                </div>
              </div>
            </div>

            {/* Read-only identity — 用文本行而不是 disabled input:
                输入框里的长邮箱/用户名会被硬切且无法横向滚动, 且 disabled
                样式让它们读起来像坏掉的表单。改为带 title 的省略号文本行。 */}
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="min-w-0">
                <p className="text-xs text-muted-foreground">{t("settings.username")}</p>
                <p
                  className="mt-1 truncate rounded-lg border border-border bg-muted/40 px-2.5 py-2 text-sm font-medium"
                  title={profile.username}
                >
                  {profile.username}
                </p>
              </div>
              <div className="min-w-0">
                <p className="text-xs text-muted-foreground">{t("settings.email")}</p>
                <p
                  className="mt-1 flex min-w-0 items-center gap-2 rounded-lg border border-border bg-muted/40 px-2.5 py-2 text-sm font-medium"
                  title={profile.email}
                >
                  <Mail className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{profile.email}</span>
                </p>
              </div>
            </div>

            {/* Editable details */}
            <div className="space-y-3 border-t border-border pt-4">
              <p className="text-xs font-medium text-muted-foreground">{t("settings.editableInfo")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="min-w-0 sm:col-span-2">
                  <Label className="text-xs text-muted-foreground">{t("settings.displayName")}</Label>
                  <Input
                    value={displayName}
                    onChange={(e) => setDisplayName(e.target.value)}
                    placeholder={profile.username}
                    maxLength={80}
                    className="mt-1 h-9 text-sm"
                  />
                </div>
                <div>
                  <Label className="text-xs text-muted-foreground">{t("settings.jobTitle")}</Label>
                  <div className="relative mt-1">
                    <Building2 className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={jobTitle}
                      onChange={(e) => setJobTitle(e.target.value)}
                      placeholder={t("settings.jobTitlePlaceholder")}
                      maxLength={80}
                      className="h-9 pl-8 text-sm"
                    />
                  </div>
                </div>
                <div>
                  <Label className="text-xs text-muted-foreground">{t("settings.phone")}</Label>
                  <div className="relative mt-1">
                    <Phone className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={phone}
                      onChange={(e) => setPhone(e.target.value)}
                      placeholder="+855 …"
                      type="tel"
                      inputMode="tel"
                      maxLength={32}
                      className="h-9 pl-8 text-sm"
                    />
                  </div>
                </div>
                <div>
                  <Label className="text-xs text-muted-foreground">{t("settings.timezone")}</Label>
                  <Select value={timezone} onValueChange={(v) => setTimezone(v ?? "Asia/Phnom_Penh")}>
                    <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="Asia/Phnom_Penh">Asia/Phnom_Penh (UTC+7)</SelectItem>
                      <SelectItem value="Asia/Bangkok">Asia/Bangkok (UTC+7)</SelectItem>
                      <SelectItem value="Asia/Shanghai">Asia/Shanghai (UTC+8)</SelectItem>
                      <SelectItem value="Asia/Singapore">Asia/Singapore (UTC+8)</SelectItem>
                      <SelectItem value="Asia/Ho_Chi_Minh">Asia/Ho_Chi_Minh (UTC+7)</SelectItem>
                      <SelectItem value="UTC">UTC</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
              {/* Avatar upload — click the circle or the button to pick an image */}
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.avatar")}</Label>
                <div className="mt-1.5 flex items-center gap-3">
                  <button
                    type="button"
                    onClick={() => fileRef.current?.click()}
                    disabled={uploading}
                    title={t("settings.avatarUpload")}
                    className="group relative flex size-14 shrink-0 items-center justify-center overflow-hidden rounded-full bg-primary/10 text-lg font-semibold text-primary ring-1 ring-border transition-colors hover:ring-primary/50 disabled:opacity-60"
                  >
                    {avatarUrl ? (
                      // eslint-disable-next-line @next/next/no-img-element
                      <img src={avatarUrl} alt="" className="size-full object-cover" />
                    ) : initial}
                    <span className="absolute inset-0 flex items-center justify-center bg-black/50 opacity-0 transition-opacity group-hover:opacity-100">
                      {uploading ? (
                        <Loader2 className="size-4 animate-spin text-white" />
                      ) : (
                        <Camera className="size-4 text-white" />
                      )}
                    </span>
                  </button>
                  <div className="min-w-0 space-y-1.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() => fileRef.current?.click()}
                        disabled={uploading}
                        className="h-8 gap-1.5 text-xs"
                      >
                        {uploading ? <Loader2 className="size-3.5 animate-spin" /> : <Upload className="size-3.5" />}
                        {uploading ? t("settings.avatarUploading") : t("settings.avatarUpload")}
                      </Button>
                      {avatarUrl && (
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          onClick={removeAvatar}
                          disabled={uploading || saving}
                          className="h-8 gap-1.5 text-xs text-danger hover:bg-destructive/10 hover:text-danger"
                        >
                          <Trash2 className="size-3.5" />
                          {t("settings.avatarRemove")}
                        </Button>
                      )}
                    </div>
                    <p className="text-[11px] text-muted-foreground">{t("settings.avatarHint")}</p>
                  </div>
                  <input
                    ref={fileRef}
                    type="file"
                    accept="image/jpeg,image/png,image/webp,image/gif"
                    className="hidden"
                    onChange={onPickFile}
                  />
                </div>
              </div>
              <Button size="sm" onClick={save} disabled={saving} className="gap-1.5">
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                {t("common.save")}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
