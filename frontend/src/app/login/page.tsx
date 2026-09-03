"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { AnimatePresence, motion } from "motion/react";
import { Loader2, Eye, EyeOff, ArrowRight } from "lucide-react";

type Lang = "km" | "en" | "zh";

const t = {
  loginTitle:    { km: "ចូលប្រើប្រាស់", en: "Sign In", zh: "登录" },
  registerTitle: { km: "បង្កើតគណនី", en: "Create Account", zh: "注册" },
  loginDesc:     { km: "បញ្ចូលព័ត៌មានខាងក្រោមដើម្បីចូល", en: "Enter your credentials to sign in", zh: "输入凭据登录" },
  registerDesc:  { km: "បំពេញព័ត៌មានដើម្បីបង្កើតគណនី", en: "Fill in the details to create an account", zh: "填写信息创建账户" },
  loginTab:      { km: "ចូល", en: "Login", zh: "登录" },
  registerTab:   { km: "ចុះឈ្មោះ", en: "Register", zh: "注册" },
  username:      { km: "ឈ្មោះអ្នកប្រើ", en: "Username", zh: "用户名" },
  password:      { km: "ពាក្យសម្ងាត់", en: "Password", zh: "密码" },
  email:         { km: "អ៊ីមែល", en: "Email", zh: "邮箱" },
  loginBtn:      { km: "ចូល", en: "Sign In", zh: "登录" },
  registerBtn:   { km: "បង្កើតគណនី", en: "Create Account", zh: "创建账户" },
  userPlaceholder: { km: "admin", en: "Enter username", zh: "输入用户名" },
  emailPlaceholder:{ km: "sophea@example.com", en: "Enter email", zh: "输入邮箱" },
};

const langs: { key: Lang; label: string }[] = [
  { key: "km", label: "ខ្មែរ" }, { key: "en", label: "EN" }, { key: "zh", label: "中文" },
];

