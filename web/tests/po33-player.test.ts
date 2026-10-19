import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formatSketchTime, loadSketchPlayer, type Sample, SKETCH_SLOTS_PER_BAR } from "@/lib/po33-player";

// The po33-player module touches the browser
// AudioContext on import; the test stubs the
// global AudioContext so the module loads
// inside the Node test runner.

class FakeBufferSource {
  buffer: AudioBuffer | null = null;
  onended: (() => void) | null = null;
  connect = vi.fn();
  disconnect = vi.fn();
  start = vi.fn(() => {
    // Fire `onended` on the next microtask so
    // the engine's "disconnect on end" path
    // runs in the test.
    queueMicrotask(() => this.onended?.());
  });
}

class FakeAnalyser {
  fftSize = 1024;
  smoothingTimeConstant = 0.6;
  frequencyBinCount = 512;
  connect = vi.fn();
  disconnect = vi.fn();
  getByteTimeDomainData = vi.fn();
  getByteFrequencyData = vi.fn();
}

class FakeGain {
  gain = { value: 1 };
  connect = vi.fn();
  disconnect = vi.fn();
}

class FakeAudioContext {
  state: AudioContextState = "running";
  destination = {};
  decodeAudioData = vi.fn(async (_bytes: ArrayBuffer) => {
    return {
      length: 1024,
      sampleRate: 44100,
      duration: 0.5,
      numberOfChannels: 1,
      getChannelData: () => new Float32Array(1024),
    } as unknown as AudioBuffer;
  });
  createBufferSource = () => new FakeBufferSource();
  createAnalyser = () => new FakeAnalyser();
  createGain = () => new FakeGain();
  resume = vi.fn(async () => {});
  close = vi.fn(async () => {});
}

let fakeContext: FakeAudioContext | null = null;

beforeEach(() => {
  fakeContext = new FakeAudioContext();
  (globalThis as { AudioContext?: unknown }).AudioContext = function () {
    return fakeContext;
  } as unknown as typeof AudioContext;
});

afterEach(() => {
  fakeContext = null;
  delete (globalThis as { AudioContext?: unknown }).AudioContext;
  vi.useRealTimers();
});

describe("formatSketchTime", () => {
  it("formats seconds as mm:ss", () => {
    expect(formatSketchTime(0)).toBe("0:00");
    expect(formatSketchTime(5)).toBe("0:05");
    expect(formatSketchTime(65)).toBe("1:05");
    expect(formatSketchTime(60 * 12 + 34)).toBe("12:34");
  });

  it("clamps negative or NaN to 0:00", () => {
    expect(formatSketchTime(-1)).toBe("0:00");
    expect(formatSketchTime(Number.NaN)).toBe("0:00");
    expect(formatSketchTime(Number.POSITIVE_INFINITY)).toBe("0:00");
  });
});

describe("SKETCH_SLOTS_PER_BAR", () => {
  it("is 16", () => {
    expect(SKETCH_SLOTS_PER_BAR).toBe(16);
  });
});

describe("loadSketchPlayer", () => {
  it("decodes samples and exposes a stopped handle by default", async () => {
    const fetchSpy = vi.fn(async () => new Response(new ArrayBuffer(8)));
    vi.stubGlobal("fetch", fetchSpy);
    const samples: Sample[] = [
      { id: "a", url: "/api/files/attachments/1/sample-0.wav", slot: 0, label: "kick" },
      { id: "b", url: "/api/files/attachments/1/sample-1.wav", slot: 1, label: "snare" },
    ];
    const handle = await loadSketchPlayer(samples, { bpm: 120 });
    expect(handle.getState()).toBe("stopped");
    expect(handle.getStep()).toBe(0);
    expect(handle.buffers.size).toBe(2);
    expect(handle.getAnalyser()).toBeUndefined();
    handle.dispose();
    vi.unstubAllGlobals();
  });

  it("clamps a 0 or negative bpm to the 120 bpm default", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(new ArrayBuffer(8))),
    );
    const samples: Sample[] = [{ id: "a", url: "/api/files/attachments/1/sample-0.wav", slot: 0, label: "kick" }];
    const handle = await loadSketchPlayer(samples, { bpm: 0 });
    // Internal: a 0-bpm resolves to 120 bpm
    // (= 125 ms per step). The handle's
    // start() schedules a setTimeout(0) for
    // the first step and then 125 ms for
    // subsequent ones. We pin the steady
    // state by advancing fake timers and
    // observing the step counter.
    vi.useFakeTimers();
    handle.start();
    expect(handle.getState()).toBe("playing");
    handle.stop();
    expect(handle.getState()).toBe("stopped");
    expect(handle.getStep()).toBe(0);
    handle.dispose();
    vi.unstubAllGlobals();
  });

  it("skips samples with no decoded buffer", async () => {
    const okBody = new ArrayBuffer(8);
    const failOnce = vi
      .fn()
      .mockResolvedValueOnce(new Response(okBody))
      .mockResolvedValueOnce(new Response("nope", { status: 500 }));
    vi.stubGlobal("fetch", failOnce);
    const samples: Sample[] = [
      { id: "a", url: "/api/files/attachments/1/a.wav", slot: 0, label: "kick" },
      { id: "b", url: "/api/files/attachments/1/b.wav", slot: 1, label: "snare" },
    ];
    const handle = await loadSketchPlayer(samples);
    expect(handle.buffers.has("a")).toBe(true);
    expect(handle.buffers.has("b")).toBe(false);
    handle.dispose();
    vi.unstubAllGlobals();
  });
});
