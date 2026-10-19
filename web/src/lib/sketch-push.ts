// sketch-push — the client-side helpers for the
// "Push to my device" flow. The flow is
// device-pulls (see DECISIONS.md "Sketch push
// transport is device-pulls", 2026-10-19):
//
//   1. SPA calls sketchServiceClient.pushSketch
//      to queue a `pending_pull` envelope on
//      the device row.
//   2. SPA polls sketchServiceClient.getPushStatus
//      every 2s; the response drives the
//      four-state badge (queued / sending /
//      delivered / failed).
//   3. Device's poll picks up the envelope
//      (server-side concern, not the SPA's).
//   4. Device acks; the badge flips to
//      delivered.
//
// The SPA never sees the signed URL after
// pushSketch returns it; the device polls
// the server directly. The signed URL is
// returned in the response so a future
// server-rendered email notification can
// deep-link the device, but the SPA
// ignores it today.
//
// The two helpers here are:
//   - pushSketch(memoName, deviceName)
//     A thin wrapper around the connect
//     client that returns the push_id
//     (or throws on a 4xx/5xx).
//   - pollPushStatus(deviceName, pushId)
//     A thin wrapper around the connect
//     client that returns the push state.
//
// The polling cadence is the caller's
// concern; the components use
// `usePushStatus(deviceName, pushId)`
// in `usePushStatus.ts` (sketch-push's
// React Query hook) to get a stable
// 2s poll.

import { create } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { sketchServiceClient } from "@/connect";
import {
  type GetPushStatusRequest,
  GetPushStatusRequestSchema,
  type PushSketchRequest,
  PushSketchRequestSchema,
  type PushSketchResponse,
  PushState,
  type PushStatus as PushStatusProto,
} from "@/types/proto/api/v1/po33_sketch_service_pb";

// PushSketchResult is the value the SPA's
// "Push to my device" dialog uses to start
// the badge poll.
export type PushSketchResult = {
  readonly pushId: string;
  readonly signedUrl: string;
  readonly signedExpiresAt: Date;
};

// pushSketch queues a sketch memo for
// delivery to a bound PO-33 device. The
// memo name is "memos/{id}"; the device
// name is "po33-devices/{id}". The
// returned push_id is the value the
// caller passes to `pollPushStatus` to
// drive the badge.
export async function pushSketch(memoName: string, deviceName: string): Promise<PushSketchResult> {
  const request: PushSketchRequest = create(PushSketchRequestSchema, {
    name: memoName,
    device: deviceName,
  });
  let response: PushSketchResponse;
  try {
    response = await sketchServiceClient.pushSketch(request);
  } catch (error) {
    if (error instanceof ConnectError && error.code === Code.ResourceExhausted) {
      throw new PushSketchRateLimitedError("push rate limited");
    }
    throw new PushSketchError("could not queue the push", error);
  }
  return {
    pushId: response.pushId,
    signedUrl: response.signedUrl,
    signedExpiresAt: response.signedExpiresAt ? timestampDate(response.signedExpiresAt) : new Date(),
  };
}

// PushSketchRateLimitedError is the
// specific failure the dialog maps to
// the "rate-limited" UI copy.
export class PushSketchRateLimitedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PushSketchRateLimitedError";
  }
}

// PushSketchError is the catch-all for
// the push path. The dialog's "push-error"
// copy is rendered for any non-rate-limit
// failure.
export class PushSketchError extends Error {
  readonly cause: unknown;
  constructor(message: string, cause: unknown) {
    super(message);
    this.name = "PushSketchError";
    this.cause = cause;
  }
}

// PushStatusState is the SPA's flattened
// view of the four-state badge. The proto
// enum carries an UNSPECIFIED zero value
// the SPA maps to "expired" / "unknown"
// so the dialog can clear its local state
// without rendering a fifth "unspecified"
// label.
export type PushStatusState = "queued" | "sending" | "delivered" | "failed" | "expired";

export type PushStatus = {
  readonly state: PushStatusState;
  readonly pushId: string;
  readonly failureReason: string;
};

// pushStatusStateFromProto flattens the
// proto enum to the SPA's five-state
// view. UNSPECIFIED (the proto zero
// value) maps to "expired" so the
// dialog can render a single
// "Push expired" label for both
// "never existed" and "rolled out
// of the 30-min keep window".
export function pushStatusStateFromProto(proto: number): PushStatusState {
  switch (proto) {
    case PushState.QUEUED:
      return "queued";
    case PushState.SENDING:
      return "sending";
    case PushState.DELIVERED:
      return "delivered";
    case PushState.FAILED:
      return "failed";
    case PushState.UNSPECIFIED:
    default:
      return "expired";
  }
}

// pollPushStatus returns the current
// push state for a (device, push) pair.
// The caller's React Query hook polls
// this every 2s; the SPA never sees a
// "no such push" 404 — the server maps
// which the helper flattens to "expired".
export async function pollPushStatus(deviceName: string, pushId: string): Promise<PushStatus> {
  const request: GetPushStatusRequest = create(GetPushStatusRequestSchema, {
    device: deviceName,
    push: pushId,
  });
  const response: PushStatusProto = await sketchServiceClient.getPushStatus(request);
  return {
    state: pushStatusStateFromProto(response.state),
    pushId: response.pushId,
    failureReason: response.failureReason,
  };
}
