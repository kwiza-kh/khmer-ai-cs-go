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

export default function SettingsPage() {
  const { user } = useAuth();
  const [lang, setLang] = React.useState("auto");
  const [notif, setNotif] = React.useState("all");

  // Load persisted preferences; fall back to localStorage/defaults.
  React.useEffect(() => {
    let cancelled = false;
    apiFetch<{ language: string; notification_pref: string }>("/auth/preferences")
      .then((prefs) => {
        if (cancelled) return;
        setLang(prefs.language || "auto");
        setNotif(prefs.notification_pref || "all");
      })
      .catch(() => {
        if (cancelled) return;
        setLang(localStorage.getItem("lang") || "km");
      });
    return () => { cancelled = true; };
  }, []);

  const savePrefs = (patch: { language?: string; notification_pref?: string }) => {
    if (patch.language !== undefined) {
      setLang(patch.language);
      localStorage.setItem("lang", patch.language);
    }
    if (patch.notification_pref !== undefined) setNotif(patch.notification_pref);
    apiFetch("/auth/preferences", { method: "PUT", body: JSON.stringify(patch) })
      .then(() => toast.success("Preferences saved"))
      .catch((err) => toast.error((err as Error).message || "Could not save preferences"));
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={SettingsIcon}
        kicker="Account"
        title="Settings"
        description="Manage your profile, password, and language."
      />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="max-w-2xl mx-auto space-y-5">

          {/* Profile */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><SettingsIcon className="size-4" /> Profile</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div>
                <Label className="text-xs text-muted-foreground">Username</Label>
                <Input value={user?.username ?? ""} disabled className="mt-1 h-9 text-sm" />
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">Email</Label>
                <Input value={user?.email ?? ""} disabled className="mt-1 h-9 text-sm" />
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">Role</Label>
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
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Globe className="size-4" /> Language</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div>
                <Label className="text-xs text-muted-foreground">AI reply language</Label>
                <Select value={lang} onValueChange={(v) => savePrefs({ language: v || "auto" })}>
                  <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">Auto — match the customer&apos;s language</SelectItem>
                    <SelectItem value="km">ខ្មែរ (Khmer)</SelectItem>
                    <SelectItem value="en">English</SelectItem>
                    <SelectItem value="zh">中文 (Chinese)</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground mt-1">Auto replies in the customer&apos;s language; a fixed choice forces every AI reply into that language.</p>
              </div>
              <div>
                <Label className="text-xs text-muted-foreground">Notifications</Label>
                <Select value={notif} onValueChange={(v) => savePrefs({ notification_pref: v || "all" })}>
                  <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">All events</SelectItem>
                    <SelectItem value="escalations">Escalations only</SelectItem>
                    <SelectItem value="none">None</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>

          {/* Tenant settings — every user manages their own schedule + quick replies */}
          <div className="pt-1">
            <p className="text-xs font-medium text-muted-foreground flex items-center gap-1.5 mb-3">
              <Clock className="size-3.5" /> Customer-service settings <span className="text-[10px]">(yours only — other accounts have their own)</span>
            </p>
            <div className="space-y-5">
              <BusinessHoursCard />
              <CannedResponsesCard />
            </div>
          </div>

          <p className="text-center text-xs text-muted-foreground py-2">
            Khmer AI Customer Service · v1.0
          </p>
        </div>
      </div>
    </div>
  );
}

function ChangePasswordCard() {
  const [old, setOld] = React.useState("");
  const [next, setNext] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  const submit = async () => {
    if (!old || !next || !confirm) { toast.error("请填写所有字段"); return; }
    if (next !== confirm) { toast.error("两次输入的新密码不一致"); return; }
    if (next.length < 6) { toast.error("新密码至少 6 位"); return; }
    setSaving(true);
    try {
      await apiFetch("/auth/password", {
        method: "PUT",
        body: JSON.stringify({ old_password: old, new_password: next }),
      });
      toast.success("密码已更新");
      setOld(""); setNext(""); setConfirm("");
    } catch (err) {
      // Endpoint may not exist yet — surface the message gracefully.
      const msg = err instanceof ApiError ? err.message : (err as Error).message;
      toast.error(`Password change unavailable: ${msg}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Lock className="size-4" /> Change password</CardTitle></CardHeader>
      <CardContent className="space-y-3">
        <div>
          <Label className="text-xs text-muted-foreground">Current password</Label>
          <Input type="password" value={old} onChange={(e) => setOld(e.target.value)} className="mt-1 h-9 text-sm" />
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <Label className="text-xs text-muted-foreground">New password</Label>
            <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} className="mt-1 h-9 text-sm" />
          </div>
          <div>
            <Label className="text-xs text-muted-foreground">Confirm</Label>
            <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className="mt-1 h-9 text-sm" />
          </div>
        </div>
        <Button onClick={submit} disabled={saving} className="h-8 text-xs gap-1.5">
          {saving ? <Loader2 className="size-3 animate-spin" /> : <Lock className="size-3" />} Update password
        </Button>
      </CardContent>
    </Card>
  );
}

function TwoFactorCard() {
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
      toast.success("已生成密钥，请在验证器中扫码后输入验证码");
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  const verify = async () => {
    if (!code.trim()) { toast.error("请输入验证码"); return; }
    setBusy(true);
    try {
      await totpVerify(code);
      setEnabled(true);
      setSecret(""); setCode("");
      toast.success("2FA 已启用");
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  const disable = async () => {
    if (!code.trim()) { toast.error("请输入当前验证码"); return; }
    setBusy(true);
    try {
      await totpDisable(code);
      setEnabled(false);
      setCode("");
      toast.success("2FA 已禁用");
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <Card>
      <CardHeader><CardTitle className="text-sm flex items-center gap-2"><ShieldCheck className="size-4" /> Two-factor authentication</CardTitle></CardHeader>
      <CardContent className="space-y-3">
        {enabled === null ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : enabled ? (
          <>
            <Badge variant="success" className="text-xs">已启用</Badge>
            <div>
              <Label className="text-xs text-muted-foreground">当前验证码</Label>
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="6 位验证码" className="mt-1 h-9 text-sm" inputMode="numeric" />
            </div>
            <Button onClick={disable} disabled={busy} variant="destructive" className="h-8 text-xs">禁用 2FA</Button>
          </>
        ) : secret ? (
          <>
            <p className="text-xs text-muted-foreground">在 Google Authenticator / 1Password / Authy 中扫码或手动输入密钥:</p>
            <code className="block rounded bg-muted px-2 py-1 text-xs break-all">{secret}</code>
            <div>
              <Label className="text-xs text-muted-foreground">验证码</Label>
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="6 位验证码" className="mt-1 h-9 text-sm" inputMode="numeric" />
            </div>
            <Button onClick={verify} disabled={busy} className="h-8 text-xs gap-1.5">{busy ? <Loader2 className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />}验证并启用</Button>
          </>
        ) : (
          <>
            <p className="text-xs text-muted-foreground">开启两步验证后，登录需要密码 + 验证器动态码，账户更安全。</p>
            <Button onClick={setup} disabled={busy} className="h-8 text-xs gap-1.5">{busy ? <Loader2 className="size-3 animate-spin" /> : <ShieldCheck className="size-3" />}开启 2FA</Button>
          </>
        )}
      </CardContent>
    </Card>
  );
}
