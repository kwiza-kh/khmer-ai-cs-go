"use client";

import * as React from "react";
import { useSearchParams } from "next/navigation";
import useSWR from "swr";
import { Check, CircleCheck, CreditCard, Loader2, QrCode, RefreshCw, ShieldCheck, Sparkles, Timer, TriangleAlert } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { useAuth } from "@/lib/auth-client";
import { fmtDate, fmtInt } from "@/lib/format";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import {
  capturePaypalOrder,
  createKhqrOrder,
  createPaypalOrder,
  getBillingCatalog,
  getKhqrStatus,
  type BillingCatalog,
  type BillingPlan,
  type BillingStatus,
  type KhqrOrder,
  type KhqrStatus,
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

// KHQR (spec docs/khqrpay-integration-spec.md §五) is a second, independent
// one-off channel, sold beside PayPal, never instead of it. A plan offers it
// only when the deployment switched the channel on AND configured a price for
// that plan — two separate backend fields, because an operator turning the
// channel off does not clear the prices, and a button that answers 503 is worse
// than no button.
//
// Both fields are read defensively (`=== true`, absent price = not offered) so a
// frontend that ships before the backend routes exist shows nothing at all
// rather than a dead entry point.
function khqrOffered(catalog: BillingCatalog | undefined, offer: PlanOffer): boolean {
  return catalog?.khqr_enabled === true && Boolean(offer.khqr_price);
}

// How often the open QR dialog asks whether it has been paid. The QR lives 15
// minutes, so even a buyer who walks away costs 300 status reads — cheap enough
// that a paid plan appears within three seconds of the webhook landing.
const KHQR_POLL_MS = 3000;

// Buying a plan: the catalogue comes from the server (prices are the
// deployment's, not this bundle's), the order is created server-side, the buyer
// goes to PayPal and returns with ?token=<order id>, which triggers the capture.
//
// The capture runs through SWR rather than an effect: it is a one-shot side
// effect keyed by the order id, and the server is idempotent, so a reload — or a
// webhook that got there first — cannot grant a second month.
export default function BillingPage() {
  const { t, tf } = useI18n();
  const { user } = useAuth();
  // Checkout is owner-only (the API refuses a seat); a member still sees the
  // catalogue and the current plan.
  const canBuy = !user?.is_member;
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

  // KHQR: the order creates a QR that lives outside this page, so the state here
  // is only "which order is showing" and "a new one is being created". Closing
  // the dialog clears the order, which unmounts the poller with it.
  const [khqrOrder, setKhqrOrder] = React.useState<KhqrOrder | null>(null);
  const [khqrBusy, setKhqrBusy] = React.useState<BillingPlan | null>(null);

  const startKhqr = async (plan: BillingPlan) => {
    setKhqrBusy(plan);
    try {
      // Regenerating replaces the order rather than mutating it: the new
      // transaction id re-keys the dialog's poller and the QR the buyer scans.
      setKhqrOrder(await createKhqrOrder(plan));
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setKhqrBusy(null);
    }
  };

  const current: BillingStatus | undefined = data?.current;
  const currency = data?.currency || "USD";
  const currentPlan = current?.plan ?? "free";
  // Whether this deployment can take money over KHQR at all — a plan-level
  // switch is not enough on its own: the channel can be enabled with no plan
  // priced for it, and then it sells nothing. The footer uses this rather than
  // the raw switch so it cannot advertise a channel that would answer 503.
  const khqrSellable = (data?.plans ?? []).some((offer) => khqrOffered(data, offer));
  const percent = (used?: number | null, quota?: number | null) => {
    if (!used || !quota || !Number.isFinite(quota)) return 0;
    return Math.min(100, Math.round((used / quota) * 100));
  };
  const limit = (value: number | null | undefined) =>
    value === null || value === undefined ? t("bl.unlimited") : fmtInt(value);
  // A used count of null means the server could not read it; rendering that as
  // 0 would be a number the merchant acts on.
  const usedText = (value?: number | null) =>
    value === null || value === undefined ? "—" : fmtInt(value);

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
            <Badge variant="secondary" className="ml-1 h-5 px-2 text-label uppercase">{capture.current?.plan ?? ""}</Badge>
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
                ? tf("bl.paidUntil", { date: fmtDate(current.paid_until) })
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
                  <div className="flex justify-between text-label">
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
            {current?.cycle_end && <p>{tf("bl.cycleEnds", { date: fmtDate(current.cycle_end) })}</p>}
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
              khqrOffered={khqrOffered(data, offer)}
              khqrBusy={khqrBusy}
              onKhqr={() => void startKhqr(offer.plan)}
              canBuy={canBuy}
              t={t}
              limit={limit}
            />
          ))}
        </div>

        <p className="flex items-center gap-1.5 text-label text-muted-foreground">
          <ShieldCheck className="size-3.5" />
          {/* PayPal's hint, else KHQR's, else the honest "nothing is on sale".
              Each names the channel it describes: telling a merchant who can
              only pay by QR to "wait for checkout to be configured" would read
              as a broken deployment. */}
          {data?.checkout_ready
            ? t("bl.checkoutHint")
            : khqrSellable
              ? t("bl.checkoutKhqrOnly")
              : t("bl.notConfigured")}
        </p>
      </div>

      {/* The dialog owns its poller: it exists only while an order is set, so
          closing it (order → null) unmounts the poller with it. Keyed by order
          id so "generate again" gets a fresh QR, countdown and poll loop rather
          than a stale one under new digits. */}
      {khqrOrder && (
        <KhqrDialog
          key={khqrOrder.transaction_id}
          order={khqrOrder}
          regenerating={khqrBusy !== null}
          onClose={() => setKhqrOrder(null)}
          onRegenerate={() => void startKhqr(khqrOrder.plan)}
          onPaid={() => void mutate()}
          t={t}
          tf={tf}
        />
      )}
    </div>
  );
}

