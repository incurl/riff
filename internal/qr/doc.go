// Package qr generates QR code PNG images for the PO-33
// device binding flow. The pairing URL the SPA encodes is
// the only payload that ever lives in a QR code; we do
// not reuse this package for any other purpose.
//
// The implementation is a thin wrapper around
// github.com/skip2/go-qrcode, which is stdlib-only at
// runtime. The wrapper exists to:
//
//   - keep the device-flow dependency out of the
//     rest of riff (only pb/api imports it);
//   - pin a single error code (PNG generation failure)
//     so the caller can return a precise HTTP status
//     without inspecting the underlying library's
//     error string.
package qr
