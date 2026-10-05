// Billing catalogue and PayPal checkout.
//
// These three calls go through the shared apiFetch (token, API base, error
// shape) but live in their own module because they form one flow — catalogue →
// order → capture — and the page that drives it is the only consumer.
import { apiFetch } from "@/lib/api";

export type BillingPlan = "free" | "pro" | "enterprise";

export interface PlanOffer {
  plan: BillingPlan;
  /** Absent when the plan has no configured price (not for sale). */
  price?: string;
  purchasable: boolean;
}

export interface BillingStatus {
  plan: BillingPlan;
  paid_until?: string | null;
  messages_used?: number;
  monthly_message_quota?: number;
  docs_used?: number;
  monthly_doc_quota?: number;
  cycle_end?: string;
}

export interface BillingCatalog {
  currency: string;
  checkout_ready: boolean;
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

export async function getBillingCatalog(): Promise<BillingCatalog> {
  return apiFetch<BillingCatalog>("/billing/plans");
}

export async function createPaypalOrder(plan: BillingPlan): Promise<PaypalOrder> {
  return apiFetch<PaypalOrder>("/billing/paypal/order", {
    method: "POST",
    body: JSON.stringify({ plan }),
  });
}

export async function capturePaypalOrder(orderId: string): Promise<PaypalCaptureResult> {
  return apiFetch<PaypalCaptureResult>("/billing/paypal/capture", {
    method: "POST",
    body: JSON.stringify({ order_id: orderId }),
  });
}
