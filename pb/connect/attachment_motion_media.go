package connect

import (
	"encoding/json"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// motionMediaJSON is the on-disk shape of the
// `motion_media` column written by the
// `pb/hooks/motion_media.go` after-save hook.
// Keeping it in a local struct (rather than a
// `map[string]any`) lets `encoding/json` fail
// loudly on a malformed row instead of silently
// dropping the data into a generic interface.
type motionMediaJSON struct {
	Family                  string `json:"family"`
	Role                    string `json:"role"`
	GroupID                 string `json:"group_id"`
	PresentationTimestampUs int64  `json:"presentation_timestamp_us"`
	HasEmbeddedVideo        bool   `json:"has_embedded_video"`
}

// readMotionMedia returns the proto `MotionMedia`
// value for an attachment record, or nil if the
// record's `motion_media` column is empty (i.e.
// the upload was not a Motion Photo). The
// converter is shared by `patchToAttachment` and
// `sampleToAttachment` so both code paths return
// the same shape.
//
// An empty or malformed column yields nil rather
// than an error: the proto's `motion_media` is
// OPTIONAL and the frontend falls back to a still
// image when the field is unset. Failing closed
// (i.e. returning a 500) would lock the user out
// of an attachment whose hook produced a bad
// payload during the first deploy, and the
// migration is supposed to be the safer path
// that ignores bad data.
func readMotionMedia(rec *core.Record) *v1.MotionMedia {
	if rec == nil {
		return nil
	}
	raw := rec.GetString("motion_media")
	if raw == "" {
		return nil
	}
	var parsed motionMediaJSON
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	out := &v1.MotionMedia{
		GroupId:                 parsed.GroupID,
		PresentationTimestampUs: parsed.PresentationTimestampUs,
		HasEmbeddedVideo:        parsed.HasEmbeddedVideo,
	}
	out.Family = motionMediaFamilyFromString(parsed.Family)
	out.Role = motionMediaRoleFromString(parsed.Role)
	// Unset enums (the zero value `*_UNSPECIFIED`) are
	// indistinguishable from a missing field on the
	// wire; we treat UNSPECIFIED as a missing payload
	// so the proto's `optional` semantics apply.
	if out.Family == v1.MotionMediaFamily_MOTION_MEDIA_FAMILY_UNSPECIFIED &&
		out.Role == v1.MotionMediaRole_MOTION_MEDIA_ROLE_UNSPECIFIED &&
		!out.HasEmbeddedVideo {
		return nil
	}
	return out
}

// motionMediaFamilyFromString maps the JSON-side
// family name (e.g. "ANDROID_MOTION_PHOTO") to the
// proto enum. Unknown strings map to
// `*_UNSPECIFIED` rather than producing an error,
// matching the lenient readMotionMedia policy.
func motionMediaFamilyFromString(s string) v1.MotionMediaFamily {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "ANDROID_MOTION_PHOTO":
		return v1.MotionMediaFamily_ANDROID_MOTION_PHOTO
	case "APPLE_LIVE_PHOTO":
		return v1.MotionMediaFamily_APPLE_LIVE_PHOTO
	default:
		return v1.MotionMediaFamily_MOTION_MEDIA_FAMILY_UNSPECIFIED
	}
}

// motionMediaRoleFromString is the role-side twin
// of motionMediaFamilyFromString.
func motionMediaRoleFromString(s string) v1.MotionMediaRole {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "STILL":
		return v1.MotionMediaRole_STILL
	case "VIDEO":
		return v1.MotionMediaRole_VIDEO
	case "CONTAINER":
		return v1.MotionMediaRole_CONTAINER
	default:
		return v1.MotionMediaRole_MOTION_MEDIA_ROLE_UNSPECIFIED
	}
}
