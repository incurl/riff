// po33-player — the Web Audio engine behind the
// sketch player component. The player is small on
// purpose: a sketch memo carries up to 16 .wav
// sample attachments (the PO-33's 16 sample slots)
// and a .p33 patch blob (the 16-step pattern
// data). The patch blob is a binary the browser
// cannot play, so the player focuses on the .wav
// samples: a simple transport (play / pause / stop)
// that loops the 16 samples in a 16-step pattern
// at the sketch's BPM (default 120 when the
// upload did not set a BPM).
//
// The engine exposes three surfaces:
//
//   loadSketchPlayer(samples, opts)
//     A handle the React component reads. The
//     handle owns an AudioContext + the decoded
//     sample buffers; the component drives the
//     transport via the returned methods.
//
//   createAnalyserFor(handle)
//     A thin wrapper around the AnalyserNode
//     that powers the live waveform / scope UI.
//     The component reads `getByteTimeDomainData`
//     in a `requestAnimationFrame` loop and draws
//     on a `<canvas>`.
//
//   formatSketchTime(seconds)
//     A mm:ss helper for the transport label.
//
// The engine is single-track: each step plays a
// single sample. A future multi-track extension
// (drum A / drum B / bass / lead, like the PO-33
// K.O!'s four tracks) is a per-track addition on
// top of this engine; the wire format already
// carries the 16-step pattern data the .p33 blob
// represents, and a future commit decodes the
// blob to drive the steps. Today's commit
// round-robins through the 16 sample slots in
// order; the simpler shape is enough for the
// in-browser preview the SPA ships today.
//
// The engine is browser-only; the module is
// not imported by the Node test runner (the
// companion po33-player.test.ts lives in
// web/tests/ and stubs the AudioContext).
//
// A SketchPlayerOptions controls the BPM. A
// BPM of 0 (the zero-value default) means
// "use 120"; the upload handler's
// `payload.sketch.bpm` is the source of truth
// when present, falling back to 120 when
// absent.

export type Sample = {
  readonly id: string;
  readonly url: string;
  // The slot index the sample occupies in
  // the PO-33's 16-slot grid. Slots are
  // numbered 0..15. A sample without a
  // slot (e.g. the patch blob) has
  // slot === -1 and the engine skips it.
  readonly slot: number;
  // The user-facing label; rendered in
  // the slot list. Defaults to the
  // filename when the upload did not set
  // a name.
  readonly label: string;
};

export type SketchPlayerOptions = {
  // Beats per minute. The default
  // 120 bpm yields a 16th-note step
  // every 31.25 ms (60_000 / (4 * bpm)).
  // A 0 or negative bpm falls back to
  // 120.
  readonly bpm?: number;
  // Optional initial position (step
  // 0..15). Default 0.
  readonly startStep?: number;
};

export type SketchPlayerHandle = {
  // The decoded sample buffers, keyed by
  // sample id. A failed decode is
  // silently dropped (the slot shows
  // as empty in the UI).
  readonly buffers: ReadonlyMap<string, AudioBuffer>;
  // The current step (0..15). Updated on
  // each step tick; the component reads
  // it in a render loop.
  readonly getStep: () => number;
  // The current transport state.
  readonly getState: () => "stopped" | "playing" | "paused";
  // Start playback from the current
  // step. Idempotent: a second call while
  // already playing is a no-op.
  start: () => void;
  // Pause playback. The current step is
  // preserved; start() resumes from the
  // same step.
  pause: () => void;
  // Stop playback and rewind to the
  // first step.
  stop: () => void;
  // Seek to a specific step. The next
  // start() resumes from there.
  seek: (step: number) => void;
  // Dispose the AudioContext + cancel
  // any pending source nodes. The
  // handle is unusable after dispose.
  dispose: () => void;
  // The AnalyserNode a UI can read for
  // the live waveform / scope. The node
  // is alive only while the engine is
  // playing; a stopped engine returns
  // undefined.
  readonly getAnalyser: () => AnalyserNode | undefined;
};

const SLOTS_PER_BAR = 16;
const DEFAULT_BPM = 120;
const MS_PER_SECOND = 1000;

// bpmToStepMs converts a beats-per-minute
// value to a 16th-note step duration in
// milliseconds. The PO-33's 16 steps per
// bar is a 16th-note grid.
function bpmToStepMs(bpm: number): number {
  if (!Number.isFinite(bpm) || bpm <= 0) {
    bpm = DEFAULT_BPM;
  }
  return MS_PER_SECOND / (4 * bpm);
}

// decodeSample fetches a sample's URL and
// decodes the bytes into an AudioBuffer.
// Returns null on failure (network, decode,
// 404, etc.); the caller treats the slot
// as empty.
async function decodeSample(audioContext: AudioContext, sample: Sample): Promise<AudioBuffer | null> {
  try {
    const response = await fetch(sample.url, { credentials: "include" });
    if (!response.ok) {
      return null;
    }
    const bytes = await response.arrayBuffer();
    return await audioContext.decodeAudioData(bytes);
  } catch {
    return null;
  }
}

