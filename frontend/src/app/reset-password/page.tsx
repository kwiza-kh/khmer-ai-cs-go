"use client";

// Password-reset landing page for links e-mailed by the user (or copied from
// the platform console). Public on purpose: the visitor is signed out by
// definition. The token is read from window.location.search inside an effect
// instead of useSearchParams — the latter forces the route behind a Suspense
// boundary for prerendering, and this page has nothing useful to prerender.
import { useEffect, useState } from "react";
import Link from "next/link";
import { CheckCircle2, Loader2, XCircle } from "lucide-react";
import { confirmPasswordReset } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";

export default function ResetPasswordPage() {
  const { t } = useI18n();
  // undefined = the URL has not been read yet, "" = no token at all.
  const [token, setToken] = useState<string | undefined>(undefined);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);

  // Reading the URL is an external-system sync; the server render cannot know
  // the query string, so this must run after mount rather than during render.
  useEffect(() => {
    const raw = new URLSearchParams(window.location.search).get("token") ?? "";
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setToken(raw.trim());
  }, []);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token) return;
    setError("");
    // Server enforces the same rules; checking here turns a round trip into
    // an instant hint and keeps an obviously-weak password out of the log.
    if (password.length < 8) { setError(t("reset.tooShort")); return; }
    if (password !== confirm) { setError(t("reset.mismatch")); return; }
    setBusy(true);
    try {
      await confirmPasswordReset(token, password);
      setDone(true);
      // The token is spent. Drop it from the address bar and the session
      // history so a bookmark/screenshot of the page cannot replay it (and the
      // route's layout marks the page noindex/no-referrer). Safari can throw
      // from History APIs when throttled — a leftover query string is a
      // degradation, never a reason to hide a successful reset.
      try { window.history.replaceState(null, "", "/reset-password"); } catch { /* ignored */ }
    } catch (err: unknown) {
      // apiFetch already localized the backend message (包括 "重置链接无效或已过期").
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const checking = token === undefined;
  const invalid = token === "";

  return (
    <section className="fixed inset-0 overflow-y-auto bg-background text-foreground">
      {/* Same chrome as the login page — this page is reached from an e-mail,
          usually on a phone, with no session and no app shell. */}
      <header className="absolute inset-x-0 top-0 z-10 flex items-center justify-between border-b border-border px-6 py-4">
        <span className="text-xs uppercase tracking-[0.12em] text-muted-foreground">RelayChat</span>
      </header>

      <div className="grid min-h-full w-full place-items-center px-4 py-6">
        <Card className="w-full max-w-sm border-border bg-card text-card-foreground">
          <CardHeader className="space-y-1">
            <CardTitle className="text-2xl text-foreground">{t("reset.title")}</CardTitle>
            <CardDescription className="text-muted-foreground">{t("reset.desc")}</CardDescription>
          </CardHeader>

          <CardContent className="grid gap-5">
            {checking ? (
              <Loader2 className="mx-auto size-5 animate-spin text-muted-foreground" />
            ) : invalid ? (
              <>
                <div role="alert" className="flex gap-2.5 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2">
                  <XCircle className="mt-0.5 size-4 shrink-0 text-danger" />
                  <p className="text-sm text-danger">{t("reset.invalidToken")}</p>
                </div>
                <LoginLink label={t("reset.toLogin")} />
              </>
            ) : done ? (
              <>
                <div role="status" aria-live="polite" className="flex gap-2.5 rounded-lg border border-success/40 bg-success/10 px-3 py-2">
                  <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" />
                  <p className="text-sm text-foreground">{t("reset.success")}</p>
                </div>
                <LoginLink label={t("reset.toLogin")} />
              </>
            ) : (
              <form onSubmit={handleSubmit} className="grid gap-5">
                {error && (
                  <div role="alert" aria-live="assertive" className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2">
                    <p className="text-sm text-danger">{error}</p>
                  </div>
                )}

                <div className="grid gap-2">
                  <Label htmlFor="reset-pass" className="text-foreground">{t("reset.newPassword")}</Label>
                  <PasswordInput
                    id="reset-pass"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="••••••••"
                    required
                    autoComplete="new-password"
                    className="border-input bg-background text-foreground placeholder:text-muted-foreground/60"
                  />
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="reset-confirm" className="text-foreground">{t("reset.confirmPassword")}</Label>
                  <PasswordInput
                    id="reset-confirm"
                    value={confirm}
                    onChange={(e) => setConfirm(e.target.value)}
                    placeholder="••••••••"
                    required
                    autoComplete="new-password"
                    className="border-input bg-background text-foreground placeholder:text-muted-foreground/60"
                  />
                </div>

                <Button
                  type="submit"
                  disabled={busy}
                  className="h-10 w-full rounded-lg bg-primary text-sm font-medium text-primary-foreground hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
                >
                  {busy ? <Loader2 className="size-4 animate-spin" /> : t("reset.submit")}
                </Button>
              </form>
            )}
          </CardContent>
        </Card>
      </div>
    </section>
  );
}

/** The one call-to-action every terminal state of this page shares. */
function LoginLink({ label }: { label: string }) {
  return (
    <Link
      href="/login"
      className="inline-flex h-10 w-full items-center justify-center rounded-lg bg-primary text-sm font-medium text-primary-foreground transition-colors hover:brightness-110"
    >
      {label}
    </Link>
  );
}
