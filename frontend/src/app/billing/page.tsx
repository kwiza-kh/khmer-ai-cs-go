"use client";

import * as React from "react";
import { useSearchParams } from "next/navigation";
import useSWR from "swr";
import { CreditCard, Check, Loader2, ShieldCheck } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { toast } from "sonner";
import {
  capturePaypalOrder,
  createPaypalOrder,
  getBillingCatalog,
  type BillingPlan,
} from "@/lib/billing-api";

// The approve URL comes from PayPal by way of our own API, but the browser must
// not follow "whatever the server said": a compromised or misconfigured backend
// response would turn the checkout button into a phishing redirect. Only PayPal's
// own hosts, over https, are allowed to receive the buyer. The backend validates
// the same thing on the way in (internal/paypal), so a bad URL has to survive two
// independent checks.
const PAYPAL_HOSTS = new Set([
  "paypal.com",
  "www.paypal.com",
  "sandbox.paypal.com",
  "www.sandbox.paypal.com",
]);

function paypalApproveURL(raw: string): string | null {
  try {
    const url = new URL(raw);
    if (url.protocol !== "https:") return null;
    if (!PAYPAL_HOSTS.has(url.hostname.toLowerCase())) return null;
    return url.toString();
  } catch {
    return null;
  }
}

// Buying a plan: the catalogue comes from the server (prices live in the
// deployment's environment, not in this bundle), the order is created
// server-side, the browser is handed to PayPal's approve URL, and PayPal returns
// it here with ?token=<order id> — which triggers the capture.
//
// The capture runs through SWR rather than an effect: it is a one-shot side
// effect keyed by the order id, and the server is idempotent, so a reload (or a
// webhook that got there first) cannot grant a second month.
export default function BillingPage() {
  const { t, tf } = useI18n();
  const searchParams = useSearchParams();
  const paypalState = searchParams.get("paypal") ?? "";
  const orderId = searchParams.get("token") ?? "";
  const shouldCapture = paypalState === "return" && orderId !== "";

  const { data, error, isLoading, mutate } = useSWR("billing-catalog", getBillingCatalog);
  const { data: capture, error: captureError } = useSWR(
    shouldCapture ? `billing-capture-${orderId}` : null,
    () => capturePaypalOrder(orderId),
    {
      revalidateOnFocus: false,
      onSuccess: () => {
        toast.success(t("bl.paidToast"));
        void mutate();
      },
      onError: (err) => toast.error((err as Error).message),
    },
  );
  const [busy, setBusy] = React.useState<BillingPlan | null>(null);

  const startCheckout = async (plan: BillingPlan) => {
    setBusy(plan);
    try {
      const order = await createPaypalOrder(plan);
      const target = paypalApproveURL(order.approve_url);
      if (!target) {
        toast.error(t("bl.badApproveUrl"));
        setBusy(null);
        return;
      }
      // assign() rather than href = : a full-page navigation to PayPal, without
      // mutating a value that lives outside this component.
      window.location.assign(target);
    } catch (err) {
      toast.error((err as Error).message);
      setBusy(null);
    }
  };

  const current = data?.current;
  const currency = data?.currency || "USD";
  const usage = (used?: number, quota?: number) => {
    if (!used || !quota || !Number.isFinite(quota)) return 0;
    return Math.min(100, Math.round((used / quota) * 100));
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <PageHeader
        icon={CreditCard}
        kicker={t("bl.kicker")}
        title={t("bl.title")}
        description={t("bl.desc")}
      />

      <div className="space-y-5 px-5 pb-8 sm:px-6">
        {paypalState === "cancel" && (
          <Card className="border-border/80">
            <CardContent className="py-3 text-sm text-muted-foreground">{t("bl.canceled")}</CardContent>
          </Card>
        )}
        {capture?.captured && (
          <Card className="border-primary/40 bg-primary/5">
            <CardContent className="flex items-center gap-2 py-3 text-sm">
              <Check className="size-4 text-primary" />
              {capture.already ? t("bl.paidAlready") : t("bl.paidToast")}
              <Badge variant="secondary" className="ml-2 h-5 px-2 text-[11px]">
                {capture.current?.plan?.toUpperCase() ?? ""}
              </Badge>
            </CardContent>
          </Card>
        )}
        {captureError && (
          <Card className="border-destructive/40">
            <CardContent className="py-3 text-sm text-destructive">{(captureError as Error).message}</CardContent>
          </Card>
        )}
        {error && (
          <Card className="border-destructive/40">
            <CardContent className="py-3 text-sm text-destructive">{(error as Error).message}</CardContent>
          </Card>
        )}

        {/* Current entitlement + usage: the same numbers the platform meters on. */}
        <Card className="border-border/80">
          <CardHeader className="flex flex-row items-center justify-between gap-3 pb-3">
            <CardTitle className="text-sm">{t("bl.currentPlan")}</CardTitle>
            <Badge variant="secondary" className="h-6 px-2.5 text-xs uppercase">
              {isLoading ? "…" : current?.plan ?? "free"}
            </Badge>
          </CardHeader>
          <CardContent className="space-y-3 text-xs text-muted-foreground">
            <p>
              {current?.paid_until
                ? tf("bl.paidUntil", { date: new Date(current.paid_until).toLocaleDateString() })
                : t("bl.neverPaid")}
            </p>
            <div className="space-y-2">
              <div>
                <div className="flex justify-between">
                  <span>{t("bl.docsLabel")}</span>
                  <span className="tabular-nums">
                    {current?.docs_used ?? 0} / {current?.monthly_doc_quota ?? 0}
                  </span>
                </div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted">
                  <div className="h-full rounded-full bg-primary/70" style={{ width: `${usage(current?.docs_used, current?.monthly_doc_quota)}%` }} />
                </div>
              </div>
              <div>
                <div className="flex justify-between">
                  <span>{t("bl.msgsLabel")}</span>
                  <span className="tabular-nums">
                    {current?.messages_used ?? 0} / {current?.monthly_message_quota ?? 0}
                  </span>
                </div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted">
                  <div className="h-full rounded-full bg-primary/70" style={{ width: `${usage(current?.messages_used, current?.monthly_message_quota)}%` }} />
                </div>
              </div>
            </div>
            {current?.cycle_end && <p>{tf("bl.cycleEnds", { date: new Date(current.cycle_end).toLocaleDateString() })}</p>}
          </CardContent>
        </Card>

        {/* Plan offers. Prices come from configuration; a plan without one is not
            for sale, which is how a deployment that is not selling yet looks. */}
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {(data?.plans ?? []).map((offer) => {
            const isCurrent = (current?.plan ?? "free") === offer.plan;
            return (
              <Card key={offer.plan} className={isCurrent ? "border-primary/50" : "border-border/80"}>
                <CardHeader className="pb-2">
                  <CardTitle className="flex items-center justify-between text-sm uppercase">
                    {offer.plan}
                    {isCurrent && <Badge className="h-5 px-2 text-[10px]">{t("bl.current")}</Badge>}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-3">
                  <p className="text-lg font-semibold tracking-tight">
                    {offer.price ? `${offer.price} ${currency}` : "—"}
                  </p>
                  {offer.purchasable ? (
                    <Button
                      className="w-full gap-2"
                      disabled={busy !== null}
                      onClick={() => { void startCheckout(offer.plan); }}
                    >
                      {busy === offer.plan ? <Loader2 className="size-3.5 animate-spin" /> : <CreditCard className="size-3.5" />}
                      {t("bl.buy")}
                    </Button>
                  ) : (
                    <p className="text-xs text-muted-foreground">
                      {offer.plan === "free" ? t("bl.freeNote") : t("bl.notForSale")}
                    </p>
                  )}
                </CardContent>
              </Card>
            );
          })}
        </div>

        <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <ShieldCheck className="size-3.5" />
          {data?.checkout_ready ? t("bl.checkoutHint") : t("bl.notConfigured")}
        </p>
      </div>
    </div>
  );
}
