package qr

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

// pngToBytes encodes an arbitrary image.Image to PNG
// bytes. The function is split from PNG() so callers
// (and tests) can supply any image.Image without
// touching the go-qrcode dependency surface.
func pngToBytes(img image.Image) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("nil image")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
