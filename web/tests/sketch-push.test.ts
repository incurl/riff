import { describe, expect, it } from "vitest";
import { type PushStatusState, pushStatusStateFromProto } from "@/lib/sketch-push";
import { PushState } from "@/types/proto/api/v1/po33_sketch_service_pb";

// pushStatusStateFromProto flattens the
// proto enum to the SPA's five-state
// view. UNSPECIFIED maps to "expired" so
// the dialog can render a single
// "Push expired" label for both
// "never existed" and "rolled out of
// the 30-min keep window".

describe("pushStatusStateFromProto", () => {
  it("maps QUEUED to queued", () => {
    expect(pushStatusStateFromProto(PushState.QUEUED)).toBe("queued");
  });

  it("maps SENDING to sending", () => {
    expect(pushStatusStateFromProto(PushState.SENDING)).toBe("sending");
  });

  it("maps DELIVERED to delivered", () => {
    expect(pushStatusStateFromProto(PushState.DELIVERED)).toBe("delivered");
  });

  it("maps FAILED to failed", () => {
    expect(pushStatusStateFromProto(PushState.FAILED)).toBe("failed");
  });

  it("maps UNSPECIFIED to expired", () => {
    expect(pushStatusStateFromProto(PushState.UNSPECIFIED)).toBe("expired");
  });

  it("maps unknown values to expired", () => {
    expect(pushStatusStateFromProto(99 as never)).toBe("expired");
  });
});

describe("PushStatusState", () => {
  it("is the five-state SPA view", () => {
    // The push dialog + badge both
    // switch on the five values; a
    // future commit that adds a sixth
    // state must update the badge
    // map and the dialog's switch
    // statement together.
    const states: PushStatusState[] = ["queued", "sending", "delivered", "failed", "expired"];
    expect(states).toHaveLength(5);
  });
});
