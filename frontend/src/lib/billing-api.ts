// Billing catalogue and one-off checkout (PayPal and KHQR).
//
// These calls go through the shared apiFetch (token, API base, error shape) but
// live in their own module because they form one flow — catalogue → order →
// pay → confirm — and the page that drives it is the only consumer.
//
// Both channels are one-off 30-day purchases, never subscriptions (spec
// docs/khqrpay-integration-spec.md §一): PayPal redirects and calls back, KHQR
// shows a QR that is polled. Neither replaces the other.
import { apiFetch } from "@/lib/api";

export type BillingPlan = "free" | "pro" | "enterprise";

export interface PlanOffer {
  plan: BillingPlan;
  /** Absent when the plan has no configured price (not for sale). */
  price?: string;
  /**
   * Price for this plan when it is bought over KHQR instead of PayPal (the
   * backend's KHQRPAY_PRICE_*). Absent means "this plan is not sold over KHQR"
   * — the QR button is hidden. It is deliberately independent of `purchasable`,
   * which describes the PayPal side only.
   */
  khqr_price?: string;
  purchasable: boolean;
  /** null = unlimited (the backend sends null rather than a sentinel number). */
  messages: number;
  documents: number | null;
  channels: number | null;
  seats: number | null;
  /** i18n keys for what the plan includes, e.g. "bl.feature.kb". */
  included: string[];
}

export interface BillingStatus {
  plan: BillingPlan;
  paid_until?: string | null;
  messages_used?: number;
  monthly_message_quota?: number;
  docs_used?: number;
  monthly_doc_quota?: number;
  /** Active agent seats in use; null when the count could not be read. */
  seats_used?: number | null;
  /** null = unlimited, matching PlanOffer.seats. */
  seats_quota?: number | null;
  cycle_end?: string;
}

export interface BillingCatalog {
  currency: string;
  checkout_ready: boolean;
  /**
   * Deployment switch for the KHQR channel (the backend's KHQRPAY_ENABLED).
   * Optional on purpose: `false` and "the backend serving this payload predates
   * the channel" (this bundle can be live before those routes are) must behave
   * the same way, so anything other than `true` means "offer nothing". A switch
   * the operator can flip at any time must never be assumed on.
   */
  khqr_enabled?: boolean;
  plans: PlanOffer[];
  current: BillingStatus;
}

export interface PaypalOrder {
  order_id: string;
  approve_url: string;
  plan: BillingPlan;
  amount: string;
  currency: string;
}

export interface PaypalCaptureResult {
  captured: boolean;
  /** True when the order had already been granted (webhook won the race). */
  already?: boolean;
  current: BillingStatus;
}

/**
 * The four states POST .../status reports, and the only ones the dialog renders
 * (spec §四.3). `pending` covers "created and not yet expired"; the server
 * collapses its own `created`/`captured`/`failed` rows into these.
 */
export type KhqrState = "pending" | "paid" | "failed" | "expired";

export interface KhqrOrder {
  /** Our order id (`MC-<user>-<unix>-<4>`); also the polling key. */
  transaction_id: string;
  /** Raw KHQR payload, used when the vendor image cannot be loaded. */
  qr: string;
  /** Vendor-hosted QR image (khqr.cc). */
  qr_url: string;
  plan: BillingPlan;
  amount: string;
  currency: string;
  /** When the QR stops being payable (default 15 minutes server-side). */
  expires_at: string;
}

export interface KhqrStatus {
  status: KhqrState;
  /** True only for `paid`; kept alongside `status` because the server sets it. */
  paid: boolean;
  /** Set once paid: the new period end, same value the billing card shows. */
  paid_until?: string | null;
  /** Set once paid: the entitlement the payment produced. */
  current?: BillingStatus;
}

export function getBillingCatalog(): Promise<BillingCatalog> {
  return apiFetch<BillingCatalog>("/billing/plans");
}

export function createPaypalOrder(plan: BillingPlan): Promise<PaypalOrder> {
  return apiFetch<PaypalOrder>("/billing/paypal/order", {
    method: "POST",
    body: JSON.stringify({ plan }),
  });
}

export function capturePaypalOrder(orderId: string): Promise<PaypalCaptureResult> {
  return apiFetch<PaypalCaptureResult>("/billing/paypal/capture", {
    method: "POST",
    body: JSON.stringify({ order_id: orderId }),
  });
}

/** Creates one KHQR order; the QR it returns is shown in a dialog and polled. */
export function createKhqrOrder(plan: BillingPlan): Promise<KhqrOrder> {
  return apiFetch<KhqrOrder>("/billing/khqr/order", {
    method: "POST",
    body: JSON.stringify({ plan }),
  });
}

// Polls one KHQR order. Reading it never grants anything: `paid` is written by
// the webhook (and, where configured, the server's own verify call), so a lost
// or forged poll response cannot activate a plan — which is the reason the
// dialog is allowed to poll at all.
export function getKhqrStatus(transactionId: string): Promise<KhqrStatus> {
  return apiFetch<KhqrStatus>(`/billing/khqr/${encodeURIComponent(transactionId)}/status`);
}