function PlanCard({
  offer, currency, isCurrent, busy, onBuy, khqrOffered: khqrAvailable, khqrBusy, onKhqr, canBuy, t, limit,
}: {
  offer: PlanOffer;
  currency: string;
  isCurrent: boolean;
  busy: BillingPlan | null;
  onBuy: () => void;
  /** The deployment sells this plan over KHQR (switch on + price configured). */
  khqrOffered: boolean;
  khqrBusy: BillingPlan | null;
  onKhqr: () => void;
  canBuy: boolean;
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
        <Badge className="absolute -top-2.5 left-5 h-5 gap-1 px-2 text-label">
          <Sparkles className="size-2.5" />
          {t("bl.recommended")}
        </Badge>
      )}

      <div className="flex items-center justify-between">
        <h3 className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">{offer.plan}</h3>
        {isCurrent && <Badge variant="secondary" className="h-5 px-2 text-label">{t("bl.current")}</Badge>}
      </div>

      <p className="mt-3 flex items-baseline gap-1.5">
        <span className="text-3xl font-semibold tracking-tight text-foreground">{offer.price ?? "—"}</span>
        {offer.price && <span className="text-xs text-muted-foreground">{currency} {t("bl.per30days")}</span>}
      </p>

      <div className="mt-4 space-y-2">
        {offer.purchasable && canBuy ? (
          <Button className="w-full gap-2" disabled={busy !== null} onClick={onBuy}>
            {busy === offer.plan ? <Loader2 className="size-3.5 animate-spin" /> : <CreditCard className="size-3.5" />}
            {t("bl.buy")}
          </Button>
        ) : (
          <p className="rounded-lg bg-muted/40 px-3 py-2.5 text-label leading-5 text-muted-foreground">
            {offer.plan === "free" ? t("bl.freeNote") : !canBuy ? t("bl.ownerOnly") : khqrAvailable ? t("bl.khqrOnlyHint") : t("bl.notForSale")}
          </p>
        )}
        {/* Beside PayPal, not instead of it: `purchasable` describes the PayPal
            side, so the QR entry point is gated on its own fields. Stacked
            rather than side by side because the three locales make these labels
            wide ("扫码支付（KHQR）" wraps at half a card's width). */}
        {khqrAvailable && canBuy && (
          <Button variant="outline" className="w-full gap-2" disabled={busy !== null || khqrBusy !== null} onClick={onKhqr}>
            {khqrBusy === offer.plan ? <Loader2 className="size-3.5 animate-spin" /> : <QrCode className="size-3.5" />}
            {t("bl.khqrButton")}
          </Button>
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
        <p className="text-label font-semibold uppercase tracking-[0.12em] text-muted-foreground">{t("bl.included")}</p>
        <ul className="mt-2.5 space-y-2">
          {(offer.included ?? []).map((key) => (
            <li key={key} className="flex items-start gap-2 text-xs leading-5">
              <Check className="mt-0.5 size-3.5 shrink-0 text-primary" />
              <span className="text-foreground/90">{t(key)}</span>
            </li>
          ))}
        </ul>
      </div>

      {/* Spec §二.3: the compliance warning ships with the code, on the card
          where the buy buttons are — not in a footnote. Only shown when this
          plan is actually sold over that channel; a warning about a channel the
          buyer cannot choose is noise. */}
      {khqrAvailable && (
        <p className="mt-4 flex items-start gap-1.5 border-t border-border/70 pt-3 text-label leading-4 text-muted-foreground">
          <TriangleAlert className="mt-0.5 size-3 shrink-0 text-warning" />
          {t("bl.khqrRisk")}
        </p>
      )}
    </div>
  );
}

