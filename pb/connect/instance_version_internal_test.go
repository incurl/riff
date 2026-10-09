package connect

import (
	"testing"

	"github.com/incurl/riff/internal/version"
)

// TestInstanceVersion_FallbackIsNonEmpty pins the fallback behaviour:
// when the build did not embed a version (i.e. `go test` without
// -buildvcs=true), instanceVersion must still return a non-empty
// string so the front-end footer never reads blank.
func TestInstanceVersion_FallbackIsNonEmpty(t *testing.T) {
	if v := version.GetCurrentVersion(); v == "" {
		// We're in a build without embedded vcs metadata;
		// instanceVersion must fall back rather than propagate
		// the empty upstream value.
		got := instanceVersion()
		if got == "" {
			t.Fatalf("instanceVersion() returned empty in fallback path")
		}
		if got != riffVersionFallback {
			t.Fatalf("instanceVersion() = %q; want %q in fallback path", got, riffVersionFallback)
		}
	}
}

// TestInstanceVersion_PassesThroughBuildVersion pins the happy
// path: when internal/version has a non-empty Version (i.e. the
// binary was built with -buildvcs=true and committed today),
// instanceVersion surfaces it unchanged.
func TestInstanceVersion_PassesThroughBuildVersion(t *testing.T) {
	if v := version.GetCurrentVersion(); v != "" {
		got := instanceVersion()
		if got != v {
			t.Fatalf("instanceVersion() = %q; want %q (build Version)", got, v)
		}
	}
}

// TestInstanceVersion_CalVerFormat guards the build-time CalVer
// format when embedded. The upstream validate function rejects
// anything outside `YY.MM[.N][-rc.N]`; instanceVersion should
// never break that contract when it passes through. We trust the
// upstream regex (`internal/version.calendarVersion`) instead
// of re-implementing the format check here.
func TestInstanceVersion_CalVerFormat(t *testing.T) {
	v := instanceVersion()
	if v == riffVersionFallback {
		// In fallback mode there's no embedded value to
		// validate; skip.
		return
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("embedded version %q failed upstream Validate: %v", v, err)
	}
}

// TestInstanceCommit_HidesUnset pins that instanceCommit never
// returns the upstream placeholder `"unknown"`, which would leak
// the unset-build sentinel to the front-end footer.
func TestInstanceCommit_HidesUnset(t *testing.T) {
	got := instanceCommit()
	if got == "unknown" {
		t.Fatalf(`instanceCommit() returned "unknown"; the front-end footer should not see the unset-build sentinel`)
	}
}
