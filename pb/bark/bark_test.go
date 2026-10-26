package bark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory DeviceStore for the tests.
//
// Its SaveDeviceTokenByKey reproduces upstream bark-server's PRODUCTION
// bbolt semantics, not its MemBase test double:
//
//	if key == "" || bucket.Get([]byte(key)) == nil {
//	    key = shortuuid.New()
//	}
//
// That is, a supplied key that is not yet registered is replaced by a
// freshly generated one. Upstream's own MemBase (used by its push_test.go)
// behaves differently — it keeps an unknown key and returns an error —
// but the bbolt store is what a real server runs, so that is the
// behaviour the port has to match.
type fakeStore struct {
	keys    map[string]string
	byKey   map[string]bool
	saved   int
	deletes int
}

func newFakeStore() *fakeStore {
	return &fakeStore{keys: map[string]string{}, byKey: map[string]bool{}}
}

func (f *fakeStore) CountAll() (int, error) { return len(f.keys), nil }

func (f *fakeStore) DeviceTokenByKey(key string) (string, error) {
	token, ok := f.keys[key]
	if !ok {
		return "", errors.Errorf("failed to get [%s] device token from database", key)
	}
	if len(token) == 0 {
		return "", errors.New("device token invalid")
	}
	return token, nil
}

func (f *fakeStore) SaveDeviceTokenByKey(key, token string) (string, error) {
	f.saved++
	if key == "" || !f.byKey[key] {
		key = fmt.Sprintf("generated-key-%d", f.saved)
	}
	f.keys[key] = token
	f.byKey[key] = true
	return key, nil
}

func (f *fakeStore) DeleteDeviceByKey(key string) error {
	f.deletes++
	delete(f.keys, key)
	delete(f.byKey, key)
	return nil
}

// fakePusher records the pushes it receives.
//
// The batch push path delivers to several devices in parallel, so the
// recording is mutex-guarded: without it the race detector reports a
// data race on the slice append.
type fakePusher struct {
	mu       sync.Mutex
	messages []*PushMessage
	code     int
	err      error
}

func (f *fakePusher) Push(msg *PushMessage) (int, error) {
	f.mu.Lock()
	f.messages = append(f.messages, msg)
	f.mu.Unlock()
	if f.code != 0 {
		return f.code, f.err
	}
	return 200, nil
}

// count returns how many pushes were recorded.
func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

// at returns the recorded push at the given index.
func (f *fakePusher) at(i int) *PushMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[i]
}

// eventFactory builds the RequestEvent the router needs.
//
// It mirrors the helper pb/api's tests use: a minimal event with the
// app, the response writer and the request.
func eventFactoryFor(app core.App) router.EventFactoryFunc[*core.RequestEvent] {
	return func(w http.ResponseWriter, r *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{
			App:      app,
			Response: w,
			Request:  r,
		}, nil
	}
}

// newTestServer builds a Bark server backed by the fakes.
func newTestServer(t *testing.T) (*server, *fakeStore, *fakePusher) {
	t.Helper()
	store := newFakeStore()
	pusher := &fakePusher{}
	svc := &server{
		opts:   Options{},
		store:  store,
		pusher: pusher,
		prefix: "/bark",
	}
	return svc, store, pusher
}

// makeRouter mounts the Bark routes on a fresh router.
func makeRouter(t *testing.T, app core.App, svc *server) http.Handler {
	t.Helper()
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	svc.mount(r)
	mux, err := r.BuildMux()
	require.NoError(t, err, "BuildMux should succeed")
	return mux
}

// TestRoutesRegisterWithoutMuxConflict is the regression test for the
// routing conflict that the port has to avoid.
//
// Upstream bark-server registers its MCP endpoint method-less
// (Fiber's router.All). Go's http.ServeMux — which PocketBase's router
// is built on — refuses to register a method-less pattern that overlaps a
// method-specific one, so a verbatim port panics at startup:
//
//	pattern "/bark/mcp" conflicts with pattern "GET /bark/{device_key}"
//
// The same applies to "/bark/mcp/{device_key}" against
// "GET /bark/{device_key}/{body}".
//
// A panic inside BuildMux is not a returned error: it takes the process
// down, so the test has to survive it to be meaningful.
func TestRoutesRegisterWithoutMuxConflict(t *testing.T) {
	app := newTestApp(t)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("mounting the Bark routes panicked: %v", r)
		}
	}()

	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	// The mux must actually serve the routes, not just build.
	for _, path := range []string{
		"/bark/ping",
		"/bark/healthz",
		"/bark/info",
		"/bark/",
		"/bark/mcp",
		"/bark/mcp/somekey",
		"/bark/somekey",
		"/bark/somekey/body",
		"/bark/somekey/title/body",
		"/bark/somekey/title/subtitle/body",
		"/bark/register",
		"/bark/register/somekey",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code,
			"GET %s should be routed, got 404", path)
	}
}

