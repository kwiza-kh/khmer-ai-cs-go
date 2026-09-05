"use client";

import * as React from "react";
import { useAuth } from "@/lib/auth-client";
import { apiFetch, ApiError, totpStatus, totpSetup, totpVerify, totpDisable } from "@/lib/api";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import { Settings as SettingsIcon, Lock, Globe, Loader2, Clock, ShieldCheck } from "lucide-react";
import { BusinessHoursCard, CannedResponsesCard } from "@/components/admin/operations-tab";
import { toast } from "sonner";
import { useI18n, type Lang } from "@/lib/i18n";

export default function SettingsPage() {
  const { user } = useAuth();
  const { setLang: setUiLang, t } = useI18n();
  // AI language preference (persisted server-side; "auto" follows the customer).
  const [aiLang, setAiLang] = React.useState("auto");
  const [notif, setNotif] = React.useState("all");

  // Load persisted preferences; fall back to defaults.
  React.useEffect(() => {
    let cancelled = false;
    apiFetch<{ language: string; notification_pref: string }>("/auth/preferences")
      .then((prefs) => {
        if (cancelled) return;
        setAiLang(prefs.language || "auto");
        setNotif(prefs.notification_pref || "all");
      })
      .catch(() => undefined);
    return () => { cancelled = true; };
  }, []);

  const savePrefs = (patch: { language?: string; notification_pref?: string }) => {
    if (patch.language !== undefined) {
      setAiLang(patch.language);
      // A concrete language choice also switches the interface immediately.
      if (patch.language !== "auto") {
        setUiLang(patch.language as Lang);
      }
    }
    if (patch.notification_pref !== undefined) setNotif(patch.notification_pref);
    apiFetch("/auth/preferences", { method: "PUT", body: JSON.stringify(patch) })
      .then(() => toast.success(t("settings.saved")))
      .catch((err) => toast.error((err as Error).message || t("settings.saveFailed")));
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={SettingsIcon}
        kicker={t("settings.kicker")}
        title={t("settings.title")}
        description={t("settings.description")}
      />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="max-w-2xl mx-auto space-y-5">

          {/* Profile */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><SettingsIcon className="size-4" /> {t("settings.profile")}</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.username")}</Label>
                <Input value={user?.username ?? ""} disabled className="mt-1 h-9 text-sm" />
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.email")}</Label>
                <Input value={user?.email ?? ""} disabled className="mt-1 h-9 text-sm" />
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.role")}</Label>
                <div className="mt-1">
                  <Badge variant={user?.role === "admin" ? "info" : "secondary"} className="text-xs">{user?.role}</Badge>
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Change password */}
          <ChangePasswordCard />

          {/* Two-factor authentication */}
          <TwoFactorCard />

          {/* Language preference */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Globe className="size-4" /> {t("settings.language")}</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.language")}</Label>
                <Select value={aiLang} onValueChange={(v) => savePrefs({ language: v || "auto" })}>
                  <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">{t("settings.langAuto")}</SelectItem>
                    <SelectItem value="km">ខ្មែរ (Khmer)</SelectItem>
                    <SelectItem value="en">English</SelectItem>
                    <SelectItem value="zh">中文 (Chinese)</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground mt-1">{t("settings.languageHint")}</p>
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">{t("settings.notifications")}</Label>
                <Select value={notif} onValueChange={(v) => savePrefs({ notification_pref: v || "all" })}>
                  <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">{t("settings.notifAll")}</SelectItem>
                    <SelectItem value="escalations">{t("settings.notifEscalations")}</SelectItem>
                    <SelectItem value="none">{t("settings.notifNone")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>

          {/* Tenant settings — every user manages their own schedule + quick replies */}
          <div className="pt-1">
            <p className="text-xs font-medium text-muted-foreground flex items-center gap-1.5 mb-3">
              <Clock className="size-3.5" /> {t("settings.csSettings")} <span className="text-[10px]">({t("settings.yoursOnly")})</span>
            </p>
            <div className="space-y-5">
              <BusinessHoursCard />
              <CannedResponsesCard />
            </div>
          </div>

          <p className="text-center text-xs text-muted-foreground py-2">
            {t("settings.version")}
          </p>
        </div>
      </div>
    </div>
  );
}

function ChangePasswordCard() {
  const { t, tf } = useI18n();
  const [old, setOld] = React.useState("");
  const [next, setNext] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  const submit = async () => {
    if (!old || !next || !confirm) { toast.error(t("settings.pwdFillAll")); return; }
    if (next !== confirm) { toast.error(t("settings.pwdMismatch")); return; }
    if (next.length < 6) { toast.error(t("settings.pwdTooShort")); return; }
    setSaving(true);
    try {
      await apiFetch("/auth/password", {
        method: "PUT",
        body: JSON.stringify({ old_password: old, new_password: next }),
      });
      toast.success(t("settings.pwdUpdated"));
      setOld(""); setNext(""); setConfirm("");
    } catch (err) {
      // Endpoint may not exist yet — surface the message gracefully.
      const msg = err instanceof ApiError ? err.message : (err as Error).message;
      toast.error(tf("settings.pwdFailed", { msg }));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Lock className="size-4" /> {t("settings.changePassword")}</CardTitle></CardHeader>
      <CardContent className="space-y-3">
        <div>
          <Label className="text-xs text-muted-foreground">{t("settings.currentPassword")}</Label>
          <Input type="password" value={old} onChange={(e) => setOld(e.target.value)} className="mt-1 h-9 text-sm" />
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <Label className="text-xs text-muted-foreground">{t("settings.newPassword")}</Label>
            <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} className="mt-1 h-9 text-sm" />
          </div>
          <div>
            <Label className="text-xs text-muted-foreground">{t("settings.confirmPassword")}</Label>
            <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className="mt-1 h-9 text-sm" />
          </div>
        </div>
        <Button onClick={submit} disabled={saving} className="h-8 text-xs gap-1.5">
          {saving ? <Loader2 className="size-3 animate-spin" /> : <Lock className="size-3" />} {t("settings.updatePassword")}
        </Button>
      </CardContent>
    </Card>
  );
}

function TwoFactorCard() {
  const { t } = useI18n();
  const [enabled, setEnabled] = React.useState<boolean | null>(null);
  const [secret, setSecret] = React.useState("");
  const [code, setCode] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    void totpStatus().then((r) => setEnabled(r.enabled)).catch(() => setEnabled(false));
  }, []);

  const setup = async () => {
    setBusy(true);
    try {
      const r = await totpSetup();
      setSecret(r.secret);
      toast.success(t("settings.totpSetupToast"));
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  const verify = async () => {
    if (!code.trim()) { toast.error(t("settings.totpEnterCode")); return; }
    setBusy(true);
    try {
      await totpVerify(code);
      setEnabled(true);
      setSecret(""); setCode("");
      toast.success(t("settings.totpEnabledToast"));
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  const disable = async () => {
    if (!code.trim()) { toast.error(t("settings.totpEnterCode")); return; }
    setBusy(true);
    try {
      await totpDisable(code);
      setEnabled(false);
      setCode("");
      toast.success(t("settings.totpDisabledToast"));
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <Card>
      <CardHeader><CardTitle className="text-sm flex items-center gap-2"><ShieldCheck className="size-4" /> {t("settings.twoFactor")}</CardTitle></CardHeader>
      <CardContent className="space-y-3">
        {enabled === null ? (
          <p className="text-xs text-muted-foreground">{t("settings.loading")}</p>
        ) : enabled ? (
          <>
            <Badge variant="success" className="text-xs">{t("settings.enabled")}</Badge>
            <div>
              <Label className="text-xs text-muted-foreground">{t("settings.totpCode")}</Label>
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder={t("settings.totpCodePlaceholder")} className="mt-1 h-9 text-sm" inputMode="numeric" />
            </div>
            <Button onClick={disable} disabled={busy} variant="destructive" className="h-8 text-xs">{t("settings.totpDisable")}</Button>
          </>
        ) : secret ? (
          <>
            <p className="text-xs text-muted-foreground">{t("settings.totpScanHint")}</p>
            <code className="block rounded bg-muted px-2 py-1 text-xs break-all">{secret}</code>
            <div>
              <Label className="text-xs text-muted-foreground">{t("settings.totpCode")}</Label>
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder={t("settings.totpCodePlaceholder")} className="mt-1 h-9 text-sm" inputMode="numeric" />
            </div>
            <Button onClick={verify} disabled={busy} className="h-8 text-xs gap-1.5">{busy ? <Loader2 className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />}{t("settings.totpVerify")}</Button>
          </>
        ) : (
          <>
            <p className="text-xs text-muted-foreground">{t("settings.totpIntro")}</p>
            <Button onClick={setup} disabled={busy} className="h-8 text-xs gap-1.5">{busy ? <Loader2 className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />}{t("settings.totpEnable")}</Button>
          </>
        )}
      </CardContent>
    </Card>
  );
}
