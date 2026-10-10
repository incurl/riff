// Package frontend serves the riff SPA (a Vite build of the Memos
// React app under web/) from the root of the PocketBase HTTP
// listener.
//
// The package is intentionally thin. All it does is
//
//   - embed the contents of server/frontend/dist (a build
//     artifact produced by `cd web && pnpm release`) into the
//     riff binary at compile time; and
//   - register a single GET /{path...} catch-all on the
//     PocketBase router at OnServe time, with index.html
//     fallback for the SPA's client-side routes.
//
// For a deeper write-up of the design, see
// server/frontend/README.md. For the build-time prerequisite
// that the dist tree must exist (otherwise `go:embed` fails to
// compile), see the "Build prerequisite" section there.
package frontend
