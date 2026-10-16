package qr

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPNG_HappyPath produces a known string, decodes the
// result back, and asserts the image is a square of the
// requested size. The decoder asserts the PNG signature
// is well-formed, which is the only thing the SPA
// actually cares about (it sets <img src="data:..."/>
// and the browser handles the rest).
func TestPNG_HappyPath(t *testing.T) {
	out, err := PNG("https://riff.example/po33/pair/ABCDE", 256)
	require.NoError(t, err)
	require.NotEmpty(t, out)

	img, format, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "png", format)
	b := img.Bounds()
	assert.Equal(t, 256, b.Dx(), "PNG must be 256x256")
	assert.Equal(t, 256, b.Dy(), "PNG must be 256x256")

	// Sanity check: the underlying go-qrcode
	// implementation produces a non-empty PNG. We do
	// not assert a specific byte stream; we just
	// confirm the bytes are a syntactically valid
	// PNG (the decode above already covers that).
	_, err = png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
}

// TestPNG_RejectsEmptyPayload pins the
// InvalidArgument path: a missing payload returns
// ErrEncode so the handler can map to 400.
func TestPNG_RejectsEmptyPayload(t *testing.T) {
	_, err := PNG("", 256)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEncode)
}

// TestPNG_RejectsSmallSize pins the lower bound on the
// size parameter. Smaller QR images are harder to scan
// reliably, and the device UI assumes a 256-pixel square
// so a smaller image would render the wrong size on the
// SPA.
func TestPNG_RejectsSmallSize(t *testing.T) {
	_, err := PNG("payload", 32)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEncode)
}

// TestPNG_SizeMatchesRequest confirms the pixel size
// the caller passed is the pixel size the encoder
// returned. This is the regression guard: a future
// change to the default recovery level could change the
// rendered size if it is not pinned here.
func TestPNG_SizeMatchesRequest(t *testing.T) {
	for _, size := range []int{128, 256, 512} {
		out, err := PNG("hello", size)
		require.NoError(t, err)
		img, _, err := image.Decode(bytes.NewReader(out))
		require.NoError(t, err)
		b := img.Bounds()
		assert.Equal(t, size, b.Dx(), "size=%d", size)
		assert.Equal(t, size, b.Dy(), "size=%d", size)
	}
}
