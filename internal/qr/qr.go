package qr

import (
	"errors"
	"fmt"

	"github.com/skip2/go-qrcode"
)

// ErrEncode is the sentinel returned when the underlying
// QR encoder fails. The handler maps this to HTTP 500.
var ErrEncode = errors.New("qr: encode failed")

// PNG returns a PNG-encoded QR code image at the given
// pixel size. The size is the image's edge length in
// pixels; pass 256 for a 256x256 image. The QR
// error-correction level is fixed at "high" so a slightly
// occluded code on the device's screen still scans
// reliably.
func PNG(payload string, size int) ([]byte, error) {
	if payload == "" {
		return nil, fmt.Errorf("%w: empty payload", ErrEncode)
	}
	if size < 64 {
		return nil, fmt.Errorf("%w: size %d below minimum 64", ErrEncode, size)
	}
	// RecoveryLevelHigh is the 30% recovery level. The
	// device is hand-held; we trade capacity for
	// robustness.
	q, err := qrcode.New(payload, qrcode.RecoveryLevel(qrcode.Highest))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncode, err)
	}
	png := q.Image(size)
	buf, err := pngToBytes(png)
	if err != nil {
		return nil, fmt.Errorf("%w: encode png: %v", ErrEncode, err)
	}
	return buf, nil
}
