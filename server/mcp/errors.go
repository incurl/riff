package mcp

import "github.com/pkg/errors"

// errSpecPathRequired is the error returned by Register when no
// OpenAPI spec path is supplied. It mirrors the check inside
// NewMCPService so callers see the same diagnostic from both
// code paths. The message points operators at the `buf generate`
// step that produces the spec, instead of leaving them to grep
// for the dependency.
var errSpecPathRequired = errors.New("mcp: specPath is required (run `buf generate` to produce proto/gen/openapi.yaml and pass its path)")
