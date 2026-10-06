"use client";

// Invite landing page. Public on purpose: the visitor usually has no session
// yet. Without one the code is parked in sessionStorage and the login page
// brings the visitor back here after signing in; with a session the invite is
// accepted immediately, and acceptance binds THIS account (the request carries
// only the code) — which is what replaced the owner typing a user_id.
import { Suspense, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { CheckCircle2, Loader2, XCircle } from "lucide-react";
import { acceptTeamInvite } from "@/lib/api";
import { useAuth } from "@/lib/auth-client";
import { useI18n } from "@/lib/i18n";
import { clearPendingInvite, stashPendingInvite } from "@/lib/pending-invite";

interface AcceptState {
  status: "idle" | "done" | "error";
  message: string;
}

function JoinInner() {
  const { t } = useI18n();
  const { token, isLoading } = useAuth();
  const params = useSearchParams();
  const code = (params.get("code") ?? "").trim();
  const [accept, setAccept] = useState<AcceptState>({ status: "idle", message: "" });
  // Guards against a second POST: it would be answered "already used" and turn
  // a success screen into an error. Keyed by code so a different link still runs.
  const attemptedCode = useRef<string | null>(null);

  // Derived during render — no effect needs to write these.
  const missingCode = code === "";
  const needLogin = !isLoading && !token && !missingCode;
  const accepting = !missingCode && !needLogin && accept.status === "idle";

  useEffect(() => {
    if (missingCode) {
      clearPendingInvite();
      return;
    }
    if (isLoading) return;
    if (!token) {
      // Park the code so the login page can bring the visitor back here.
      stashPendingInvite(code);
      return;
    }
    if (attemptedCode.current === code) return;
    attemptedCode.current = code;
    void acceptTeamInvite(code)
      .then(() => {
        clearPendingInvite();
        setAccept({ status: "done", message: "" });
      })
      .catch((error: unknown) => {
        // Terminal: clear the parked code so the next login does not bounce
        // back to a link that will keep failing.
        clearPendingInvite();
        setAccept({ status: "error", message: (error as Error).message });
      });
  }, [code, token, isLoading, missingCode]);

  let title = t("join.title");
  let body: React.ReactNode = null;
  if (missingCode) {
    title = t("join.invalidTitle");
    body = (
      <>
        <XCircle className="text-destructive mx-auto size-6" />
        <h1 className="text-sm font-semibold">{title}</h1>
        <p className="text-muted-foreground text-xs">{t("join.missingCode")}</p>
      </>
    );
  } else if (needLogin) {
    body = (
      <>
        <h1 className="text-sm font-semibold">{t("join.needLoginTitle")}</h1>
        <p className="text-muted-foreground text-xs">{t("join.needLoginDesc")}</p>
        <Link
          href="/login"
          className="bg-primary text-primary-foreground inline-flex h-8 items-center justify-center rounded-lg px-3 text-xs font-medium"
        >
          {t("join.loginCta")}
        </Link>
      </>
    );
  } else if (accept.status === "done") {
    body = (
      <>
        <CheckCircle2 className="text-success mx-auto size-6" />
        <h1 className="text-sm font-semibold">{t("join.successTitle")}</h1>
        <p className="text-muted-foreground text-xs">{t("join.successDesc")}</p>
        <Link
          href="/inbox"
          className="bg-primary text-primary-foreground inline-flex h-8 items-center justify-center rounded-lg px-3 text-xs font-medium"
        >
          {t("join.goInbox")}
        </Link>
      </>
    );
  } else if (accept.status === "error") {
    body = (
      <>
        <XCircle className="text-destructive mx-auto size-6" />
        <h1 className="text-sm font-semibold">{t("join.invalidTitle")}</h1>
        <p className="text-muted-foreground text-xs">{accept.message}</p>
      </>
    );
  } else if (accepting) {
    body = (
      <>
        <Loader2 className="text-muted-foreground mx-auto size-6 animate-spin" />
        <h1 className="text-sm font-semibold">{t("join.title")}</h1>
        <p className="text-muted-foreground text-xs">{t("join.loading")}</p>
      </>
    );
  }

  return (
    <div className="bg-background flex min-h-screen items-center justify-center p-6">
      <div className="border-border bg-card w-full max-w-sm space-y-3 rounded-xl border p-6 text-center shadow-sm">
        {body}
      </div>
    </div>
  );
}

export default function JoinPage() {
  // useSearchParams needs a Suspense boundary when the route is prerendered.
  return (
    <Suspense fallback={null}>
      <JoinInner />
    </Suspense>
  );
}
