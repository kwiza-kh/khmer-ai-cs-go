"use client";

import * as React from "react";
import { useState, useEffect } from "react";
import { useRouter } from "next/navigation";
import { API_BASE, useAuth } from "@/lib/auth-client";
import { peekPendingInvite } from "@/lib/pending-invite";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter,
} from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { AnimatePresence, motion } from "motion/react";
import { Eye, EyeOff, Lock, Mail, UserRound, ShieldCheck, ArrowRight, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { LANGS, useI18n } from "@/lib/i18n";
import { TelegramIcon } from "@/components/platform-icons";

export default function LoginPage() {
  const { login, register, completeGoogleLogin, completeTelegramLogin } = useAuth();
  const router = useRouter();
  // An invite link opened without a session parks its code in sessionStorage
  // (see /join), so a successful sign-in finishes that job instead of dropping
  // the visitor on the dashboard.
  const afterAuthPath = () => {
    const pending = peekPendingInvite();
    return pending ? `/join?code=${encodeURIComponent(pending)}` : "/ai-test";
  };
  const { lang, setLang, t } = useI18n();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"login" | "register">("login");
  const [showPassword, setShowPassword] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);
  const [telegramEnabled, setTelegramEnabled] = useState(false);

  const [loginUser, setLoginUser] = useState("");
  const [loginPass, setLoginPass] = useState("");
  const [loginCode, setLoginCode] = useState("");
  const [need2fa, setNeed2fa] = useState(false);
  const [regUser, setRegUser] = useState("");
  const [regEmail, setRegEmail] = useState("");
  const [regPass, setRegPass] = useState("");

  // OAuth return: the callback redirects here with ?<provider>_code= (success)
  // or ?<provider>_error= (failure). Swap the one-time code for a session, then
  // clean the URL so a refresh cannot replay it. Google and Telegram share this
  // path — same one-time-code shape on both sides.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const googleCode = params.get("google_code");
    const telegramCode = params.get("telegram_code");
    const errReason = params.get("google_error") || params.get("telegram_error");
    if (!googleCode && !telegramCode && !errReason) return;
    window.history.replaceState({}, "", window.location.pathname);
    if (errReason) {
      const key = `login.googleErr.${errReason}`;
      const msg = t(key);
      // The OAuth callback is a one-time external navigation event; this effect
      // is where the redirect is turned into UI state. Deriving the message
      // during render would mean moving the whole callback path onto
      // useSearchParams + Suspense. Pre-existing debt (same class as the
      // inbox/page.tsx entries in docs/DEVELOPMENT.md).
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setError(msg === key ? t("login.googleErr.failed") : msg);
      return;
    }
    setLoading(true);
    // The state travels with the code; the server rejects a code whose state
    // did not originate from this browser (login-CSRF guard).
    const state = params.get("state") ?? "";
    const exchange = telegramCode
      ? completeTelegramLogin(telegramCode, state)
      : completeGoogleLogin(googleCode!, state);
    void exchange
      .then(() => router.push(afterAuthPath()))
      .catch((err: unknown) => setError((err as Error).message))
      .finally(() => setLoading(false));
  }, [completeGoogleLogin, completeTelegramLogin, router, t]);

  // Probe which sign-in providers this deployment has configured.
  useEffect(() => {
    let cancelled = false;
    void fetch(`${API_BASE}/auth/methods`)
      .then((res) => res.json())
      .then((data: { google?: boolean; telegram?: boolean }) => {
        if (cancelled) return;
        setGoogleEnabled(Boolean(data.google));
        setTelegramEnabled(Boolean(data.telegram));
      })
      .catch(() => { /* keep the buttons hidden */ });
    return () => { cancelled = true; };
  }, []);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault(); setError(""); setLoading(true);
    try {
      const result = await login(loginUser, loginPass, need2fa ? loginCode.trim() : undefined);
      if (result.twoFactorRequired) {
        setNeed2fa(true);
        setError("");
      } else {
        router.push(afterAuthPath());
      }
    }
    catch (err: unknown) { setError((err as Error).message); }
    finally { setLoading(false); }
  };

  const handleRegister = async (e: React.FormEvent) => {
    e.preventDefault(); setError(""); setLoading(true);
    try { await register(regUser, regEmail, regPass); router.push(afterAuthPath()); }
    catch (err: unknown) { setError((err as Error).message); }
    finally { setLoading(false); }
  };

  return (
    <section className="fixed inset-0 overflow-y-auto bg-background text-foreground">
      {/* Header */}
      <header className="absolute inset-x-0 top-0 z-10 flex items-center justify-between border-b border-border px-6 py-4">
        <span className="text-xs uppercase tracking-[0.14em] text-muted-foreground">RelayChat</span>
        <div className="flex items-center gap-1 rounded-lg border border-border bg-card p-0.5">
          {LANGS.map((opt) => (
            <button
              key={opt.key}
              onClick={() => setLang(opt.key)}
              className={cn(
                "rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors",
                lang === opt.key ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {opt.label}
            </button>
          ))}
        </div>
      </header>

      {/* Centered card. This page has its own fixed dark design, so every
          text colour is set explicitly — inheriting card-foreground would
          render dark-on-dark when the app theme is light. */}
      <div className="grid min-h-full w-full place-items-center px-4 py-6">
        <Card className="w-full max-w-sm border-border bg-card text-card-foreground">
          <CardHeader className="space-y-1">
            <CardTitle className="text-2xl text-foreground">
              {tab === "login" ? t("login.title") : t("login.registerTitle")}
            </CardTitle>
            <CardDescription className="text-muted-foreground">
              {tab === "login" ? t("login.desc") : t("login.registerDesc")}
            </CardDescription>
          </CardHeader>

          <CardContent className="grid gap-5">
            {/* Segmented control */}
            <div className="grid grid-cols-2 gap-1 rounded-lg border border-border bg-muted/50 p-1">
              {(["login", "register"] as const).map((key) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => { setError(""); setTab(key); }}
                  className={cn(
                    "h-8 rounded-md text-[13px] font-medium transition-colors",
                    tab === key ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {key === "login" ? t("login.tab") : t("login.registerTab")}
                </button>
              ))}
            </div>

            <AnimatePresence>
              {error && (
                <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }}>
                  <div role="alert" aria-live="assertive" className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2">
                    <p className="text-sm text-danger">{error}</p>
                  </div>
                </motion.div>
              )}
            </AnimatePresence>

            <AnimatePresence mode="wait">
              {tab === "login" ? (
                <motion.form key="login" initial={false} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleLogin} className="grid gap-5">
                  <div className="grid gap-2">
                    <Label htmlFor="login-user" className="text-foreground">{t("login.username")}</Label>
                    <div className="relative">
                      <UserRound className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground/70" />
                      <Input
                        id="login-user"
                        value={loginUser}
                        onChange={(e) => setLoginUser(e.target.value)}
                        placeholder={t("login.userPlaceholder")}
                        required
                        autoComplete="username"
                        className="border-input bg-background pl-10 text-foreground placeholder:text-muted-foreground/60"
                      />
                    </div>
                  </div>

                  <div className="grid gap-2">
                    <Label htmlFor="login-pass" className="text-foreground">{t("login.password")}</Label>
                    <div className="relative">
                      <Lock className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground/70" />
                      <Input
                        id="login-pass"
                        type={showPassword ? "text" : "password"}
                        value={loginPass}
                        onChange={(e) => setLoginPass(e.target.value)}
                        placeholder="••••••••"
                        required
                        autoComplete="current-password"
                        className="border-input bg-background pl-10 pr-10 text-foreground placeholder:text-muted-foreground/60"
                      />
                      <button
                        type="button"
                        aria-label={showPassword ? t("login.hidePassword") : t("login.showPassword")}
                        className="absolute right-2 top-1/2 -translate-y-1/2 rounded-md p-2 text-muted-foreground hover:text-foreground"
                        onClick={() => setShowPassword((v) => !v)}
                      >
                        {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                      </button>
                    </div>
                  </div>

                  {need2fa && (
                    <div className="grid gap-2">
                      <Label htmlFor="login-code" className="text-foreground">{t("login.totp")}</Label>
                      <div className="relative">
                        <ShieldCheck className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground/70" />
                        <Input
                          id="login-code"
                          value={loginCode}
                          onChange={(e) => setLoginCode(e.target.value)}
                          placeholder={t("login.totpPlaceholder")}
                          inputMode="numeric"
                          autoComplete="one-time-code"
                          maxLength={6}
                          required
                          autoFocus
                          className="border-input bg-background pl-10 text-center tracking-[0.4em] text-foreground placeholder:text-muted-foreground/60"
                        />
                      </div>
                    </div>
                  )}

                  <button
                    type="submit"
                    disabled={loading || (need2fa && loginCode.trim().length !== 6)}
                    className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg bg-primary text-sm font-medium text-primary-foreground transition-colors hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    {loading ? <Loader2 className="size-4 animate-spin" /> : t("login.submit")}
                  </button>

                  {googleEnabled && (
                    <button
                      type="button"
                      onClick={() => window.location.assign(`${API_BASE}/auth/google/start`)}
                      disabled={loading}
                      className="inline-flex h-10 w-full items-center justify-center gap-2.5 rounded-lg border border-border bg-card text-sm text-foreground transition-colors hover:bg-muted/70 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      <GoogleMark />
                      {t("login.googleSignIn")}
                    </button>
                  )}

                  {telegramEnabled && (
                    <button
                      type="button"
                      onClick={() => window.location.assign(`${API_BASE}/auth/telegram/start`)}
                      disabled={loading}
                      className="inline-flex h-10 w-full items-center justify-center gap-2.5 rounded-lg border border-border bg-card text-sm text-foreground transition-colors hover:bg-muted/70 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      <TelegramIcon className="size-4 text-brand-telegram" />
                      {t("login.telegramSignIn")}
                    </button>
                  )}

                  <div className="relative">
                    <Separator />
                    <span className="absolute left-1/2 -top-3 -translate-x-1/2 bg-card px-2 text-[11px] uppercase tracking-widest text-muted-foreground/70">
                      {t("login.or")}
                    </span>
                  </div>

                  <button
                    type="button"
                    onClick={() => { setError(""); setTab("register"); }}
                    className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg border border-border bg-card text-sm text-foreground transition-colors hover:bg-muted/70"
                  >
                    {t("login.registerSubmit")}
                    <ArrowRight className="size-4" />
                  </button>
                </motion.form>
              ) : (
                <motion.form key="register" initial={false} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleRegister} className="grid gap-5">
                  <div className="grid gap-2">
                    <Label htmlFor="reg-user" className="text-foreground">{t("login.username")}</Label>
                    <div className="relative">
                      <UserRound className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground/70" />
                      <Input
                        id="reg-user"
                        value={regUser}
                        onChange={(e) => setRegUser(e.target.value)}
                        placeholder={t("login.userPlaceholder")}
                        required
                        autoComplete="username"
                        className="border-input bg-background pl-10 text-foreground placeholder:text-muted-foreground/60"
                      />
                    </div>
                  </div>

                  <div className="grid gap-2">
                    <Label htmlFor="reg-email" className="text-foreground">{t("login.email")}</Label>
                    <div className="relative">
                      <Mail className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground/70" />
                      <Input
                        id="reg-email"
                        type="email"
                        value={regEmail}
                        onChange={(e) => setRegEmail(e.target.value)}
                        placeholder={t("login.emailPlaceholder")}
                        required
                        autoComplete="email"
                        className="border-input bg-background pl-10 text-foreground placeholder:text-muted-foreground/60"
                      />
                    </div>
                  </div>

                  <div className="grid gap-2">
                    <Label htmlFor="reg-pass" className="text-foreground">{t("login.password")}</Label>
                    <div className="relative">
                      <Lock className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground/70" />
                      <Input
                        id="reg-pass"
                        type={showPassword ? "text" : "password"}
                        value={regPass}
                        onChange={(e) => setRegPass(e.target.value)}
                        placeholder="••••••••"
                        required
                        autoComplete="new-password"
                        className="border-input bg-background pl-10 pr-10 text-foreground placeholder:text-muted-foreground/60"
                      />
                      <button
                        type="button"
                        aria-label={showPassword ? t("login.hidePassword") : t("login.showPassword")}
                        className="absolute right-2 top-1/2 -translate-y-1/2 rounded-md p-2 text-muted-foreground hover:text-foreground"
                        onClick={() => setShowPassword((v) => !v)}
                      >
                        {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                      </button>
                    </div>
                  </div>

                  <button
                    type="submit"
                    disabled={loading}
                    className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg bg-primary text-sm font-medium text-primary-foreground transition-colors hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    {loading ? <Loader2 className="size-4 animate-spin" /> : t("login.registerSubmit")}
                  </button>

                  <div className="relative">
                    <Separator />
                    <span className="absolute left-1/2 -top-3 -translate-x-1/2 bg-card px-2 text-[11px] uppercase tracking-widest text-muted-foreground/70">
                      {t("login.or")}
                    </span>
                  </div>

                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => { setError(""); setTab("login"); }}
                    className="h-10 w-full rounded-lg border-border bg-card text-foreground hover:bg-muted/70"
                  >
                    {t("login.tab")}
                    <ArrowRight className="size-4" />
                  </Button>
                </motion.form>
              )}
            </AnimatePresence>
          </CardContent>

          <CardFooter className="flex flex-col items-center gap-3 border-t-0 bg-transparent pt-0 text-sm text-muted-foreground">
            <div>
              {tab === "login" ? t("login.noAccount") : t("login.haveAccount")}
              <button
                type="button"
                onClick={() => { setError(""); setTab(tab === "login" ? "register" : "login"); }}
                className="ml-1 font-medium text-primary hover:underline"
              >
                {tab === "login" ? t("login.registerTab") : t("login.tab")}
              </button>
            </div>
            <span className="text-[11px] tracking-wide text-muted-foreground/70">{t("login.footer")}</span>
          </CardFooter>
        </Card>
      </div>
    </section>
  );
}