// The poll interval handed to SWR. It lives at module scope on purpose: SWR
// re-arms its timer whenever this value's identity changes (its polling effect
// depends on it), and an inline arrow is a new identity on every render — the
// 1 Hz countdown below would then clear the 3-second timer before it ever
// fired. A stable function is called with the cached data on each tick, so the
// terminal check still sees the newest status.
function khqrPollInterval(latest?: KhqrStatus): number {
  return latest && latest.status !== "pending" ? 0 : KHQR_POLL_MS;
}

// The QR dialog: one order, shown until the buyer closes it.
//
// It owns the two things the page must not: the countdown (a timer, which has to
// be cleared when the dialog closes) and the status poll (SWR, whose key dies
// with this component). `paid` is rendered from the STATUS endpoint, never from
// anything the browser decides — the webhook is what activates the plan, and all
// this screen does is notice.
function KhqrDialog({
  order, regenerating, onClose, onRegenerate, onPaid, t, tf,
}: {
  order: KhqrOrder;
  regenerating: boolean;
  onClose: () => void;
  onRegenerate: () => void;
  /** Paid landed: revalidate the catalogue so the card behind the dialog updates. */
  onPaid: () => void;
  t: (key: string) => string;
  tf: (key: string, params: Record<string, string | number>) => string;
}) {
  // The tick only exists to redraw the countdown; the server owns expiry. This
  // is the one timer the component owns, so it is the one it must clear.
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  // The vendor image is the only rendering of the QR we get. When its host is
  // blocked or down the buyer must still be able to pay, so the raw KHQR string
  // is shown instead (spec §五: "失败则用 qr 原始串兜底").
  const [imageBroken, setImageBroken] = React.useState(false);

  const expiresAt = Date.parse(order.expires_at);
  const remaining = Number.isFinite(expiresAt) ? Math.max(0, expiresAt - now) : null;

  const { data: status, error: statusError } = useSWR<KhqrStatus>(
    `khqr-status-${order.transaction_id}`,
    () => getKhqrStatus(order.transaction_id),
    {
      refreshInterval: khqrPollInterval,
      onSuccess: (latest) => {
        if (latest.paid) onPaid();
      },
    },
  );

  const state = status?.status ?? "pending";
  // The countdown can only bring "expired" forward for the message: if the two
  // clocks disagree, the server's answer wins (it is the one that refuses the
  // money). A null `remaining` means the payload had no usable expires_at, in
  // which case the poll and the server decide everything.
  const view = state === "pending" && remaining === 0 ? "expired" : state;
  const clock =
    remaining === null
      ? ""
      : `${String(Math.floor(remaining / 60000)).padStart(2, "0")}:${String(Math.floor((remaining % 60000) / 1000)).padStart(2, "0")}`;

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <QrCode className="size-4 text-primary" />
            {t("bl.khqrTitle")}
          </DialogTitle>
          <DialogDescription className="text-xs">{t("bl.khqrHint")}</DialogDescription>
        </DialogHeader>

        <div className="flex items-baseline justify-between rounded-lg bg-muted/40 px-3 py-2">
          <span className="text-label font-semibold uppercase tracking-[0.12em] text-muted-foreground">{order.plan}</span>
          <span className="text-sm font-semibold tabular-nums text-foreground">
            {order.amount} {order.currency}
          </span>
        </div>

        <div className="flex flex-col items-center gap-3">
          {imageBroken ? (
            <div className="w-full space-y-2 rounded-lg border border-dashed border-border px-3 py-2.5">
              <p className="flex items-start gap-1.5 text-xs leading-5 text-muted-foreground">
                <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-warning" />
                {t("bl.khqrQrFallback")}
              </p>
              <code className="block max-h-24 overflow-auto break-all rounded bg-muted/50 px-2 py-1.5 text-label leading-5 text-foreground">
                {order.qr}
              </code>
            </div>
          ) : (
            /* eslint-disable-next-line @next/next/no-img-element */
            <img
              src={order.qr_url}
              alt={t("bl.khqrTitle")}
              // Plain <img>: the host is the vendor's, chosen at runtime, so it
              // cannot be a next/image remotePattern. White plate on purpose —
              // a QR drawn on the dark card does not scan.
              className="size-56 rounded-xl bg-white p-2 ring-1 ring-border"
              onError={() => setImageBroken(true)}
            />
          )}

          {view === "pending" && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" />
              <span>{t("bl.khqrWaiting")}</span>
              {clock !== "" && (
                <span className="inline-flex items-center gap-1 text-xs tabular-nums">
                  <Timer className="size-3.5" />
                  {clock}
                </span>
              )}
            </div>
          )}
          {view === "paid" && (
            <div className="w-full space-y-1 rounded-lg border border-primary/40 bg-primary/5 px-3 py-2.5">
              <p className="flex items-center gap-2 text-sm font-medium text-foreground">
                <CircleCheck className="size-4 text-primary" />
                {t("bl.khqrPaid")}
              </p>
              {status?.paid_until && (
                <p className="text-xs text-muted-foreground">{tf("bl.paidUntil", { date: fmtDate(status.paid_until) })}</p>
              )}
            </div>
          )}
          {(view === "expired" || view === "failed") && (
            <p className="flex items-center gap-2 text-sm text-destructive">
              <TriangleAlert className="size-4 shrink-0" />
              {view === "expired" ? t("bl.khqrExpired") : t("bl.khqrFailed")}
            </p>
          )}
          {/* A failed status read is not a failed payment: say so without
              replacing the waiting/paid state, which the server still owns. */}
          {statusError && <p className="text-label text-destructive">{(statusError as Error).message}</p>}
        </div>

        <DialogFooter>
          {(view === "expired" || view === "failed") && (
            <Button className="gap-2" disabled={regenerating} onClick={onRegenerate}>
              {regenerating ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
              {t("bl.khqrRegenerate")}
            </Button>
          )}
          <Button variant="outline" onClick={onClose}>{t("common.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
