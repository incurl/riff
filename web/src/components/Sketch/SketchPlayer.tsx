import { PauseIcon, PlayIcon, SquareIcon } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { loadSketchPlayer, type Sample, SKETCH_SLOTS_PER_BAR, type SketchPlayerHandle } from "@/lib/po33-player";
import { useTranslate } from "@/utils/i18n";
import AnalyserCanvas from "./AnalyserCanvas";

type SketchPlayerProps = {
  // The samples the engine should
  // preload. Each sample is one of the
  // memo's .wav attachments. The .p33
  // patch blob is intentionally NOT in
  // the list — it is binary the browser
  // cannot play, and the SPA's preview
  // is the per-slot waveform, not the
  // patch sequence.
  readonly samples: readonly Sample[];
  // The sketch's BPM. 0 falls back to
  // 120 inside the engine.
  readonly bpm: number;
  // The sketch's version number (the
  // "v3" badge on the sketch card).
  // Rendered as a small label.
  readonly version: number;
  // The device fingerprint the upload
  // stamped on the sketch envelope.
  // Rendered as a small label.
  readonly deviceFingerprint: string;
};

// SketchPlayer is the in-memo-detail
// component that previews a sketch
// memo. The component owns the engine
// (created in a useEffect, disposed on
// unmount) and re-renders on every
// animation frame the engine ticks.
// The render loop reads the engine's
// step / state via the handle's
// getters, so a single source of truth
// (the engine) drives the UI.
const SketchPlayer = ({ samples, bpm, version, deviceFingerprint }: SketchPlayerProps) => {
  const t = useTranslate();
  const handleRef = useRef<SketchPlayerHandle | null>(null);
  const [handle, setHandle] = useState<SketchPlayerHandle | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [step, setStep] = useState(0);
  const [state, setState] = useState<"stopped" | "playing" | "paused">("stopped");

  // Load the engine once per sample
  // list. The dependency on
  // samples.length keeps the
  // re-load stable across renders
  // (a memo with N samples triggers
  // exactly one load); a future
  // commit can switch to a deep
  // equality check.
  useEffect(() => {
    let cancelled = false;
    setLoadError(null);
    setHandle(null);
    void loadSketchPlayer(samples, { bpm })
      .then((h) => {
        if (cancelled) {
          h.dispose();
          return;
        }
        handleRef.current = h;
        setHandle(h);
      })
      .catch((err: unknown) => {
        if (cancelled) {
          return;
        }
        setLoadError(err instanceof Error ? err.message : "load failed");
      });
    return () => {
      cancelled = true;
      handleRef.current?.dispose();
      handleRef.current = null;
    };
  }, [samples, bpm]);

  // Render loop. The component reads
  // the handle's step / state on
  // every animation frame so the
  // slot-grid highlight + transport
  // label stay in sync with the
  // engine timer. A stopped engine
  // does not advance the step.
  useEffect(() => {
    if (!handle) {
      return;
    }
    let raf = 0;
    const tick = () => {
      setStep(handle.getStep());
      setState(handle.getState());
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => {
      cancelAnimationFrame(raf);
    };
  }, [handle]);

  // The slot grid is a 4×4 layout
  // (16 slots) with the current step
  // highlighted. The grid renders
  // even when the engine is stopped;
  // an empty slot has no border
  // accent.
  const slotGrid = useMemo(() => {
    const cells: number[] = [];
    for (let i = 0; i < SKETCH_SLOTS_PER_BAR; i++) {
      cells.push(i);
    }
    return cells;
  }, []);

  const isLoading = !handle && !loadError;
  const hasNoSamples = samples.length === 0;

  return (
    <section
      className="border-border bg-card text-card-foreground mt-4 rounded-lg border p-4 shadow-sm"
      aria-label={t("memo.sketch.player.title")}
    >
      <header className="mb-3 flex items-center justify-between gap-2">
        <div className="flex flex-col">
          <h3 className="text-sm font-semibold">{t("memo.sketch.player.title")}</h3>
          <div className="text-muted-foreground text-xs">
            {version > 0 ? t("memo.sketch.player.version", { version }) : null}
            {version > 0 && deviceFingerprint ? " · " : null}
            {deviceFingerprint ? t("memo.sketch.player.device-fingerprint", { fingerprint: deviceFingerprint.slice(0, 8) }) : null}
          </div>
        </div>
        <div className="text-muted-foreground text-xs tabular-nums">{bpm > 0 ? t("memo.sketch.player.bpm", { bpm }) : null}</div>
      </header>

      <div className="mb-3">
        <AnalyserCanvas analyser={handle?.getAnalyser()} width={320} height={64} />
      </div>

      <div className="mb-3 grid grid-cols-8 gap-1 sm:grid-cols-16" role="list" aria-label="Step grid">
        {slotGrid.map((slotIndex) => {
          const filled = samples.some((s) => s.slot === slotIndex);
          const current = slotIndex === step && state === "playing";
          return (
            <div
              key={slotIndex}
              role="listitem"
              aria-current={current ? "step" : undefined}
              className={[
                "h-6 rounded-sm border",
                current ? "border-primary bg-primary/30" : "border-border",
                filled ? "bg-muted" : "bg-background",
              ].join(" ")}
            />
          );
        })}
      </div>

      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          {state === "playing" ? (
            <Button
              type="button"
              size="sm"
              variant="secondary"
              aria-label={t("memo.sketch.player.pause")}
              onClick={() => handle?.pause()}
              disabled={!handle}
            >
              <PauseIcon className="h-4 w-4" />
            </Button>
          ) : (
            <Button
              type="button"
              size="sm"
              variant="secondary"
              aria-label={t("memo.sketch.player.play")}
              onClick={() => handle?.start()}
              disabled={!handle}
            >
              <PlayIcon className="h-4 w-4" />
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            variant="ghost"
            aria-label={t("memo.sketch.player.stop")}
            onClick={() => handle?.stop()}
            disabled={!handle}
          >
            <SquareIcon className="h-4 w-4" />
          </Button>
        </div>
        <div className="text-muted-foreground text-xs tabular-nums">{t("memo.sketch.player.step", { current: step + 1 })}</div>
      </div>

      {isLoading ? <p className="text-muted-foreground mt-3 text-xs">{t("memo.sketch.player.loading")}</p> : null}
      {loadError ? (
        <p className="text-destructive mt-3 text-xs" role="alert">
          {loadError}
        </p>
      ) : null}
      {hasNoSamples && !isLoading ? <p className="text-muted-foreground mt-3 text-xs">{t("memo.sketch.player.no-samples")}</p> : null}
    </section>
  );
};

export default SketchPlayer;
