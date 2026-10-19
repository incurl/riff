import type { PushStatusState } from "@/lib/sketch-push";
import { useTranslate } from "@/utils/i18n";

type PushStatusBadgeProps = {
  readonly state: PushStatusState;
};

// PushStatusBadge is the small label that
// appears next to the "Push" button. The
// four states map to four Tailwind color
// tokens: queued/sending are amber, delivered
// is green, failed is red, expired is muted.
// The component is a pure mapping; the
// dialog owns the polling logic and passes
// the latest state.

const STATE_CLASS: Record<PushStatusState, string> = {
  queued: "bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100",
  sending: "bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100",
  delivered: "bg-emerald-100 text-emerald-900 dark:bg-emerald-900/40 dark:text-emerald-100",
  failed: "bg-rose-100 text-rose-900 dark:bg-rose-900/40 dark:text-rose-100",
  expired: "bg-muted text-muted-foreground",
};

const PushStatusBadge = ({ state }: PushStatusBadgeProps) => {
  const t = useTranslate();
  // The five states map to five locale
  // keys; the switch keeps the `t()`
  // call's argument literal-typed so the
  // TypedT signature is satisfied.
  let label: string;
  switch (state) {
    case "queued":
      label = t("memo.sketch.push.queued");
      break;
    case "sending":
      label = t("memo.sketch.push.sending");
      break;
    case "delivered":
      label = t("memo.sketch.push.delivered");
      break;
    case "failed":
      label = t("memo.sketch.push.failed");
      break;
    case "expired":
      label = t("memo.sketch.push.expired");
      break;
  }
  return (
    <span
      role="status"
      aria-live="polite"
      className={`inline-flex items-center rounded-md px-2 py-1 text-xs font-medium ${STATE_CLASS[state]}`}
    >
      {label}
    </span>
  );
};

export default PushStatusBadge;
