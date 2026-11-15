package plugin

import (
	"github.com/pocketbase/pocketbase/core"
)

// RequestHandler serves a plugin route.
//
// It is PocketBase's own handler shape, so a plugin route is a plain
// function over the request event. The router handed to a plugin is
// already scoped to the module's base path.
type RequestHandler = func(e *core.RequestEvent) error
