"use client";

import * as React from "react";
import { useState, useRef, useEffect } from "react";
import { useRouter } from "next/navigation";
import { API_BASE, useAuth } from "@/lib/auth-client";
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

export default function LoginPage() {
  const { login, register, completeGoogleLogin } = useAuth();
  const router = useRouter();
  const { lang, setLang, t } = useI18n();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"login" | "register">("login");
  const [showPassword, setShowPassword] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);

  const [loginUser, setLoginUser] = useState("");
  const [loginPass, setLoginPass] = useState("");
  const [loginCode, setLoginCode] = useState("");
  const [need2fa, setNeed2fa] = useState(false);
  const [regUser, setRegUser] = useState("");
  const [regEmail, setRegEmail] = useState("");
  const [regPass, setRegPass] = useState("");

  // OAuth return: the callback redirects here with ?google_code= (success) or
  // ?google_error= (failure). Swap the one-time code for a session, then clean
  // the URL so a refresh cannot replay it.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get("google_code");
    const errReason = params.get("google_error");
    if (!code && !errReason) return;
    window.history.replaceState({}, "", window.location.pathname);
    if (errReason) {
      const key = `login.googleErr.${errReason}`;
      const msg = t(key);
      setError(msg === key ? t("login.googleErr.failed") : msg);
      return;
    }
    setLoading(true);
    // The state travels with the code; the server rejects a code whose state
    // did not originate from this browser (login-CSRF guard).
    void completeGoogleLogin(code!, params.get("state") ?? "")
      .then(() => router.push("/ai-test"))
      .catch((err: unknown) => setError((err as Error).message))
      .finally(() => setLoading(false));
  }, [completeGoogleLogin, router, t]);

  // Probe whether the deployment has Google sign-in configured.
  useEffect(() => {
    let cancelled = false;
    void fetch(`${API_BASE}/auth/methods`)
      .then((res) => res.json())
      .then((data: { google?: boolean }) => { if (!cancelled) setGoogleEnabled(Boolean(data.google)); })
      .catch(() => { /* keep the button hidden */ });
    return () => { cancelled = true; };
  }, []);

  // Monochrome particle drift — the signature backdrop of this design.
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  useEffect(() => {
    const canvas = canvasRef.current;
    const ctx = canvas?.getContext("2d");
    if (!canvas || !ctx) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    const setSize = () => {
      canvas.width = window.innerWidth;
      canvas.height = window.innerHeight;
    };
    setSize();

    type P = { x: number; y: number; v: number; o: number };
    let ps: P[] = [];
    let raf = 0;

    const make = (): P => ({
      x: Math.random() * canvas.width,
      y: Math.random() * canvas.height,
      v: Math.random() * 0.25 + 0.05,
      o: Math.random() * 0.35 + 0.15,
    });

    const init = () => {
      ps = [];
      const count = Math.floor((canvas.width * canvas.height) / 9000);
      for (let i = 0; i < count; i++) ps.push(make());
    };

    const draw = () => {
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      ps.forEach((p) => {
        p.y -= p.v;
        if (p.y < 0) {
          p.x = Math.random() * canvas.width;
          p.y = canvas.height + Math.random() * 40;
          p.v = Math.random() * 0.25 + 0.05;
          p.o = Math.random() * 0.35 + 0.15;
        }
        ctx.fillStyle = `rgba(250,250,250,${p.o})`;
        ctx.fillRect(p.x, p.y, 0.7, 2.2);
      });
      raf = requestAnimationFrame(draw);
    };

    const onResize = () => { setSize(); init(); };
    window.addEventListener("resize", onResize);
    init();
    raf = requestAnimationFrame(draw);
    return () => {
      window.removeEventListener("resize", onResize);
      cancelAnimationFrame(raf);
    };
  }, []);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault(); setError(""); setLoading(true);
    try {
      const result = await login(loginUser, loginPass, need2fa ? loginCode.trim() : undefined);
      if (result.twoFactorRequired) {
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
    <section className="fixed inset-0 overflow-y-auto bg-zinc-950 text-zinc-50">
      <style>{`
        .accent-lines{position:absolute;inset:0;pointer-events:none;opacity:.7}
        .hline,.vline{position:absolute;background:#27272a;will-change:transform,opacity}
        .hline{left:0;right:0;height:1px;transform:scaleX(0);transform-origin:50% 50%;animation:drawX .8s cubic-bezier(.22,.61,.36,1) forwards}
        .vline{top:0;bottom:0;width:1px;transform:scaleY(0);transform-origin:50% 0%;animation:drawY .9s cubic-bezier(.22,.61,.36,1) forwards}
        .hline:nth-child(1){top:18%;animation-delay:.12s}
        .hline:nth-child(2){top:50%;animation-delay:.22s}
        .hline:nth-child(3){top:82%;animation-delay:.32s}
        .vline:nth-child(4){left:22%;animation-delay:.42s}
        .vline:nth-child(5){left:50%;animation-delay:.54s}
        .vline:nth-child(6){left:78%;animation-delay:.66s}
        .hline::after,.vline::after{content:"";position:absolute;inset:0;background:linear-gradient(90deg,transparent,rgba(250,250,250,.24),transparent);opacity:0;animation:shimmerLine .9s ease-out forwards}
        .hline:nth-child(1)::after{animation-delay:.12s}
        .hline:nth-child(2)::after{animation-delay:.22s}
        .hline:nth-child(3)::after{animation-delay:.32s}
        .vline:nth-child(4)::after{animation-delay:.42s}
        .vline:nth-child(5)::after{animation-delay:.54s}
        .vline:nth-child(6)::after{animation-delay:.66s}
        @keyframes drawX{0%{transform:scaleX(0);opacity:0}60%{opacity:.95}100%{transform:scaleX(1);opacity:.7}}
        @keyframes drawY{0%{transform:scaleY(0);opacity:0}60%{opacity:.95}100%{transform:scaleY(1);opacity:.7}}
        @keyframes shimmerLine{0%{opacity:0}35%{opacity:.25}100%{opacity:0}}
        .card-animate{opacity:0;transform:translateY(20px);animation:cardUp .8s cubic-bezier(.22,.61,.36,1) .4s forwards}
        @keyframes cardUp{to{opacity:1;transform:translateY(0)}}
        @media (prefers-reduced-motion: reduce){
          .hline,.vline,.hline::after,.vline::after{animation:none;transform:none;opacity:.7}
          .card-animate{animation:none;opacity:1;transform:none}
        }
      `}</style>

      {/* Subtle vignette */}
      <div className="pointer-events-none absolute inset-0 [background:radial-gradient(80%_60%_at_50%_30%,rgba(255,255,255,0.06),transparent_60%)]" />

      {/* Animated accent lines */}
      <div aria-hidden className="accent-lines">
        <div className="hline" />
        <div className="hline" />
        <div className="hline" />
        <div className="vline" />
        <div className="vline" />
        <div className="vline" />
      </div>

      {/* Particles */}
      <canvas
        ref={canvasRef}
        aria-hidden
        className="pointer-events-none absolute inset-0 h-full w-full opacity-50 mix-blend-screen"
      />

      {/* Header */}
      <header className="absolute inset-x-0 top-0 z-10 flex items-center justify-between border-b border-zinc-800/80 px-6 py-4">
        <span className="text-xs uppercase tracking-[0.14em] text-zinc-400">Khmer AI</span>
        <div className="flex items-center gap-1 rounded-lg border border-zinc-800 bg-zinc-900/70 p-0.5">
          {LANGS.map((opt) => (
            <button
              key={opt.key}
              onClick={() => setLang(opt.key)}
              className={cn(
                "rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors",
                lang === opt.key ? "bg-zinc-50 text-zinc-900" : "text-zinc-400 hover:text-zinc-100",
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
        <Card className="card-animate w-full max-w-sm border-zinc-800 bg-zinc-900/70 text-zinc-50 backdrop-blur supports-[backdrop-filter]:bg-zinc-900/60">
          <CardHeader className="space-y-1">
            <CardTitle className="text-2xl text-zinc-50">
              {tab === "login" ? t("login.title") : t("login.registerTitle")}
            </CardTitle>
            <CardDescription className="text-zinc-400">
              {tab === "login" ? t("login.desc") : t("login.registerDesc")}
            </CardDescription>
          </CardHeader>

          <CardContent className="grid gap-5">
            {/* Segmented control */}
            <div className="grid grid-cols-2 gap-1 rounded-lg border border-zinc-800 bg-zinc-950 p-1">
              {(["login", "register"] as const).map((key) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => { setError(""); setTab(key); }}
                  className={cn(
                    "h-8 rounded-md text-[13px] font-medium transition-colors",
                    tab === key ? "bg-zinc-800 text-zinc-50" : "text-zinc-400 hover:text-zinc-200",
                  )}
                >
                  {key === "login" ? t("login.tab") : t("login.registerTab")}
                </button>
              ))}
            </div>

            <AnimatePresence>
              {error && (
                <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }}>
                  <div role="alert" aria-live="assertive" className="rounded-lg border border-red-900/60 bg-red-950/40 px-3 py-2">
                    <p className="text-sm text-red-300">{error}</p>
                  </div>
                </motion.div>
              )}
            </AnimatePresence>

            <AnimatePresence mode="wait">
              {tab === "login" ? (
                <motion.form key="login" initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleLogin} className="grid gap-5">
                  <div className="grid gap-2">
                    <Label htmlFor="login-user" className="text-zinc-300">{t("login.username")}</Label>
                    <div className="relative">
                      <UserRound className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-zinc-500" />
                      <Input
                        id="login-user"
                        value={loginUser}
                        onChange={(e) => setLoginUser(e.target.value)}
                        placeholder={t("login.userPlaceholder")}
                        required
                        autoComplete="username"
                        className="border-zinc-800 bg-zinc-950 pl-10 text-zinc-50 placeholder:text-zinc-600"
                      />
                    </div>
                  </div>

                  <div className="grid gap-2">
                    <Label htmlFor="login-pass" className="text-zinc-300">{t("login.password")}</Label>
                    <div className="relative">
                      <Lock className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-zinc-500" />
                      <Input
                        id="login-pass"
                        type={showPassword ? "text" : "password"}
                        value={loginPass}
                        onChange={(e) => setLoginPass(e.target.value)}
                        placeholder="••••••••"
                        required
                        autoComplete="current-password"
                        className="border-zinc-800 bg-zinc-950 pl-10 pr-10 text-zinc-50 placeholder:text-zinc-600"
                      />
                      <button
                        type="button"
                        aria-label={showPassword ? t("login.hidePassword") : t("login.showPassword")}
                        className="absolute right-2 top-1/2 -translate-y-1/2 rounded-md p-2 text-zinc-400 hover:text-zinc-200"
                        onClick={() => setShowPassword((v) => !v)}
                      >
                        {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                      </button>
                    </div>
                  </div>

                  {need2fa && (
                    <div className="grid gap-2">
                      <Label htmlFor="login-code" className="text-zinc-300">{t("login.totp")}</Label>
                      <div className="relative">
                        <ShieldCheck className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-zinc-500" />
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
                          className="border-zinc-800 bg-zinc-950 pl-10 text-center tracking-[0.4em] text-zinc-50 placeholder:text-zinc-600"
                        />
                      </div>
                    </div>
                  )}

                  <button
                    type="submit"
                    disabled={loading || (need2fa && loginCode.trim().length !== 6)}
                    className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg bg-zinc-50 text-sm font-medium text-zinc-900 transition-colors hover:bg-zinc-200 disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    {loading ? <Loader2 className="size-4 animate-spin" /> : t("login.submit")}
                  </button>

                  {googleEnabled && (
                    <button
                      type="button"
                      onClick={() => window.location.assign(`${API_BASE}/auth/google/start`)}
                      disabled={loading}
                      className="inline-flex h-10 w-full items-center justify-center gap-2.5 rounded-lg border border-zinc-800 bg-zinc-950 text-sm text-zinc-50 transition-colors hover:bg-zinc-900/80 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      <GoogleMark />
                      {t("login.googleSignIn")}
                    </button>
                  )}

                  <div className="relative">
                    <Separator className="bg-zinc-800" />
                    <span className="absolute left-1/2 -top-3 -translate-x-1/2 bg-zinc-900/70 px-2 text-[11px] uppercase tracking-widest text-zinc-500">
                      {t("login.or")}
                    </span>
                  </div>

                  <button
                    type="button"
                    onClick={() => { setError(""); setTab("register"); }}
                    className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg border border-zinc-800 bg-zinc-950 text-sm text-zinc-50 transition-colors hover:bg-zinc-900/80"
                  >
                    {t("login.registerSubmit")}
                    <ArrowRight className="size-4" />
                  </button>
                </motion.form>
              ) : (
                <motion.form key="register" initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleRegister} className="grid gap-5">
                  <div className="grid gap-2">
                    <Label htmlFor="reg-user" className="text-zinc-300">{t("login.username")}</Label>
                    <div className="relative">
                      <UserRound className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-zinc-500" />
                      <Input
                        id="reg-user"
                        value={regUser}
                        onChange={(e) => setRegUser(e.target.value)}
                        placeholder={t("login.userPlaceholder")}
                        required
                        autoComplete="username"
                        className="border-zinc-800 bg-zinc-950 pl-10 text-zinc-50 placeholder:text-zinc-600"
                      />
                    </div>
                  </div>

                  <div className="grid gap-2">
                    <Label htmlFor="reg-email" className="text-zinc-300">{t("login.email")}</Label>
                    <div className="relative">
                      <Mail className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-zinc-500" />
                      <Input
                        id="reg-email"
                        type="email"
                        value={regEmail}
                        onChange={(e) => setRegEmail(e.target.value)}
                        placeholder={t("login.emailPlaceholder")}
                        required
                        autoComplete="email"
                        className="border-zinc-800 bg-zinc-950 pl-10 text-zinc-50 placeholder:text-zinc-600"
                      />
                    </div>
                  </div>

                  <div className="grid gap-2">
                    <Label htmlFor="reg-pass" className="text-zinc-300">{t("login.password")}</Label>
                    <div className="relative">
                      <Lock className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-zinc-500" />
                      <Input
                        id="reg-pass"
                        type={showPassword ? "text" : "password"}
                        value={regPass}
                        onChange={(e) => setRegPass(e.target.value)}
                        placeholder="••••••••"
                        required
                        autoComplete="new-password"
                        className="border-zinc-800 bg-zinc-950 pl-10 pr-10 text-zinc-50 placeholder:text-zinc-600"
                      />
                      <button
                        type="button"
                        aria-label={showPassword ? t("login.hidePassword") : t("login.showPassword")}
                        className="absolute right-2 top-1/2 -translate-y-1/2 rounded-md p-2 text-zinc-400 hover:text-zinc-200"
                        onClick={() => setShowPassword((v) => !v)}
                      >
                        {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                      </button>
                    </div>
                  </div>

                  <button
                    type="submit"
                    disabled={loading}
                    className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg bg-zinc-50 text-sm font-medium text-zinc-900 transition-colors hover:bg-zinc-200 disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    {loading ? <Loader2 className="size-4 animate-spin" /> : t("login.registerSubmit")}
                  </button>

                  <div className="relative">
                    <Separator className="bg-zinc-800" />
                    <span className="absolute left-1/2 -top-3 -translate-x-1/2 bg-zinc-900/70 px-2 text-[11px] uppercase tracking-widest text-zinc-500">
                      {t("login.or")}
                    </span>
                  </div>

                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => { setError(""); setTab("login"); }}
                    className="h-10 w-full rounded-lg border-zinc-800 bg-zinc-950 text-zinc-50 hover:bg-zinc-900/80"
                  >
                    {t("login.tab")}
                    <ArrowRight className="size-4" />
                  </Button>
                </motion.form>
              )}
            </AnimatePresence>
          </CardContent>

          <CardFooter className="flex flex-col items-center gap-3 border-t-0 bg-transparent pt-0 text-sm text-zinc-400">
            <div>
              {tab === "login" ? t("login.noAccount") : t("login.haveAccount")}
              <button
                type="button"
                onClick={() => { setError(""); setTab(tab === "login" ? "register" : "login"); }}
                className="ml-1 text-zinc-200 hover:underline"
              >
                {tab === "login" ? t("login.registerTab") : t("login.tab")}
              </button>
            </div>
            <span className="text-[11px] tracking-wide text-zinc-500">{t("login.footer")}</span>
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
