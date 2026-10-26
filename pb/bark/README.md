# pb/bark — Bark push notifications for PocketBase

A self-contained port of [finb/bark-server](https://github.com/Finb/bark-server)
(MIT, Copyright (c) 2018 Feng) that mounts the Bark HTTP API on a
PocketBase instance at `/bark`.

The point of the port is that the **device registrations live in the
instance database** instead of a bbolt file, so they survive a restart
and are administered with the rest of the instance.

## Using it

In a PocketBase application:

```go
import "github.com/incurl/riff/pb/bark" // or your own path to the package

func main() {
    app := pocketbase.New()

    if err := bark.Register(app); err != nil {
        log.Fatal(err)
    }

    app.OnServe().BindFunc(func(e *core.ServeEvent) error {
        return e.Next()
    })
    app.Start()
}
```

`Register` is the whole integration: it mounts the routes and ensures the
`bark_devices` collection exists. Use `RegisterWithOptions` to override
the APNs identifiers or to inject a fake store/pusher (which is what the
tests do).

The package imports no riff package, so the directory can be copied into
another PocketBase project as-is.

## The API surface

Upstream's, unchanged, so an existing Bark client can point at
`https://your-instance/bark` and keep working.

| Route | Method | Purpose |
| --- | --- | --- |
| `/bark/push` | POST | V2 push (JSON body); `device_keys` does a batch push |
| `/bark/{device_key}` | GET, POST | V1 compatibility push |
| `/bark/{device_key}/{body}` | GET, POST | V1 compatibility push |
| `/bark/{device_key}/{title}/{body}` | GET, POST | V1 compatibility push |
| `/bark/{device_key}/{title}/{subtitle}/{body}` | GET, POST | V1 compatibility push |
| `/bark/register` | POST | Register a device (JSON body) |
| `/bark/register` | GET | Register a device (query parameters, V1 shape) |
| `/bark/register/{device_key}` | GET | Is this device registered? |
| `/bark/ping` | GET | `{"code":200,"message":"pong",...}` |
| `/bark/healthz` | GET | `ok` (text/plain) |
| `/bark/info` | GET | version, build, arch, commit, device count |
| `/bark/` | GET | `ok` (text/plain) |
| `/bark/mcp` | GET, POST, DELETE | MCP server with the `notify` tool |
| `/bark/mcp/{device_key}` | GET, POST, DELETE | The same, with the device key from the path |

Responses use upstream's envelope: `{"code": N, "message": "...", "data": {...}, "timestamp": N}`.

## Configuration

The embedded APNs key is upstream's, for the official Bark iOS app
(topic `me.fin.bark`). A third-party client needs its own Apple key:

| Env var | Meaning |
| --- | --- |
| `RIFF_BARK_APNS_TOPIC` | The bundle ID the pushes are addressed to |
| `RIFF_BARK_APNS_KEY_ID` | The APNs key identifier |
| `RIFF_BARK_APNS_TEAM_ID` | The APNs team identifier |

The signing key itself is `Options.APNsPrivateKey` (PEM `.p8` content);
the env vars cover the identifiers only, because a key is a secret and
belongs in a file or a secret store, not in an environment variable that
shows up in `ps` output.

## Things that are easy to get wrong

- **The MCP routes must be registered per method.** A method-less
  `/bark/mcp` conflicts with `GET /bark/{device_key}`, and Go's
  `http.ServeMux` *panics* on that — the instance dies at boot.
  `muxconflict_test.go` pins both halves.
- **`/bark/` uses `{$}`, not a trailing slash.** A trailing slash is a
  subtree pattern that would shadow the four-segment V1 route.
- **The device key is always server-generated.** Upstream replaces any
  key the server did not previously issue, so a client cannot pin one.
- **The collection is created in the `OnServe` hook**, not in `Register`:
  the app is not bootstrapped at `Register` time.

See `DECISIONS.md` for the full rationale.