// TestLiteralRoutesBeatWildcardRoutes pins the precedence between the
// literal routes (/bark/ping) and the V1 wildcard route
// (/bark/{device_key}).
//
// A device key that collides with a reserved word would otherwise be
// unreachable: the literal route wins, so "ping" is the health probe and
// never a device.
func TestLiteralRoutesBeatWildcardRoutes(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	cases := []struct {
		path string
		want string
	}{
		{"/bark/ping", "pong"},
		{"/bark/healthz", "ok"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		if c.path == "/bark/ping" {
			// The ping endpoint answers the upstream JSON
			// envelope, not a bare string.
			var envelope struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			assert.Equal(t, 200, envelope.Code)
			assert.Equal(t, c.want, envelope.Message)
			continue
		}
		assert.Equal(t, c.want, rec.Body.String())
	}
}

// TestRegisterCreatesAndReadsBackADevice walks the registration flow.
//
// The supplied key is unknown to the store, so — exactly as upstream's
// bbolt store does — the server mints a new key and returns it. The
// registration is then readable under the returned key.
func TestRegisterCreatesAndReadsBackADevice(t *testing.T) {
	app := newTestApp(t)
	svc, store, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	// Register with an explicit key.
	body := bytes.NewBufferString(`{"device_key":"mykey","device_token":"token123"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/register", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var envelope struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, 200, envelope.Code)

	minted, ok := envelope.Data["device_key"].(string)
	require.True(t, ok, "device_key must be a string")
	require.NotEmpty(t, minted, "the server must return a device key")
	assert.Equal(t, minted, envelope.Data["key"], "the legacy key field must be present")
	assert.Equal(t, "token123", envelope.Data["device_token"])

	// The registration check endpoint must now find it.
	req = httptest.NewRequest(http.MethodGet, "/bark/register/"+minted, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, len(store.keys))
}

// TestRegisterWithoutDeviceTokenIsRejected pins the upstream validation.
func TestRegisterWithoutDeviceTokenIsRejected(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_key":"mykey"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/register", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "device token is empty")
}

// TestRegisterWithOverlongDeviceTokenIsRejected pins the upstream length
// check (upstream rejects a token longer than 160 characters).
func TestRegisterWithOverlongDeviceTokenIsRejected(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	longToken := strings.Repeat("a", 200)
	body := bytes.NewBufferString(`{"device_key":"mykey","device_token":"` + longToken + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/register", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "device token is invalid")
}

// TestRegisterV1CompatUsesTheLegacyFieldNames pins the upstream alias
// handling: the old clients send "key" and "devicetoken".
//
// The key is unknown to the store, so (as upstream's bbolt store does)
// the server mints a new one and returns it in both spellings.
func TestRegisterV1CompatUsesTheLegacyFieldNames(t *testing.T) {
	app := newTestApp(t)
	svc, store, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	req := httptest.NewRequest(http.MethodGet, "/bark/register?key=legacykey&devicetoken=legacytoken", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	minted, ok := envelope.Data["device_key"].(string)
	require.True(t, ok, "device_key must be a string")
	require.NotEmpty(t, minted, "the server must return a device key")

	// The returned key must be the one the token was stored under.
	assert.Equal(t, "legacytoken", store.keys[minted])
	// Both spellings must be present.
	assert.Equal(t, minted, envelope.Data["key"])
}

// TestPushV2DeliversTheMessage walks the V2 push flow.
func TestPushV2DeliversTheMessage(t *testing.T) {
	app := newTestApp(t)
	svc, store, pusher := newTestServer(t)
	store.keys["mykey"] = "apnstoken"
	store.byKey["mykey"] = true
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_key":"mykey","title":"t","body":"b","badge":3}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, pusher.count())
	msg := pusher.at(0)
	assert.Equal(t, "mykey", msg.DeviceKey)
	assert.Equal(t, "apnstoken", msg.DeviceToken)
	assert.Equal(t, "t", msg.Title)
	assert.Equal(t, "b", msg.Body)
	// The default sound is upstream's "1107"; the ".caf" suffix is
	// only appended to a sound the CLIENT supplied.
	assert.Equal(t, "1107", msg.Sound)
	// A non-string extension parameter must survive into the payload.
	assert.Equal(t, float64(3), msg.ExtParams["badge"])
}

// TestPushAppendsTheCafSuffixToACustomSound pins upstream's sound
// handling: a sound name without the ".caf" extension gets one, and a
// name that already has it is left alone.
func TestPushAppendsTheCafSuffixToACustomSound(t *testing.T) {
	app := newTestApp(t)
	svc, store, pusher := newTestServer(t)
	store.keys["mykey"] = "apnstoken"
	store.byKey["mykey"] = true
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_key":"mykey","body":"b","sound":"minuet"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, pusher.count())
	assert.Equal(t, "minuet.caf", pusher.at(0).Sound)

	// A sound that already carries the extension is untouched.
	body = bytes.NewBufferString(`{"device_key":"mykey","body":"b","sound":"minuet.caf"}`)
	req = httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 2, pusher.count())
	assert.Equal(t, "minuet.caf", pusher.at(1).Sound)
}

// TestPushV1FromThePath walks the V1 compat push flow.
func TestPushV1FromThePath(t *testing.T) {
	app := newTestApp(t)
	svc, store, pusher := newTestServer(t)
	store.keys["mykey"] = "apnstoken"
	store.byKey["mykey"] = true
	mux := makeRouter(t, app, svc)

	req := httptest.NewRequest(http.MethodGet, "/bark/mykey/hello", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, pusher.count())
	assert.Equal(t, "mykey", pusher.at(0).DeviceKey)
	assert.Equal(t, "hello", pusher.at(0).Body)
}

// TestPushV1TitleSubtitleBody pins the four-segment V1 route.
func TestPushV1TitleSubtitleBody(t *testing.T) {
	app := newTestApp(t)
	svc, store, pusher := newTestServer(t)
	store.keys["mykey"] = "apnstoken"
	store.byKey["mykey"] = true
	mux := makeRouter(t, app, svc)

	req := httptest.NewRequest(http.MethodGet, "/bark/mykey/title/subtitle/body", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, pusher.count())
	assert.Equal(t, "title", pusher.at(0).Title)
	assert.Equal(t, "subtitle", pusher.at(0).Subtitle)
	assert.Equal(t, "body", pusher.at(0).Body)
}

// TestPushToAnUnknownDeviceIsRejected pins the upstream 400.
func TestPushToAnUnknownDeviceIsRejected(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_key":"nosuchkey","body":"b"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to get device token")
}

// TestBatchPushDeliversToEveryDevice pins the batch response shape.
func TestBatchPushDeliversToEveryDevice(t *testing.T) {
	app := newTestApp(t)
	svc, store, pusher := newTestServer(t)
	store.keys["k1"] = "t1"
	store.byKey["k1"] = true
	store.keys["k2"] = "t2"
	store.byKey["k2"] = true
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_keys":["k1","k2"],"body":"b"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 2, pusher.count())

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 2, len(envelope.Data))
	// The results must keep the order of the requested keys.
	assert.Equal(t, "k1", envelope.Data[0]["device_key"])
	assert.Equal(t, "k2", envelope.Data[1]["device_key"])
}

