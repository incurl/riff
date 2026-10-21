import { useEffect, useState } from "react";
import { toast } from "react-hot-toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useDeviceList } from "@/hooks/useDeviceList";
import { usePushStatus } from "@/hooks/usePushStatus";
import { PushSketchError, PushSketchRateLimitedError, pushSketch } from "@/lib/sketch-push";
import { DeviceStatus } from "@/types/proto/api/v1/po33_device_service_pb";
import { useTranslate } from "@/utils/i18n";
import PushStatusBadge from "./PushStatusBadge";

type PushToDeviceDialogProps = {
  // The memo to push. Format:
  // "memos/{id}". The dialog calls
  // SketchService.PushSketch with
  // (name = memoName, device = picked
  // device).
  readonly memoName: string;
  // The dialog is controlled by the
  // parent (the player).
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
};

// PushToDeviceDialog is the "Push to my
// device" flow the sketch player
// mounts. The dialog is a controlled
// component: the parent owns `open`
// and the dialog calls
// `onOpenChange` when the user
// dismisses. While open, the dialog
// polls getPushStatus every 2s via
// the usePushStatus React Query
// hook; the badge reflects the
// current state.

const PENDING_DEVICE_VALUE = "__pending__";

const PushToDeviceDialog = ({ memoName, open, onOpenChange }: PushToDeviceDialogProps) => {
  const t = useTranslate();
  const { devices, isLoading: devicesLoading } = useDeviceList(open);
  const [selectedDevice, setSelectedDevice] = useState<string>("");
  const [activePushId, setActivePushId] = useState<string>("");
  const [activeDeviceName, setActiveDeviceName] = useState<string>("");
  const [pushError, setPushError] = useState<{ rateLimited: boolean; generic: boolean }>({
    rateLimited: false,
    generic: false,
  });
  const [isPushing, setIsPushing] = useState(false);

  // Reset the local state when the
  // dialog closes; a re-open
  // re-fetches the device list and
  // starts fresh.
  useEffect(() => {
    if (!open) {
      setActivePushId("");
      setActiveDeviceName("");
      setSelectedDevice("");
      setPushError({ rateLimited: false, generic: false });
      setIsPushing(false);
    }
  }, [open]);

  const activeDevices = devices.filter((d) => d.status === DeviceStatus.ACTIVE);
  const deviceNameFor = (deviceId: string) => {
    const d = devices.find((dev) => dev.name === deviceId);
    return d?.name ?? "";
  };

  // The poll hook is only enabled
  // after the user has clicked
  // Push and we have a push_id.
  const { status, error: pollError } = usePushStatus(activeDeviceName, activePushId, {
    enabled: open && Boolean(activePushId) && Boolean(activeDeviceName),
  });

  const handlePush = async () => {
    if (!selectedDevice) {
      return;
    }
    const deviceName = deviceNameFor(selectedDevice);
    if (!deviceName) {
      return;
    }
    setIsPushing(true);
    setPushError({ rateLimited: false, generic: false });
    try {
      const result = await pushSketch(memoName, deviceName);
      setActivePushId(result.pushId);
      setActiveDeviceName(deviceName);
    } catch (error) {
      if (error instanceof PushSketchRateLimitedError) {
        setPushError({ rateLimited: true, generic: false });
      } else if (error instanceof PushSketchError) {
        setPushError({ rateLimited: false, generic: true });
      } else {
        setPushError({ rateLimited: false, generic: true });
      }
      // Surface the failure via the
      // toast as well so the user
      // sees a transient confirmation
      // even if they navigate away
      // from the dialog.
      toast.error(t("memo.sketch.push.push-error"));
    } finally {
      setIsPushing(false);
    }
  };

  const shortFingerprint = (fpr: string) => fpr.slice(0, 8);
  const isPolling = Boolean(activePushId);
  const state = status?.state ?? "queued";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("memo.sketch.push.dialog-title")}</DialogTitle>
          <DialogDescription>
            {activeDevices.length === 0 && !devicesLoading ? t("memo.sketch.push.no-devices") : t("memo.sketch.push.select-device")}
          </DialogDescription>
        </DialogHeader>

        {activeDevices.length > 0 ? (
          <div className="flex flex-col gap-3 py-2">
            <Select value={selectedDevice} onValueChange={setSelectedDevice} disabled={isPolling}>
              <SelectTrigger>
                <SelectValue placeholder={t("memo.sketch.push.select-device")} />
              </SelectTrigger>
              <SelectContent>
                {activeDevices.map((d) => (
                  <SelectItem key={d.name} value={d.name}>
                    {d.label && d.label.length > 0
                      ? t("memo.sketch.push.device-label", {
                          label: d.label,
                          fingerprint: shortFingerprint(d.publicKeyFingerprint),
                        })
                      : t("memo.sketch.push.device-fingerprint-short", {
                          fingerprint: shortFingerprint(d.publicKeyFingerprint),
                        })}
                  </SelectItem>
                ))}
                {/* Placeholder option that Select never picks; ensures
                    the value="" initial state does not display "". */}
                {!selectedDevice ? (
                  <SelectItem value={PENDING_DEVICE_VALUE} disabled>
                    {t("memo.sketch.push.select-device")}
                  </SelectItem>
                ) : null}
              </SelectContent>
            </Select>

            {isPolling ? (
              <div className="flex items-center gap-2">
                <PushStatusBadge state={state} />
                {status?.failureReason ? <span className="text-muted-foreground text-xs">{status.failureReason}</span> : null}
              </div>
            ) : null}

            {pushError.rateLimited ? (
              <p className="text-destructive text-xs" role="alert">
                {t("memo.sketch.push.rate-limited")}
              </p>
            ) : null}
            {pushError.generic ? (
              <p className="text-destructive text-xs" role="alert">
                {t("memo.sketch.push.push-error")}
              </p>
            ) : null}
            {pollError ? (
              <p className="text-destructive text-xs" role="alert">
                {t("memo.sketch.push.poll-error")}
              </p>
            ) : null}
          </div>
        ) : null}

        <DialogFooter>
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
            {t("memo.sketch.push.cancel")}
          </Button>
          <Button
            type="button"
            onClick={handlePush}
            disabled={!selectedDevice || selectedDevice === PENDING_DEVICE_VALUE || isPushing || isPolling}
          >
            {t("memo.sketch.push.push")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default PushToDeviceDialog;
