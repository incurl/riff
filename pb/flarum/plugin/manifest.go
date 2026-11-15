// Package plugin implements the forum's extension framework: the Extender
// equivalent to Flarum's `extend.php` files.
//
// A plugin declares what it is in a manifest, registers its extenders
// during the Register phase, and does its startup work during the Boot
// phase. Go plugins are compiled into the host binary — there is no
// dynamic .so loading — and register themselves through an init hook or an
// explicit [Manager.Register] call.
package plugin

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
)

// BackendKind selects how a plugin's backend code runs.
type BackendKind string

const (
	// BackendStaticGo is a plugin compiled into the host binary.
	BackendStaticGo BackendKind = "static-go"
	// BackendJSSandbox is a plugin whose backend logic runs as a script in
	// the sandbox. Deferred past v1; the manifest accepts it so a
	// manifest can be written for it, but the manager refuses to load
	// one with an explicit error.
	BackendJSSandbox BackendKind = "js-sandbox"
)

// Assets declares a plugin's frontend bundles.
//
// The bundles are prebuilt files served by the Go backend under the forum
// asset prefix and loaded by the SPA boot sequence in dependency order.
type Assets struct {
	// Forum is the forum-app bundle, relative to the asset root.
	Forum string `json:"forum,omitempty" yaml:"forum,omitempty"`
	// Admin is the admin-app bundle, relative to the asset root.
	Admin string `json:"admin,omitempty" yaml:"admin,omitempty"`
	// CSS is a stylesheet, relative to the asset root.
	CSS string `json:"css,omitempty" yaml:"css,omitempty"`
}

// Icon is a plugin's icon metadata, mirroring Flarum's extension icon.
type Icon struct {
	// BackgroundColor is a CSS color, e.g. "#238c59".
	BackgroundColor string `json:"backgroundColor,omitempty" yaml:"backgroundColor,omitempty"`
	// Glyph is an icon class, e.g. "fas fa-tags".
	Glyph string `json:"glyph,omitempty" yaml:"glyph,omitempty"`
}

// Manifest describes a plugin.
//
// It mirrors the metadata fields of Flarum's composer.json for an
// extension: the id, the human-readable name, the version, the core
// version range it supports, and its dependencies.
type Manifest struct {
	// ID is the plugin's unique identifier, e.g. "acme/bookmarks".
	ID string `json:"id" yaml:"id"`
	// Name is the human-readable name.
	Name string `json:"name" yaml:"name"`
	// Version is the plugin's own version.
	Version string `json:"version" yaml:"version"`
	// CoreRequire is a semver range the running core must satisfy.
	CoreRequire string `json:"coreRequire" yaml:"coreRequire"`
	// Deps lists the plugins this one depends on, each as
	// "id" or "id range".
	Deps []string `json:"deps,omitempty" yaml:"deps,omitempty"`
	// BackendKind selects the backend execution model.
	BackendKind BackendKind `json:"backendKind" yaml:"backendKind"`
	// Assets declares the frontend bundles.
	Assets Assets `json:"assets,omitempty" yaml:"assets,omitempty"`
	// Icon is the plugin's icon metadata.
	Icon Icon `json:"icon,omitempty" yaml:"icon,omitempty"`
}

// Dependency is one parsed entry of a manifest's dependency list.
type Dependency struct {
	// ID is the required plugin's ID.
	ID string
	// Range is the semver range the required plugin's version must
	// satisfy. An empty range means "any version".
	Range string
}

// ParseDeps parses a manifest's dependency list.
//
// Each entry is either "id" or "id range" (a space between them). The
// range is optional.
func ParseDeps(deps []string) ([]Dependency, error) {
	parsed := make([]Dependency, 0, len(deps))
	for _, raw := range deps {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		fields := strings.Fields(entry)
		dep := Dependency{ID: fields[0]}
		if len(fields) > 1 {
			dep.Range = strings.TrimSpace(strings.Join(fields[1:], " "))
		}
		if dep.ID == "" {
			return nil, errors.Errorf("plugin: empty dependency id in %q", raw)
		}
		parsed = append(parsed, dep)
	}
	return parsed, nil
}

// Validate checks a manifest for the problems that would make it
// unloadable.
//
// The messages name the offending field, because the manifest is written
// by a human and a plugin author needs to know which line to fix.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("plugin: manifest is missing an id")
	}
	if !strings.Contains(m.ID, "/") {
		return errors.Errorf("plugin: manifest id %q must be in \"vendor/name\" form", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.Errorf("plugin: manifest %q is missing a name", m.ID)
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.Errorf("plugin: manifest %q is missing a version", m.ID)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return errors.Wrapf(err, "plugin: manifest %q has an invalid version", m.ID)
	}
	if strings.TrimSpace(m.CoreRequire) == "" {
		return errors.Errorf("plugin: manifest %q is missing a coreRequire range", m.ID)
	}
	switch m.BackendKind {
	case "":
		return errors.Errorf("plugin: manifest %q is missing a backendKind", m.ID)
	case BackendStaticGo:
		// The only kind the manager loads in v1.
	case BackendJSSandbox:
		// Accepted as a manifest value, but the manager refuses to load
		// it in v1 (see Manager.resolve). The case is spelled out so a
		// future implementation has a place to hang the loading.
		return nil
	default:
		return errors.Errorf("plugin: manifest %q has an unknown backendKind %q", m.ID, m.BackendKind)
	}
	if _, err := ParseDeps(m.Deps); err != nil {
		return errors.Wrapf(err, "plugin: manifest %q has an invalid dependency list", m.ID)
	}
	return nil
}

// String renders a manifest for logs.
func (m Manifest) String() string {
	return fmt.Sprintf("%s %s", m.ID, m.Version)
}
