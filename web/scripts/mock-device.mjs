#!/usr/bin/env node
// mock-device.mjs — a stand-in for a real
// PO-33 K.O! that pulls a sketch from a
// riff instance. The script registers
// itself as a bound device, queues a
// sketch for itself via the SPA-facing
// push flow, then polls the
// /api/po33/sketches/pending route
// every 2s, fetches the signed URL, and
// acks the push.
//
// Usage:
//
//   node web/scripts/mock-device.mjs \
//     --instance http://localhost:8090 \
//     --username alice --password p \
//     --sketch-memo <memo-id>
//
// The script prints one line per push
// poll and exits non-zero on a failure
// so a CI smoke can wire it in as a
// test target. The script is plain
// Node (>=18), no extra deps, so the
// PocketBase test suite can call it
// from a sidecar container.
//
// The script reads --instance
// (defaults to RIFF_INSTANCE_URL or
// http://localhost:8090), --username
// (required), --password (required),
// --sketch-memo (required). It uses
// --device-label (defaults to
// "Mock PO-33") for the device's
// human-readable name.
//
// The signing path is a real
// Ed25519 keypair generated with
// crypto.generateKeyPairSync; the
// /api/po33/pair/start → /pair/finish
// handshake writes the public key to
// the po33_devices row. The script
// then computes the X-Device-Fingerprint
// the same way the upload route does
// (sha256 hex of the public key bytes)
// and uses that for the polling +
// download + ack steps.

import { createHash, generateKeyPairSync, randomUUID, sign as signEd25519 } from "node:crypto";
import { argv, exit, stderr, stdout } from "node:process";

const args = parseArgs(argv.slice(2), {
  instance: { type: "string", default: process.env.RIFF_INSTANCE_URL ?? "http://localhost:8090" },
  username: { type: "string", default: "" },
  password: { type: "string", default: "" },
  "sketch-memo": { type: "string", default: "" },
  "device-label": { type: "string", default: "Mock PO-33" },
  help: { type: "boolean", short: "h", default: false },
});

if (args.help) {
  printHelp();
  exit(0);
}

if (!args.username || !args.password || !args["sketch-memo"]) {
  stderr.write("missing required flag(s): --username, --password, --sketch-memo\n");
  printHelp();
  exit(2);
}

const INSTANCE = args.instance.replace(/\/$/, "");
const SKETCH_MEMO = args["sketch-memo"];

const { publicKey, privateKey } = generateKeyPairSync("ed25519");
const publicKeyHex = publicKey.export({ format: "der", type: "spki" }).toString("hex");
const fingerprint = sha256Hex(publicKey.export({ format: "der", type: "spki" }));

async function main() {
  const token = await signIn(args.username, args.password);
  await pairDevice(token, publicKeyHex, args["device-label"]);
  const deviceId = await findDeviceId(token, fingerprint);
  if (!deviceId) {
    throw new Error("device not found after pairing");
  }
  await pushSketch(token, deviceId, SKETCH_MEMO);
  await pollUntilDelivered(fingerprint, 90);
}

main()
  .then(() => {
    stdout.write("delivered\n");
    exit(0);
  })
  .catch((err) => {
    stderr.write(`mock-device: ${err?.message ?? err}\n`);
    exit(1);
  });

async function signIn(username, password) {
  const response = await fetch(`${INSTANCE}/api/v1/auth/signin`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!response.ok) {
    throw new Error(`signin failed: ${response.status}`);
  }
  const body = await response.json();
  if (!body?.token) {
    throw new Error("signin response missing token");
  }
  return body.token;
}

