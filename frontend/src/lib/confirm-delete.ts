// Confirmation helper for destructive actions.
//
// Every delete control should ask first and report failures — a mis-tap that
// silently removes an SLA policy or a webhook subscription is unrecoverable
// for the operator, and a failed request that shows nothing is worse.
import { toast } from "sonner";
import { localizeCurrentLang } from "./api-errors";

/**
 * confirmDelete — ask the user, run the action, report the outcome.
 *
 * @param message  Localized question, e.g. `Delete "Night SLA"?`
 * @param action   The async delete call.
 * @param onDone   Refresh callback (usually SWR's `mutate`).
 * @returns true when the action ran successfully.
 */
export async function confirmDelete(
  message: string,
  action: () => Promise<unknown>,
  // SWR's mutate returns the revalidated data, so the callback is typed loosely.
  onDone?: () => unknown,
): Promise<boolean> {
  if (typeof window !== "undefined" && !window.confirm(message)) return false;
  try {
    await action();
    await onDone?.();
    return true;
  } catch (err) {
    const raw = err instanceof Error ? err.message : "";
    toast.error(localizeCurrentLang(raw) || "Delete failed");
    return false;
  }
}