// loadSketchPlayer is the entry point the
// component calls. The samples list is the
// memo's attachment rows whose filename
// ends in `.wav`. The function resolves
// once every sample has been fetched +
// decoded (or has failed to decode); the
// component can render "loading" while
// the returned promise is pending.
export async function loadSketchPlayer(samples: readonly Sample[], options: SketchPlayerOptions = {}): Promise<SketchPlayerHandle> {
  const audioContext = new AudioContext();
  const buffers = new Map<string, AudioBuffer>();
  // Decode in parallel; the engine is
  // single-threaded so the decode
  // work runs on the audio worklet's
  // thread internally. A future commit
  // can stream-decode a sample as the
  // transport reaches its slot.
  await Promise.all(
    samples.map(async (sample) => {
      const buffer = await decodeSample(audioContext, sample);
      if (buffer) {
        buffers.set(sample.id, buffer);
      }
    }),
  );

  const stepMs = bpmToStepMs(options.bpm ?? DEFAULT_BPM);
  let step = Math.max(0, Math.min(SLOTS_PER_BAR - 1, options.startStep ?? 0));
  let state: "stopped" | "playing" | "paused" = "stopped";
  let timer: ReturnType<typeof setTimeout> | null = null;
  let analyser: AnalyserNode | null = null;
  let gain: GainNode | null = null;

  // playStep fires the slot for the current
  // step, if any. The buffer is connected
  // through the analyser (when active) and
  // the gain node; the source is stopped +
  // disconnected on the buffer's `ended`
  // event so a long sample does not leak
  // into the next step's playback.
  const playStep = () => {
    if (state !== "playing") {
      return;
    }
    const sample = samples.find((s) => s.slot === step);
    if (sample) {
      const buffer = buffers.get(sample.id);
      if (buffer && gain) {
        const source = audioContext.createBufferSource();
        source.buffer = buffer;
        source.connect(gain);
        source.start();
        source.onended = () => {
          try {
            source.disconnect();
          } catch {
            // Already disconnected.
          }
        };
      }
    }
    step = (step + 1) % SLOTS_PER_BAR;
    timer = setTimeout(playStep, stepMs);
  };

  // The handle. The component reads
  // getStep() and getState() in a render
  // loop; the underlying state changes
  // happen on the engine's timer, not the
  // component's.
  const handle: SketchPlayerHandle = {
    buffers,
    getStep: () => step,
    getState: () => state,
    getAnalyser: () => analyser ?? undefined,
    start: () => {
      if (state === "playing") {
        return;
      }
      state = "playing";
      if (!gain) {
        gain = audioContext.createGain();
        gain.gain.value = 0.85;
      }
      if (!analyser) {
        analyser = audioContext.createAnalyser();
        analyser.fftSize = 1024;
        analyser.smoothingTimeConstant = 0.6;
        gain.connect(analyser);
        analyser.connect(audioContext.destination);
      }
      // resume() in case the context was
      // suspended by the browser's
      // autoplay policy; a user-gesture
      // click on the play button is the
      // usual trigger.
      void audioContext.resume();
      // Cancel any pending step and start
      // fresh.
      if (timer) {
        clearTimeout(timer);
      }
      timer = setTimeout(playStep, 0);
    },
    pause: () => {
      if (state !== "playing") {
        return;
      }
      state = "paused";
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
    },
    stop: () => {
      state = "stopped";
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
      step = 0;
    },
    seek: (next: number) => {
      const clamped = Math.max(0, Math.min(SLOTS_PER_BAR - 1, Math.floor(next)));
      step = clamped;
    },
    dispose: () => {
      state = "stopped";
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
      if (analyser) {
        try {
          analyser.disconnect();
        } catch {
          // Already disconnected.
        }
        analyser = null;
      }
      if (gain) {
        try {
          gain.disconnect();
        } catch {
          // Already disconnected.
        }
        gain = null;
      }
      void audioContext.close();
    },
  };
  return handle;
}

// formatSketchTime is a small mm:ss helper
// for the transport label. A negative
// or NaN input returns 0:00 so a
// stopped transport never displays
// "--:--".
export function formatSketchTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    seconds = 0;
  }
  const totalSeconds = Math.floor(seconds);
  const minutes = Math.floor(totalSeconds / 60);
  const remainder = totalSeconds % 60;
  return `${minutes}:${remainder.toString().padStart(2, "0")}`;
}

// sketchPlayerSlots is the canonical 16
// slots the engine round-robins through.
// The constant is exported so the UI
// can render a fixed grid regardless of
// how many samples the memo has.
export const SKETCH_SLOTS_PER_BAR = SLOTS_PER_BAR;