export default function LoginPage() {
  const { login, register } = useAuth();
  const router = useRouter();
  const [lang, setLang] = useState<Lang>("km");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [tab, setTab] = useState("login");
  const [showPassword, setShowPassword] = useState(false);

  const [loginUser, setLoginUser] = useState("");
  const [loginPass, setLoginPass] = useState("");
  const [regUser, setRegUser] = useState("");
  const [regEmail, setRegEmail] = useState("");
  const [regPass, setRegPass] = useState("");

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault(); setError(""); setLoading(true);
    try { await login(loginUser, loginPass); router.push("/ai-test"); }
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
      {/* Ambient brand glow */}
      <div aria-hidden className="pointer-events-none absolute inset-0">
        <div className="absolute -top-40 left-1/2 -translate-x-1/2 h-[480px] w-[820px] rounded-full bg-primary/10 blur-[120px]" />
        <div className="absolute bottom-[-180px] right-[-120px] h-[380px] w-[380px] rounded-full bg-info/5 blur-[100px]" />
      </div>

      <div className="absolute top-6 right-6 z-20 flex gap-1">
        {langs.map((opt) => (
          <button key={opt.key} onClick={() => setLang(opt.key)}
            className={`px-2 py-1 rounded-md text-xs font-medium transition-colors ${
              lang === opt.key ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"
            }`}
          >{opt.label}</button>
        ))}
      </div>

      <div className="relative w-full max-w-[400px]">
        {/* Brand */}
        <div className="flex flex-col items-center mb-7">
          <div className="flex size-11 items-center justify-center rounded-xl bg-gradient-to-br from-primary via-primary to-emerald-800 shadow-[0_4px_18px_-4px] shadow-primary/60">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" className="size-5 text-primary-foreground">
              <path d="M12 3l9 4.9-9 4.9-9-4.9L12 3z" />
              <path d="M3 13l9 4.9 9-4.9" opacity="0.55" />
            </svg>
          </div>
          <p className="mt-3 text-[15px] font-semibold tracking-tight text-foreground">Khmer AI</p>
          <p className="mt-1 text-[10px] font-semibold uppercase tracking-[0.22em] text-muted-foreground">
            Customer Service Platform
          </p>
        </div>

        {/* Card */}
        <div className="bg-card border border-border/80 rounded-xl shadow-[0_8px_30px_-12px_rgb(0_0_0/0.35)]">
          <div className="px-6 pt-6 pb-2">
            <h1 className="text-lg font-semibold tracking-tight">
              {tab === "login" ? t.loginTitle[lang] : t.registerTitle[lang]}
            </h1>
            <p className="text-sm text-muted-foreground mt-1">
              {tab === "login" ? t.loginDesc[lang] : t.registerDesc[lang]}
            </p>
          </div>

          <div className="px-6 pt-3 pb-1">
            <Tabs value={tab} onValueChange={setTab}>
              <TabsList className="w-full h-9">
                <TabsTrigger value="login" className="flex-1 text-xs">{t.loginTab[lang]}</TabsTrigger>
                <TabsTrigger value="register" className="flex-1 text-xs">{t.registerTab[lang]}</TabsTrigger>
              </TabsList>
            </Tabs>
          </div>

          <AnimatePresence>
            {error && (
              <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="mx-6 mt-3">
                <div className="rounded-md bg-destructive/10 border border-destructive/20 px-3 py-2">
                  <p className="text-sm text-destructive-foreground">{error}</p>
                </div>
              </motion.div>
            )}
          </AnimatePresence>

          <div className="px-6 py-5">
            <AnimatePresence mode="wait">
              {tab === "login" ? (
                <motion.form key="login" initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleLogin} className="space-y-4">
                  <Field label={t.username[lang]}>
                    <Input value={loginUser} onChange={(e) => setLoginUser(e.target.value)}
                      placeholder={t.userPlaceholder[lang]} required className="h-10" />
                  </Field>
                  <Field label={t.password[lang]}>
                    <div className="relative">
                      <Input type={showPassword ? "text" : "password"} value={loginPass}
                        onChange={(e) => setLoginPass(e.target.value)} placeholder="········" required className="h-10 pr-10" />
                      <button type="button" onClick={() => setShowPassword(!showPassword)}
                        className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground">
                        {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                      </button>
                    </div>
                  </Field>
                  <Button type="submit" disabled={loading} className="w-full h-10 text-sm gap-2">
                    {loading ? <Loader2 className="size-4 animate-spin" /> : <>{t.loginBtn[lang]}<ArrowRight className="size-3.5 opacity-50" /></>}
                  </Button>
                </motion.form>
              ) : (
                <motion.form key="register" initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: 4 }} transition={{ duration: 0.15 }} onSubmit={handleRegister} className="space-y-4">
                  <Field label={t.username[lang]}>
                    <Input value={regUser} onChange={(e) => setRegUser(e.target.value)}
                      placeholder={t.userPlaceholder[lang]} required className="h-10" />
                  </Field>
                  <Field label={t.email[lang]}>
                    <Input type="email" value={regEmail} onChange={(e) => setRegEmail(e.target.value)}
                      placeholder={t.emailPlaceholder[lang]} required className="h-10" />
                  </Field>
                  <Field label={t.password[lang]}>
                    <Input type="password" value={regPass} onChange={(e) => setRegPass(e.target.value)}
                      placeholder="········" required className="h-10" />
                  </Field>
                  <Button type="submit" disabled={loading} className="w-full h-10 text-sm gap-2">
                    {loading ? <Loader2 className="size-4 animate-spin" /> : <>{t.registerBtn[lang]}<ArrowRight className="size-3.5 opacity-50" /></>}
                  </Button>
                </motion.form>
              )}
            </AnimatePresence>
          </div>
        </div>

        <p className="text-center text-xs text-muted-foreground/70 mt-6 tracking-wide">
          Khmer AI Customer Service · Production
        </p>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <label className="block text-sm font-medium text-foreground">{label}</label>
      {children}
    </div>
  );
}