// GoogleMark — the official four-colour "G" (brand guidelines require the
// full-colour mark on light surfaces and the monochrome one on dark).
function GoogleMark() {
  return (
    <svg viewBox="0 0 24 24" className="size-4" aria-hidden>
      <path fill="#4285F4" d="M23.06 12.25c0-.85-.08-1.67-.22-2.45H12v4.63h6.2a5.3 5.3 0 0 1-2.3 3.48v2.9h3.72c2.18-2 3.44-4.96 3.44-8.56Z" />
      <path fill="#34A853" d="M12 24c3.11 0 5.72-1.03 7.62-2.79l-3.72-2.89c-1.03.69-2.35 1.1-3.9 1.1-3 0-5.54-2.03-6.45-4.75H1.71v2.98A11.5 11.5 0 0 0 12 24Z" />
      <path fill="#FBBC05" d="M5.55 14.67a6.9 6.9 0 0 1 0-4.41V7.28H1.71a11.5 11.5 0 0 0 0 10.37l3.84-2.98Z" />
      <path fill="#EA4335" d="M12 4.75c1.69 0 3.21.58 4.4 1.72l3.3-3.3C17.72 1.19 15.1 0 12 0A11.5 11.5 0 0 0 1.71 7.28l3.84 2.98C6.46 7.54 9 4.75 12 4.75Z" />
    </svg>
  );
}
