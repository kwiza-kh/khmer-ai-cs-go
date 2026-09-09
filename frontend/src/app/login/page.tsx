"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { AnimatePresence, motion } from "motion/react";
import { Loader2, Eye, EyeOff, UserRound, Lock, Mail, ShieldCheck } from "lucide-react";
import { cn } from "@/lib/utils";
import { LANGS, useI18n } from "@/lib/i18n";

export default function LoginPage() {
  const { login, register } = useAuth();
  const router = useRouter();
  const { lang, setLang, t } = useI18n();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [tab, setTab] = useState("login");
  const [showPassword, setShowPassword] = useState(false);

  const [loginUser, setLoginUser] = useState("");
  const [loginPass, setLoginPass] = useState("");
  const [loginCode, setLoginCode] = useState("");
  const [need2fa, setNeed2fa] = useState(false);
  const [regUser, setRegUser] = useState("");
  const [regEmail, setRegEmail] = useState("");
  const [regPass, setRegPass] = useState("");

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault(); setError(""); setLoading(true);
    try {
      const result = await login(loginUser, loginPass, need2fa ? loginCode.trim() : undefined);
      if (result.twoFactorRequired) {
        // Account has 2FA: reveal the code field and ask the user to complete
        // the second factor (previously this dead-ended with a raw 401).
        setNeed2fa(true);
        setError("");
      } else {
        router.push("/ai-test");
      }
    }
    catch (err: unknown) { setError((err as Error).message); }
    finally { setLoading(false); }
  };

  const handleRegister = async (e: React.FormEvent) => {
    e.preventDefault(); setError(""); setLoading(true);
    try { await register(regUser, regEmail, regPass); router.push("/ai-test"); }
    catch (err: unknown) { setError((err as Error).message); }
    finally { setLoading(false); }
  };

  return (
    <div className="relative min-h-[100dvh] bg-background flex flex-col items-center justify-center p-6 overflow-hidden">
      {/* Ambient: 极光 + 细网格 */}
      <div aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden">
        <div className="aurora" />
        <div className="grid-texture absolute inset-0" />
      </div>

      {/* 界面语言切换 — 分段控件 (全局, 与设置页/侧边栏联动) */}
      <div className="glass absolute top-5 right-5 z-20 flex items-center gap-0.5 rounded-full border border-border/60 p-1 shadow-[0_2px_10px_-4px_rgb(0_0_0/0.15)] dark:bg-white/[0.05]">
        {LANGS.map((opt) => (
          <button key={opt.key} onClick={() => setLang(opt.key)}
            className={cn(
              "rounded-full px-3 py-1 text-xs font-medium transition-all",
              lang === opt.key
                ? "bg-primary text-primary-foreground shadow-[0_2px_8px_-2px_color-mix(in_oklch,var(--color-primary)_55%,transparent)]"
                : "text-muted-foreground hover:text-foreground"
            )}
          >{opt.label}</button>
        ))}
      </div>

      <div className="relative w-full max-w-[380px]">
        {/* Heading — the reference design leads with a plain centred title,
            no logo block, no card chrome. */}
        <div className="mb-7 text-center">
          <h1 className="text-[28px] font-semibold tracking-tight text-foreground">
            {tab === "login" ? t("login.title") : t("login.registerTitle")}
          </h1>
          <p className="mt-1.5 text-sm text-muted-foreground">
            {tab === "login" ? t("login.desc") : t("login.registerDesc")}
          </p>
        </div>

        {/* Segmented control — pill track with the active side raised. */}
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList className="grid h-11 w-full grid-cols-2 gap-1 rounded-2xl bg-muted/70 p-1">
            <TabsTrigger
              value="login"
              className="rounded-xl text-sm font-medium data-[state=active]:bg-background data-[state=active]:shadow-sm"
            >
              {t("login.tab")}
            </TabsTrigger>
            <TabsTrigger
              value="register"
              className="rounded-xl text-sm font-medium data-[state=active]:bg-background data-[state=active]:shadow-sm"
            >
              {t("login.registerTab")}
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <AnimatePresence>
          {error && (
            <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }}>
              <div className="mt-4 rounded-xl border border-destructive/25 bg-destructive/10 px-3.5 py-2.5">
                <p className="text-sm text-destructive">{error}</p>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        <div className="mt-5">
          <AnimatePresence mode="wait">
            {tab === "login" ? (
              <motion.form key="login" initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleLogin} className="space-y-3.5">
                <IconField icon={UserRound}>
                  <Input value={loginUser} onChange={(e) => setLoginUser(e.target.value)}
                    placeholder={t("login.userPlaceholder")} required autoComplete="username"
                    className="h-12 rounded-2xl border-border/70 bg-card/70 pl-11 text-sm shadow-none" />
                </IconField>
                <IconField icon={Lock}>
                  <Input type={showPassword ? "text" : "password"} value={loginPass}
                    onChange={(e) => setLoginPass(e.target.value)} placeholder="········" required
                    autoComplete="current-password"
                    className="h-12 rounded-2xl border-border/70 bg-card/70 pl-11 pr-11 text-sm shadow-none" />
                  <button type="button" onClick={() => setShowPassword(!showPassword)}
                    aria-label={showPassword ? t("login.hidePassword") : t("login.showPassword")}
                    className="absolute right-3.5 top-1/2 -translate-y-1/2 text-muted-foreground transition-colors hover:text-foreground">
                    {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                  </button>
                </IconField>
                {need2fa && (
                  <IconField icon={ShieldCheck}>
                    <Input value={loginCode} onChange={(e) => setLoginCode(e.target.value)}
                      placeholder={t("login.totpPlaceholder")} inputMode="numeric" autoComplete="one-time-code"
                      maxLength={6} required autoFocus
                      className="h-12 rounded-2xl border-border/70 bg-card/70 pl-11 text-center text-sm tracking-[0.4em] shadow-none" />
                  </IconField>
                )}
                <button type="submit" disabled={loading || (need2fa && loginCode.trim().length !== 6)}
                  className="inline-flex h-12 w-full items-center justify-center gap-2 rounded-2xl bg-foreground text-base font-semibold text-background transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60">
                  {loading ? <Loader2 className="size-4 animate-spin" /> : t("login.submit")}
                </button>
              </motion.form>
            ) : (
              <motion.form key="register" initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleRegister} className="space-y-3.5">
                <IconField icon={UserRound}>
                  <Input value={regUser} onChange={(e) => setRegUser(e.target.value)}
                    placeholder={t("login.userPlaceholder")} required autoComplete="username"
                    className="h-12 rounded-2xl border-border/70 bg-card/70 pl-11 text-sm shadow-none" />
                </IconField>
                <IconField icon={Mail}>
                  <Input type="email" value={regEmail} onChange={(e) => setRegEmail(e.target.value)}
                    placeholder={t("login.emailPlaceholder")} required autoComplete="email"
                    className="h-12 rounded-2xl border-border/70 bg-card/70 pl-11 text-sm shadow-none" />
                </IconField>
                <IconField icon={Lock}>
                  <Input type="password" value={regPass} onChange={(e) => setRegPass(e.target.value)}
                    placeholder="········" required autoComplete="new-password"
                    className="h-12 rounded-2xl border-border/70 bg-card/70 pl-11 text-sm shadow-none" />
                </IconField>
                <button type="submit" disabled={loading}
                  className="inline-flex h-12 w-full items-center justify-center gap-2 rounded-2xl bg-foreground text-base font-semibold text-background transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60">
                  {loading ? <Loader2 className="size-4 animate-spin" /> : t("login.registerSubmit")}
                </button>
              </motion.form>
            )}
          </AnimatePresence>
        </div>

        <p className="mt-7 text-center text-sm text-muted-foreground">
          {tab === "login" ? t("login.noAccount") : t("login.haveAccount")}{" "}
          <button
            type="button"
            onClick={() => { setError(""); setTab(tab === "login" ? "register" : "login"); }}
            className="font-semibold text-foreground underline-offset-4 hover:underline"
          >
            {tab === "login" ? t("login.registerTab") : t("login.tab")}
          </button>
        </p>

        <p className="mt-6 text-center text-[11px] tracking-wide text-muted-foreground/60">
          {t("login.footer")}
        </p>
      </div>
    </div>
  );
}

// Icon-prefixed input wrapper — the leading icon sits inside the rounded field.
function IconField({ icon: Icon, children }: { icon: React.ComponentType<{ className?: string }>; children: React.ReactNode }) {
  return (
    <div className="relative">
      <Icon className="pointer-events-none absolute left-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
      {children}
    </div>
  );
}