async function pairDevice(token, publicKeyHex, label) {
  const start = await fetch(`${INSTANCE}/api/po33/pair/start`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({ public_key: publicKeyHex }),
  });
  if (!start.ok) {
    throw new Error(`pair/start failed: ${start.status}`);
  }
  const { user_code, authorization_id, expires_at } = await start.json();
  const canonical = canonicalPairFinish({
    authorization_id,
    user_code,
    public_key: publicKeyHex,
  });
  const signature = signEd25519(null, Buffer.from(canonical), privateKey).toString("hex");
  const finish = await fetch(`${INSTANCE}/api/po33/pair/finish?code=${encodeURIComponent(user_code)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      authorization_id,
      user_code,
      public_key: publicKeyHex,
      signature,
    }),
  });
  if (!finish.ok) {
    throw new Error(`pair/finish failed: ${finish.status}`);
  }
  const body = await finish.json();
  return body;
}

async function findDeviceId(token, fingerprint) {
  // ListDevices returns the caller's
  // devices; the new one is at the top
  // because the list is sorted by
  // created-desc.
  const response = await fetch(`${INSTANCE}/memos.api.v1.DeviceService/ListDevices`, {
    method: "POST",
    headers: { "Content-Type": "application/proto", Authorization: `Bearer ${token}` },
    body: encodeConnectRequest("memos.api.v1.DeviceService/ListDevices", {}),
  });
  if (!response.ok) {
    throw new Error(`ListDevices failed: ${response.status}`);
  }
  const text = await response.text();
  const json = decodeConnectResponse(text);
  const devices = json?.devices ?? [];
  for (const d of devices) {
    if (d.publicKeyFingerprint === fingerprint) {
      return d.name?.replace(/^po33-devices\//, "");
    }
  }
  return null;
}

async function pushSketch(token, deviceId, memoId) {
  const response = await fetch(`${INSTANCE}/api/po33/devices/${deviceId}/push`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({ sketch_memo_id: `memos/${memoId}` }),
  });
  if (!response.ok) {
    throw new Error(`push failed: ${response.status}`);
  }
  return await response.json();
}

async function pollUntilDelivered(fingerprint, maxAttempts) {
  for (let i = 0; i < maxAttempts; i++) {
    const pending = await fetch(`${INSTANCE}/api/po33/sketches/pending`, {
      method: "GET",
      headers: { "X-Device-Fingerprint": fingerprint },
    });
    if (!pending.ok) {
      throw new Error(`pending failed: ${pending.status}`);
    }
    const envelope = await pending.json();
    if (envelope.state === "NO_PUSH") {
      stdout.write(`poll ${i + 1}: no push yet\n`);
      await sleep(2000);
      continue;
    }
    if (envelope.state === "FAILED") {
      throw new Error(`push failed: ${envelope.reason ?? "unknown"}`);
    }
    stdout.write(`poll ${i + 1}: state=${envelope.state} push_id=${envelope.push_id}\n`);
    if (envelope.signed_url) {
      // The signed URL is absolute;
      // node:http follows the 302 to the
      // .p33 blob and lands on the file
      // server. We don't read the body;
      // a non-2xx is the failure.
      const download = await fetch(envelope.signed_url, {
        headers: { "X-Device-Fingerprint": fingerprint },
      });
      if (!download.ok) {
        throw new Error(`download failed: ${download.status}`);
      }
      stdout.write(`download ok: ${envelope.memo} (${download.headers.get("content-length") ?? "?"} bytes)\n`);
    }
    const ack = await fetch(`${INSTANCE}/api/po33/sketches/pending/ack`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Device-Fingerprint": fingerprint },
      body: JSON.stringify({ push_id: envelope.push_id }),
    });
    if (!ack.ok) {
      throw new Error(`ack failed: ${ack.status}`);
    }
    stdout.write("ack ok\n");
    return;
  }
  throw new Error("timed out waiting for the device poll to deliver");
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function sha256Hex(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function canonicalPairFinish(payload) {
  // The pairing route signs a
  // canonical JSON serialisation of
  // the metadata; sorted keys, no
  // whitespace, fixed integer
  // rendering. The mock reuses the
  // exact same serialisation the
  // server verifies.
  const ordered = Object.keys(payload).sort();
  return JSON.stringify(ordered.map((k) => [k, payload[k]]));
}

// encodeConnectRequest is a tiny
// connect-over-HTTP/1.1 request
// body encoder (a 5-byte header
// + JSON). The mock only needs the
// ListDevices call (which takes an
// empty request body); the rest of
// the script uses the REST /api/po33/*
// routes. A future mock can move
// the push to the Connect path.
function encodeConnectRequest(procedure, body) {
  // Connect unary body = 5-byte
  // big-endian length prefix + JSON
  // payload. An empty body uses a
  // 0-length prefix.
  const json = JSON.stringify(body);
  const buf = Buffer.alloc(5 + Buffer.byteLength(json));
  buf.writeUInt8(0, 0); // flags
  buf.writeUInt32BE(Buffer.byteLength(json), 1);
  Buffer.from(json).copy(buf, 5);
  return buf;
}

function decodeConnectResponse(text) {
  // Connect's wire format is the
  // same length-prefixed JSON for
  // unary responses; a single
  // envelope is what ListDevices
  // returns. The mock decodes the
  // first envelope; a streaming
  // response would loop.
  const buf = Buffer.from(text, "utf8");
  if (buf.length < 5) {
    return null;
  }
  const flags = buf.readUInt8(0);
  if (flags & 0x01) {
    // trailer-only frame; the error
    // is in the headers. The mock
    // does not parse trailers.
    return null;
  }
  const len = buf.readUInt32BE(1);
  return JSON.parse(buf.subarray(5, 5 + len).toString("utf8"));
}

function parseArgs(argv, schema) {
  const out = {};
  for (const key in schema) {
    if (schema[key].default !== undefined) {
      out[key] = schema[key].default;
    }
  }
  for (let i = 0; i < argv.length; i++) {
    const tok = argv[i];
    if (tok === "--help" || tok === "-h") {
      out.help = true;
      continue;
    }
    if (tok.startsWith("--")) {
      const eq = tok.indexOf("=");
      const key = eq >= 0 ? tok.slice(2, eq) : tok.slice(2);
      if (!(key in schema)) {
        stderr.write(`unknown flag: --${key}\n`);
        exit(2);
      }
      if (schema[key].type === "boolean") {
        out[key] = true;
      } else {
        if (eq >= 0) {
          out[key] = tok.slice(eq + 1);
        } else {
          out[key] = argv[++i];
        }
      }
    }
  }
  return out;
}

function printHelp() {
  stdout.write(`Usage: node web/scripts/mock-device.mjs [flags]

Flags:
  --instance <url>       riff base URL (default: ${process.env.RIFF_INSTANCE_URL ?? "http://localhost:8090"})
  --username <name>      riff username (required)
  --password <pwd>       riff password (required)
  --sketch-memo <id>     the memo id to push (required)
  --device-label <text>  the device's human-readable name (default: "Mock PO-33")
  -h, --help             print this help
`);
}
