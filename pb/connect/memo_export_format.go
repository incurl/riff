// memo_export_format.go owns the Memos export format
// helpers shared between the ExportMemos writer and the
// ImportMemos reader. The format is documented in
// docs/design/memos-export-format.md; the schema files
// under docs/design/memo-export/1.0/ are the normative
// definition. This file implements the parts that are
// orthogonal to either direction: the safe-filename
// mapping, the JSON wire shape, and the JSON encode
// helpers.
//
// JSON wire format. Records follow the proto3 JSON
// mapping: field names are lowerCamelCase, enum values
// are their proto names as strings, and timestamps are
// RFC 3339 date-time strings. protojson is the canonical
// encoder because the design deliberately reuses the
// public API's vocabulary.
package connect

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// memoExportMediaType is the media type for the export
// container. Defined in RFC 6838 §3.2 vendor tree.
const memoExportMediaType = "application/vnd.usememos.export+zip"

// memoExportFormat is the format identifier. The reader
// also accepts the legacy `memos-archive` identifier for
// archives written by older builds.
const memoExportFormat = "memos-export"

// memoExportFormatVersion is the version we write. The
// reader refuses a major version it does not know.
const memoExportFormatVersion = "1.0"

// generatorName is the value written into the manifest's
// `generator.name` field. The version is filled in at
// write time.
const generatorName = "riff"

// reservedWindowsNames is the set of base names the
// safe-filename mapping prefixes with `_` so an
// extraction on a Windows host cannot shadow a device.
var reservedWindowsNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {},
	"COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {},
	"LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// exportManifest is the JSON shape written to
// `manifest.json`.
type exportManifest struct {
	Format        string          `json:"format"`
	FormatVersion string          `json:"formatVersion"`
	Generator     exportGenerator `json:"generator"`
	ExportTime    string          `json:"exportTime"`
	Scope         exportScope     `json:"scope"`
	Counts        exportCounts    `json:"counts"`
	Warnings      []exportIssue   `json:"warnings,omitempty"`

	// ExportTimeProto is the parsed RFC 3339
	// timestamp, set by readManifest.
	ExportTimeProto *timestamppb.Timestamp `json:"-"`

	// ScopeUserID is filled by the import pipeline
	// after the auth context is known; the
	// exporter never sets it.
	ScopeUserID string `json:"-"`
}

type exportGenerator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type exportScope struct {
	Kind string     `json:"kind"`
	User exportUser `json:"user"`
}

type exportUser struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	// UserID is filled by the import pipeline
	// after the auth context is known; the
	// exporter never sets it.
	UserID string `json:"-"`
}

type exportCounts struct {
	Memos       int `json:"memos"`
	Attachments int `json:"attachments"`
}

// exportIssue is a single warning or failure entry. The
// reader collects the same shape and emits one entry per
// problem it had to correct.
type exportIssue struct {
	Memo    string `json:"memo,omitempty"`
	Message string `json:"message"`
}

// exportMemoRecord is the JSON shape of one memo file
// under `memos/<uid>.json`.
type exportMemoRecord struct {
	UID              string             `json:"uid"`
	Creator          string             `json:"creator"`
	CreateTime       string             `json:"createTime"`
	CreateTimeParsed time.Time          `json:"-"`
	UpdateTime       string             `json:"updateTime"`
	State            string             `json:"state"`
	Visibility       string             `json:"visibility"`
	Pinned           bool               `json:"pinned"`
	ContentPath      string             `json:"contentPath"`
	Tags             []string           `json:"tags,omitempty"`
	Location         *exportLocation    `json:"location,omitempty"`
	Space            *exportSpaceRef    `json:"space,omitempty"`
	Parent           string             `json:"parent,omitempty"`
	Relations        []exportRelation   `json:"relations,omitempty"`
	Attachments      []exportAttachment `json:"attachments,omitempty"`
	Reactions        []exportReaction   `json:"reactions,omitempty"`
}

type exportLocation struct {
	Placeholder string  `json:"placeholder,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
}

type exportSpaceRef struct {
	UID   string `json:"uid"`
	Title string `json:"title"`
}

type exportRelation struct {
	Type string `json:"type"`
	Memo string `json:"memo"`
}

type exportAttachment struct {
	UID          string           `json:"uid"`
	Filename     string           `json:"filename"`
	Type         string           `json:"type"`
	Size         int64            `json:"size"`
	SHA256       string           `json:"sha256,omitempty"`
	Path         string           `json:"path,omitempty"`
	ExternalLink string           `json:"externalLink,omitempty"`
	CreateTime   string           `json:"createTime"`
	MediaMeta    *exportMediaMeta `json:"mediaMetadata,omitempty"`
}

type exportMediaMeta struct {
	Width      *int32 `json:"width,omitempty"`
	Height     *int32 `json:"height,omitempty"`
	DurationUs *int64 `json:"durationUs,omitempty"`
}

type exportReaction struct {
	ReactionType string `json:"reactionType"`
	Creator      string `json:"creator"`
	CreateTime   string `json:"createTime"`
}

// safeFilename applies the safe-filename mapping defined
// in the design.
func safeFilename(in string) string {
	if in == "" {
		return "file"
	}
	var b strings.Builder
	for _, r := range in {
		switch r {
		case '/', '\\', ':', 0:
			b.WriteByte('_')
		default:
			if unicode.IsControl(r) {
				b.WriteByte('_')
			} else {
				b.WriteRune(r)
			}
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ".")
	base := out
	if dot := strings.Index(out, "."); dot >= 0 {
		base = out[:dot]
	}
	if _, isReserved := reservedWindowsNames[strings.ToUpper(base)]; isReserved {
		out = "_" + out
	}
	if out == "" {
		return "file"
	}
	return out
}

// rfc3339UTC returns t as an RFC 3339 string with the `Z`
// designator.
func rfc3339UTC(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// encodeManifest marshals m with no HTML escaping.
func encodeManifest(m exportManifest) ([]byte, error) {
	return json.Marshal(m)
}

// encodeMemoRecord marshals r with the same settings.
func encodeMemoRecord(r exportMemoRecord) ([]byte, error) {
	return json.Marshal(r)
}