// TestBatchPushAcceptsACommaSeparatedString pins upstream's tolerance of
// the device_keys string form.
func TestBatchPushAcceptsACommaSeparatedString(t *testing.T) {
	app := newTestApp(t)
	svc, store, pusher := newTestServer(t)
	store.keys["k1"] = "t1"
	store.byKey["k1"] = true
	store.keys["k2"] = "t2"
	store.byKey["k2"] = true
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_keys":"k1,k2","body":"b"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 2, pusher.count())
}

// TestPushToAnInvalidDeviceClearsTheToken pins the upstream behaviour of
// invalidating a device whose APNs token was rejected.
func TestPushToAnInvalidDeviceClearsTheToken(t *testing.T) {
	app := newTestApp(t)
	svc, store, _ := newTestServer(t)
	store.keys["mykey"] = "apnstoken"
	store.byKey["mykey"] = true
	svc.pusher = &fakePusher{code: 410, err: errors.New("Unregistered")}
	mux := makeRouter(t, app, svc)

	body := bytes.NewBufferString(`{"device_key":"mykey","body":"b"}`)
	req := httptest.NewRequest(http.MethodPost, "/bark/push", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	// The token must have been cleared, marking the device invalid.
	assert.Equal(t, "", store.keys["mykey"])
}

// TestHealthzAndRootReturnPlainText pins the upstream plain-text
// responses.
func TestHealthzAndRootReturnPlainText(t *testing.T) {
	app := newTestApp(t)
	svc, _, _ := newTestServer(t)
	mux := makeRouter(t, app, svc)

	for _, path := range []string{"/bark/healthz", "/bark/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "ok", rec.Body.String())
	}
}

// TestInfoReportsTheDeviceCount pins the /bark/info payload.
func TestInfoReportsTheDeviceCount(t *testing.T) {
	app := newTestApp(t)
	svc, store, _ := newTestServer(t)
	store.keys["k1"] = "t1"
	mux := makeRouter(t, app, svc)

	req := httptest.NewRequest(http.MethodGet, "/bark/info", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, float64(1), payload["devices"])
	assert.Contains(t, payload, "version")
	assert.Contains(t, payload, "arch")
}
