# Mock PO-33 device (`mock-device.mjs`)

A plain-Node (>=18, no extra deps) stand-in
for a real PO-33 K.O! that pulls a sketch
from a riff instance. The script:

1. signs in via the existing
   `POST /api/v1/auth/signin` REST surface
   (the same call the SPA makes on login),
2. registers itself as a bound PO-33 via
   the existing `POST /api/po33/pair/start`
   + `POST /api/po33/pair/finish` flow,
3. queues a sketch for itself via the new
   `POST /api/po33/devices/{id}/push` REST
   surface,
4. polls `GET /api/po33/sketches/pending`
   every 2s, fetches the signed URL via
   `GET /api/po33/sketches/{memo}/download`,
   and acks via
   `POST /api/po33/sketches/pending/ack`.

The script is a smoke harness for the
device-pull transport the
device-binding design doc specifies. A
real device firmware is out of scope for
this repo; the mock is the only harness
that exercises the full server-side
flow without a piece of hardware.

## Usage

```
node web/scripts/mock-device.mjs \
  --instance http://localhost:8090 \
  --username alice --password passw0rd! \
  --sketch-memo <memo-id>
```

The script prints one line per push
poll and exits non-zero on a failure
so a CI smoke can wire it in as a test
target. The `--help` flag prints the
full flag list.

## Why plain Node

The mock is intentionally dependency-free
so the CI can run it from a sidecar
container without a Node 18+ toolchain
beyond the runtime itself. A real
firmware implementation will use the
same wire format the mock drives, so a
correctness check is "the mock succeeds
end-to-end" rather than "the device
emits byte-identical HTTP requests".

## What the mock does NOT cover

- The 16-step pattern decoding the .p33
  blob carries. The mock downloads the
  blob (so the audit row is written) but
  does not play the samples.
- The signed-URL signature validation on
  the device side. The mock uses
  `node:http` which follows the 302
  redirect; the signature is verified
  by the server on the way in.
- The Ed25519 signature on the upload
  route. The mock is a *download* smoke
  harness; the upload flow is exercised
  by `pb/api/po33_device_upload_internal_test.go`.
