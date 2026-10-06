"use client";

import * as React from "react";
import { useSearchParams } from "next/navigation";
import useSWR from "swr";
import { Check, CreditCard, Loader2, ShieldCheck, Sparkles } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import {
  capturePaypalOrder,
  createPaypalOrder,
  getBillingCatalog,
  type BillingPlan,
  type BillingStatus,
  type PlanOffer,
} from "@/lib/billing-api";

// The approve URL comes from PayPal by way of our own API, but the browser must
// not follow "whatever the server said": a compromised or misconfigured backend
// response would turn the checkout button into a phishing redirect. Only PayPal's
// own hosts, over https, are allowed to receive the buyer. The backend validates
// the same thing (internal/paypal), so a bad URL survives two independent checks.
const PAYPAL_HOSTS = new Set(["paypal.com", "www.paypal.com", "sandbox.paypal.com", "www.sandbox.paypal.com"]);

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

// Buying a plan: the catalogue comes from the server (prices are the
// deployment's, not this bundle's), the order is created server-side, the buyer
// goes to PayPal and returns with ?token=<order id>, which triggers the capture.
//
// The capture runs through SWR rather than an effect: it is a one-shot side
// effect keyed by the order id, and the server is idempotent, so a reload — or a
// webhook that got there first — cannot grant a second month.
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

  const current: BillingStatus | undefined = data?.current;
  const currency = data?.currency || "USD";
  const currentPlan = current?.plan ?? "free";
  const percent = (used?: number | null, quota?: number | null) => {
    if (!used || !quota || !Number.isFinite(quota)) return 0;
    return Math.min(100, Math.round((used / quota) * 100));
  };
  const limit = (value: number | null | undefined) =>
    value === null || value === undefined ? t("bl.unlimited") : value.toLocaleString("en-US");
  // A used count of null means the server could not read it; rendering that as
  // 0 would be a number the merchant acts on.
  const usedText = (value?: number | null) =>
    value === null || value === undefined ? "—" : value.toLocaleString("en-US");

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <PageHeader icon={CreditCard} kicker={t("bl.kicker")} title={t("bl.title")} description={t("bl.desc")} />

      <div className="mx-auto w-full max-w-6xl space-y-6 px-5 pb-12 sm:px-6">
        {paypalState === "cancel" && (
          <div className="rounded-xl border border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground">{t("bl.canceled")}</div>
        )}
        {capture?.captured && (
          <div className="flex items-center gap-2 rounded-xl border border-primary/40 bg-primary/5 px-4 py-3 text-sm">
            <Check className="size-4 text-primary" />
            {capture.already ? t("bl.paidAlready") : t("bl.paidToast")}
            <Badge variant="secondary" className="ml-1 h-5 px-2 text-[11px] uppercase">{capture.current?.plan ?? ""}</Badge>
          </div>
        )}
        {captureError && (
          <div className="rounded-xl border border-destructive/40 px-4 py-3 text-sm text-destructive">{(captureError as Error).message}</div>
        )}
        {error && (
          <div className="rounded-xl border border-destructive/40 px-4 py-3 text-sm text-destructive">{(error as Error).message}</div>
        )}

        {/* Current entitlement: the same numbers the platform meters on. */}
        <Card className="border-border/80">
          <CardHeader className="flex flex-row items-center justify-between gap-3 pb-3">
            <CardTitle className="text-sm">{t("bl.currentPlan")}</CardTitle>
            <Badge variant="secondary" className="h-6 px-2.5 text-xs uppercase">{isLoading ? "…" : currentPlan}</Badge>
          </CardHeader>
          <CardContent className="space-y-4 text-xs text-muted-foreground">
            <p>
              {current?.paid_until
                ? tf("bl.paidUntil", { date: new Date(current.paid_until).toLocaleDateString() })
                : currentPlan === "free"
                  ? t("bl.neverPaid")
                  // A plan granted from the platform console has no paid_until:
                  // telling that merchant "no paid plan yet" while the badge reads
                  // PRO is simply wrong.
                  : t("bl.planGranted")}
            </p>
            <div className="grid gap-4 sm:grid-cols-3">
              {[
                { label: t("bl.docsLabel"), used: current?.docs_used, quota: current?.monthly_doc_quota },
                { label: t("bl.msgsLabel"), used: current?.messages_used, quota: current?.monthly_message_quota },
                { label: t("bl.limitSeats"), used: current?.seats_used, quota: current?.seats_quota },
              ].map((row) => (
                <div key={row.label}>
                  <div className="flex justify-between text-[11px]">
                    <span>{row.label}</span>
                    <span className="tabular-nums">
                      {usedText(row.used)} / {limit(row.quota)}
                    </span>
                  </div>
                  <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
                    <div className="h-full rounded-full bg-primary/70" style={{ width: `${percent(row.used, row.quota)}%` }} />
                  </div>
                </div>
              ))}
            </div>
            {current?.cycle_end && <p>{tf("bl.cycleEnds", { date: new Date(current.cycle_end).toLocaleDateString() })}</p>}
          </CardContent>
        </Card>

        {/* Plans. Prices come from configuration; a plan without one is not for
            sale, which is what a deployment that is not selling yet looks like. */}
        <div className="grid gap-5 lg:grid-cols-3">
          {(data?.plans ?? []).map((offer) => (
            <PlanCard
              key={offer.plan}
              offer={offer}
              currency={currency}
              isCurrent={currentPlan === offer.plan}
              busy={busy}
              onBuy={() => void startCheckout(offer.plan)}
              t={t}
              limit={limit}
            />
          ))}
        </div>

        <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <ShieldCheck className="size-3.5" />
          {data?.checkout_ready ? t("bl.checkoutHint") : t("bl.notConfigured")}
        </p>
      </div>
    </div>
  );
}

