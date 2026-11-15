package plugin

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// Version is a parsed semantic version.
type Version struct {
	// Major is the major version number.
	Major int
	// Minor is the minor version number.
	Minor int
	// Patch is the patch version number.
	Patch int
	// Pre is the pre-release identifier, without the leading "-".
	Pre string
}

// String renders the version.
func (v Version) String() string {
	base := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		return base + "-" + v.Pre
	}
	return base
}

// ParseVersion parses a semantic version.
//
// A leading "v" is accepted, because a release tag usually carries one.
func ParseVersion(raw string) (Version, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Version{}, errors.New("empty version")
	}
	text = strings.TrimPrefix(text, "v")

	pre := ""
	if idx := strings.IndexAny(text, "-+"); idx >= 0 {
		pre = text[idx+1:]
		text = text[:idx]
	}

	parts := strings.Split(text, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, errors.Errorf("invalid version %q", raw)
	}

	numbers := [3]int{}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return Version{}, errors.Errorf("invalid version %q: %q is not a number", raw, part)
		}
		if value < 0 {
			return Version{}, errors.Errorf("invalid version %q: negative component", raw)
		}
		numbers[i] = value
	}

	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Pre: pre}, nil
}

// Compare orders two versions.
//
// A pre-release version sorts BEFORE the release it precedes, matching
// semver's rule that "1.0.0-alpha" < "1.0.0".
func (v Version) Compare(other Version) int {
	for _, pair := range [][2]int{
		{v.Major, other.Major},
		{v.Minor, other.Minor},
		{v.Patch, other.Patch},
	} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Pre == other.Pre:
		return 0
	case v.Pre == "":
		return 1
	case other.Pre == "":
		return -1
	case v.Pre < other.Pre:
		return -1
	default:
		return 1
	}
}

// SatisfiesRange reports whether a version satisfies a semver range.
//
// The supported range syntax is deliberately small and explicit, because a
// full npm-style range grammar is not what a plugin author needs:
//
//	""      — any version
//	"1.2.3" — exactly that version
//	">=1.2" — at least that version (the omitted patch is 0)
//	">1.2"  — strictly newer than that version
//	"<=1.2" — at most that version
//	"<1.2"  — strictly older than that version
//	"^1.2"  — at least 1.2.0 and below 2.0.0
//	"~1.2"  — at least 1.2.0 and below 1.3.0
//	"1.2 - 1.4" — between the two versions, inclusive
//
// A range may be a single comparator or a space-separated conjunction of
// them. Anything else is an error, so a typo in a manifest is loud rather
// than silently permissive.
func SatisfiesRange(version Version, rangeText string) (bool, error) {
	rangeText = strings.TrimSpace(rangeText)
	if rangeText == "" {
		return true, nil
	}

	// A hyphen range ("1.2 - 1.4") is handled before the comparator split,
	// because it contains a space.
	if strings.Contains(rangeText, " - ") {
		bounds := strings.SplitN(rangeText, " - ", 2)
		low, err := parseBound(bounds[0])
		if err != nil {
			return false, err
		}
		high, err := parseBound(bounds[1])
		if err != nil {
			return false, err
		}
		return version.Compare(low) >= 0 && version.Compare(high) <= 0, nil
	}

	for _, comparator := range strings.Fields(rangeText) {
		ok, err := satisfiesComparator(version, comparator)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// satisfiesComparator evaluates one comparator.
func satisfiesComparator(version Version, comparator string) (bool, error) {
	switch {
	case strings.HasPrefix(comparator, ">="):
		bound, err := parseBound(strings.TrimPrefix(comparator, ">="))
		if err != nil {
			return false, err
		}
		return version.Compare(bound) >= 0, nil
	case strings.HasPrefix(comparator, "<="):
		bound, err := parseBound(strings.TrimPrefix(comparator, "<="))
		if err != nil {
			return false, err
		}
		return version.Compare(bound) <= 0, nil
	case strings.HasPrefix(comparator, ">"):
		bound, err := parseBound(strings.TrimPrefix(comparator, ">"))
		if err != nil {
			return false, err
		}
		return version.Compare(bound) > 0, nil
	case strings.HasPrefix(comparator, "<"):
		bound, err := parseBound(strings.TrimPrefix(comparator, "<"))
		if err != nil {
			return false, err
		}
		return version.Compare(bound) < 0, nil
	case strings.HasPrefix(comparator, "^"):
		bound, err := parseBound(strings.TrimPrefix(comparator, "^"))
		if err != nil {
			return false, err
		}
		upper := Version{Major: bound.Major + 1}
		if bound.Major == 0 {
			upper = Version{Major: 0, Minor: bound.Minor + 1}
		}
		return version.Compare(bound) >= 0 && version.Compare(upper) < 0, nil
	case strings.HasPrefix(comparator, "~"):
		bound, err := parseBound(strings.TrimPrefix(comparator, "~"))
		if err != nil {
			return false, err
		}
		upper := Version{Major: bound.Major, Minor: bound.Minor + 1}
		return version.Compare(bound) >= 0 && version.Compare(upper) < 0, nil
	default:
		bound, err := parseBound(comparator)
		if err != nil {
			return false, err
		}
		return version.Compare(bound) == 0, nil
	}
}

// parseBound parses a version used as a range bound.
//
// A two-component bound ("1.2") is treated as "1.2.0", which is what the
// comparator semantics expect.
func parseBound(raw string) (Version, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Version{}, errors.New("empty version bound")
	}
	parts := strings.Split(strings.TrimPrefix(text, "v"), ".")
	switch len(parts) {
	case 1:
		return ParseVersion(parts[0] + ".0.0")
	case 2:
		return ParseVersion(parts[0] + "." + parts[1] + ".0")
	case 3:
		return ParseVersion(text)
	default:
		return Version{}, errors.Errorf("invalid version bound %q", raw)
	}
}
