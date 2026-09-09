"use client";

// Personal profile section — identity (read-only), editable contact details,
// and the linked sign-in methods.
import * as React from "react";
import { getProfile, putProfile, type UserProfile } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import {
  UserRound, Mail, Building2, Phone, Save, Loader2,
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
        avatar_url: avatarUrl,
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

            {/* Read-only identity */}
            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.username")}</Label>
                <Input value={profile.username} disabled className="mt-1 h-9 text-sm" />
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.email")}</Label>
                <div className="relative mt-1">
                  <Mail className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <Input value={profile.email} disabled className="h-9 pl-8 text-sm" />
                </div>
              </div>
            </div>

            {/* Editable details */}
            <div className="space-y-3 border-t border-border pt-4">
              <p className="text-xs font-medium text-muted-foreground">{t("settings.editableInfo")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
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
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.avatarUrl")}</Label>
                <Input
                  value={avatarUrl}
                  onChange={(e) => setAvatarUrl(e.target.value)}
                  placeholder="https://…"
                  className="mt-1 h-9 text-sm"
                />
                <p className="mt-1 text-[11px] text-muted-foreground">{t("settings.avatarHint")}</p>
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