function PlanCard({
  offer, currency, isCurrent, busy, onBuy, t, limit,
}: {
  offer: PlanOffer;
  currency: string;
  isCurrent: boolean;
  busy: BillingPlan | null;
  onBuy: () => void;
  t: (key: string) => string;
  limit: (value: number | null | undefined) => string;
}) {
  const recommended = offer.plan === "pro";
  const rows: { label: string; value: string }[] = [
    { label: t("bl.limitMessages"), value: limit(offer.messages) },
    { label: t("bl.limitDocuments"), value: limit(offer.documents) },
    { label: t("bl.limitChannels"), value: limit(offer.channels) },
    { label: t("bl.limitSeats"), value: limit(offer.seats) },
  ];
  return (
    <div
      className={cn(
        "relative flex flex-col rounded-2xl border bg-card p-5 transition-shadow hover:shadow-lg",
        recommended ? "border-primary/40 shadow-md ring-1 ring-primary/15" : "border-border/80",
      )}
    >
      {recommended && (
        <Badge className="absolute -top-2.5 left-5 h-5 gap-1 px-2 text-[10px]">
          <Sparkles className="size-2.5" />
          {t("bl.recommended")}
        </Badge>
      )}

      <div className="flex items-center justify-between">
        <h3 className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">{offer.plan}</h3>
        {isCurrent && <Badge variant="secondary" className="h-5 px-2 text-[10px]">{t("bl.current")}</Badge>}
      </div>

      <p className="mt-3 flex items-baseline gap-1.5">
        <span className="text-3xl font-semibold tracking-tight text-foreground">{offer.price ?? "—"}</span>
        {offer.price && <span className="text-xs text-muted-foreground">{currency} {t("bl.per30days")}</span>}
      </p>

      <div className="mt-4">
        {offer.purchasable ? (
          <Button className="w-full gap-2" disabled={busy !== null} onClick={onBuy}>
            {busy === offer.plan ? <Loader2 className="size-3.5 animate-spin" /> : <CreditCard className="size-3.5" />}
            {t("bl.buy")}
          </Button>
        ) : (
          <p className="rounded-lg bg-muted/40 px-3 py-2.5 text-[11px] leading-5 text-muted-foreground">
            {offer.plan === "free" ? t("bl.freeNote") : t("bl.notForSale")}
          </p>
        )}
      </div>

      {/* The metered allowances first: these are the numbers the gates enforce. */}
      <dl className="mt-5 space-y-2 border-t border-border/70 pt-4 text-xs">
        {rows.map((row) => (
          <div key={row.label} className="flex items-baseline justify-between gap-3">
            <dt className="text-muted-foreground">{row.label}</dt>
            <dd className="shrink-0 font-medium tabular-nums text-foreground">{row.value}</dd>
          </div>
        ))}
      </dl>

      <div className="mt-4 flex-1 border-t border-border/70 pt-4">
        <p className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">{t("bl.included")}</p>
        <ul className="mt-2.5 space-y-2">
          {(offer.included ?? []).map((key) => (
            <li key={key} className="flex items-start gap-2 text-xs leading-5">
              <Check className="mt-0.5 size-3.5 shrink-0 text-primary" />
              <span className="text-foreground/90">{t(key)}</span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
