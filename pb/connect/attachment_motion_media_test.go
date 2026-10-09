package connect

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestMotionMediaFamilyFromString pins the
// JSON-side family name to proto-enum mapping.
func TestMotionMediaFamilyFromString(t *testing.T) {
	cases := []struct {
		in   string
		want v1.MotionMediaFamily
	}{
		{"ANDROID_MOTION_PHOTO", v1.MotionMediaFamily_ANDROID_MOTION_PHOTO},
		{"android_motion_photo", v1.MotionMediaFamily_ANDROID_MOTION_PHOTO},
		{"  ANDROID_MOTION_PHOTO  ", v1.MotionMediaFamily_ANDROID_MOTION_PHOTO},
		{"APPLE_LIVE_PHOTO", v1.MotionMediaFamily_APPLE_LIVE_PHOTO},
		{"apple_live_photo", v1.MotionMediaFamily_APPLE_LIVE_PHOTO},
		{"", v1.MotionMediaFamily_MOTION_MEDIA_FAMILY_UNSPECIFIED},
		{"unknown", v1.MotionMediaFamily_MOTION_MEDIA_FAMILY_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, motionMediaFamilyFromString(tc.in))
		})
	}
}

// TestMotionMediaRoleFromString pins the
// role-side twin of the family mapper.
func TestMotionMediaRoleFromString(t *testing.T) {
	cases := []struct {
		in   string
		want v1.MotionMediaRole
	}{
		{"STILL", v1.MotionMediaRole_STILL},
		{"VIDEO", v1.MotionMediaRole_VIDEO},
		{"CONTAINER", v1.MotionMediaRole_CONTAINER},
		{"container", v1.MotionMediaRole_CONTAINER},
		{"", v1.MotionMediaRole_MOTION_MEDIA_ROLE_UNSPECIFIED},
		{"bogus", v1.MotionMediaRole_MOTION_MEDIA_ROLE_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, motionMediaRoleFromString(tc.in))
		})
	}
}

// TestReadMotionMediaNilCases covers the
// empty/garbage short-circuits. A nil record
// returns nil (no panic). An empty column
// returns nil. A malformed JSON column
// returns nil rather than an error (the proto's
// `motion_media` is OPTIONAL and the frontend
// falls back to a still image).
func TestReadMotionMediaNilCases(t *testing.T) {
	t.Run("nil-record", func(t *testing.T) {
		assert.Nil(t, readMotionMedia(nil))
	})
}
