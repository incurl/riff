// routes.go mounts the Bark endpoints and implements the handlers.
//
// The response envelope is upstream's CommonResp verbatim:
//
//	{"code": 200, "message": "success", "data": {...}, "timestamp": 1234}
//
// A Bark client reads `code` and `message`; the HTTP status is the same
// value, so a client that only looks at the status sees the same result.
package bark

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// CommonResp is the Bark response envelope.
//
// It is upstream bark-server's router.CommonResp verbatim.
type CommonResp struct {
	Code      int         `json:"code"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

// success returns the envelope for a successful request.
func success() CommonResp {
	return CommonResp{
		Code:      http.StatusOK,
		Message:   "success",
		Timestamp: time.Now().Unix(),
	}
}

// failed returns the envelope for a failed request.
func failed(code int, message string, args ...interface{}) CommonResp {
	return CommonResp{
		Code:      code,
		Message:   fmt.Sprintf(message, args...),
		Timestamp: time.Now().Unix(),
	}
}

// data returns the envelope for a successful request with a payload.
func data(payload interface{}) CommonResp {
	return CommonResp{
		Code:      http.StatusOK,
		Message:   "success",
		Timestamp: time.Now().Unix(),
		Data:      payload,
	}
}

// mount registers every Bark route on the router.
//
// The route table is upstream's, with one structural change: the method
// -less upstream registrations become explicit method lists. Go's
// http.ServeMux (which PocketBase's router is built on) refuses to
// register a method-less pattern that overlaps a method-specific one, and
// upstream's "/mcp" would collide with the "/{device_key}" V1 routes:
//
//	pattern "/bark/mcp" conflicts with pattern "GET /bark/{device_key}"
//
// Registering the MCP routes as GET+POST+DELETE resolves it.
func (s *server) mount(r *router.Router[*core.RequestEvent]) {
	p := s.prefix

	// --- V2 API -------------------------------------------------------
	r.POST(p+"/push", s.pushV2Handler)

	// --- V1 compatibility routes --------------------------------------
	r.GET(p+"/{device_key}", s.pushV1Handler)
	r.POST(p+"/{device_key}", s.pushV1Handler)
	r.GET(p+"/{device_key}/{body}", s.pushV1Handler)
	r.POST(p+"/{device_key}/{body}", s.pushV1Handler)
	r.GET(p+"/{device_key}/{title}/{body}", s.pushV1Handler)
	r.POST(p+"/{device_key}/{title}/{body}", s.pushV1Handler)
	r.GET(p+"/{device_key}/{title}/{subtitle}/{body}", s.pushV1Handler)
	r.POST(p+"/{device_key}/{title}/{subtitle}/{body}", s.pushV1Handler)

	// --- registration -------------------------------------------------
	r.POST(p+"/register", s.registerHandler)
	r.GET(p+"/register", s.registerCompatHandler)
	r.GET(p+"/register/{device_key}", s.registerCheckHandler)

	// --- misc ---------------------------------------------------------
	r.GET(p+"/ping", s.pingHandler)
	r.GET(p+"/healthz", s.healthzHandler)
	r.GET(p+"/info", s.infoHandler)

	// The root of the Bark surface. The "{$}" pattern matches the
	// directory itself and nothing below it; a trailing slash would
	// instead be a subtree pattern that shadows the V1 routes.
	r.GET(p+"/{$}", s.rootHandler)

	// --- MCP ----------------------------------------------------------
	s.mountMCP(r)
}

// pushV1Handler serves the V1 push routes.
//
// Upstream picks the V1 or the V2 parser from the content type, so the
// same handler serves both; the V2 route (/bark/push) always uses the
// JSON parser.
func (s *server) pushV1Handler(e *core.RequestEvent) error {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(e.Request.Header.Get("Content-Type"), ";")[0]))
	if strings.HasPrefix(contentType, "application/json") {
		return s.pushV2Handler(e)
	}
	return s.pushV1(e)
}

// pushV2Handler serves POST /bark/push.
func (s *server) pushV2Handler(e *core.RequestEvent) error {
	params, err := parseV2Params(e.Request, extractPathParams(e.Request))
	if err != nil {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "%s", err.Error()))
	}

	deviceKeys, err := splitDeviceKeys(params)
	if err != nil {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "%s", err.Error()))
	}

	if len(deviceKeys) == 0 {
		return s.pushOne(e, params)
	}

	// Batch push: one delivery per device key, in parallel.
	return s.pushBatch(e, params, deviceKeys)
}

// pushV1 serves the V1 push routes.
func (s *server) pushV1(e *core.RequestEvent) error {
	params, err := parseV1Params(e.Request, extractPathParams(e.Request))
	if err != nil {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "%s", err.Error()))
	}
	return s.pushOne(e, params)
}

// pushOne delivers a single push.
func (s *server) pushOne(e *core.RequestEvent, params pushParams) error {
	if !s.allowed(e, params["device_key"]) {
		return e.JSON(http.StatusTooManyRequests, failed(http.StatusTooManyRequests, "too many requests"))
	}
	code, err := s.pushService().push(params)
	if err != nil {
		return e.JSON(code, failed(code, "%s", err.Error()))
	}
	return e.JSON(http.StatusOK, success())
}

// pushBatch delivers one push per device key.
//
// The result is a list with one entry per requested key, in the order the
// keys were given, each carrying its own code and message. This is
// upstream's batch response shape.
func (s *server) pushBatch(e *core.RequestEvent, params pushParams, deviceKeys []string) error {
	if !s.allowed(e, deviceKeys[0]) {
		return e.JSON(http.StatusTooManyRequests, failed(http.StatusTooManyRequests, "too many requests"))
	}

	svc := s.pushService()
	results := make([]map[string]interface{}, len(deviceKeys))

	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range deviceKeys {
		wg.Go(func() {
			// Copy the params so the concurrent deliveries do
			// not share the map.
			newParams := make(pushParams, len(params)+1)
			for k, v := range params {
				newParams[k] = v
			}
			newParams["device_key"] = deviceKeys[i]

			code, err := svc.push(newParams)

			mu.Lock()
			results[i] = map[string]interface{}{
				"code":       code,
				"device_key": deviceKeys[i],
			}
			if err != nil {
				results[i]["message"] = err.Error()
			}
			mu.Unlock()
		})
	}
	wg.Wait()

	return e.JSON(http.StatusOK, data(results))
}

// allowed consults the optional rate-limit hook.
func (s *server) allowed(e *core.RequestEvent, deviceKey any) bool {
	if s.opts.Allow == nil {
		return true
	}
	key, ok := deviceKey.(string)
	if !ok {
		key = ""
	}
	return s.opts.Allow(key, e.RealIP())
}

// pushService returns the push pipeline.
func (s *server) pushService() *pushService {
	return newPushService(s.store, s.pusher)
}

// deviceInfo is the registration request body.
//
// The field set and the "key"/"devicetoken" aliases are upstream's
// DeviceInfo verbatim, so both the V2 and the V1 registration shapes are
// accepted.
type deviceInfo struct {
	DeviceKey   string `json:"device_key,omitempty" form:"device_key,omitempty" query:"device_key,omitempty"`
	DeviceToken string `json:"device_token,omitempty" form:"device_token,omitempty" query:"device_token,omitempty"`

	// compatible with old req
	OldDeviceKey   string `json:"key,omitempty" form:"key,omitempty" query:"key,omitempty"`
	OldDeviceToken string `json:"devicetoken,omitempty" form:"devicetoken,omitempty" query:"devicetoken,omitempty"`
}

// registerHandler serves POST /bark/register.
func (s *server) registerHandler(e *core.RequestEvent) error {
	var info deviceInfo
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(e.Request.Header.Get("Content-Type"), ";")[0]))
	switch contentType {
	case "application/json":
		if err := decodeJSONBody(e, &info); err != nil {
			return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "request bind failed: %s", err.Error()))
		}
	default:
		if err := e.Request.ParseForm(); err != nil {
			return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "request bind failed: %s", err.Error()))
		}
		bindDeviceInfo(&info, e.Request.Form)
	}
	return s.register(e, info)
}

// registerCompatHandler serves GET /bark/register (the V1 shape, where
// the parameters arrive in the query string).
func (s *server) registerCompatHandler(e *core.RequestEvent) error {
	if err := e.Request.ParseForm(); err != nil {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "request bind failed: %s", err.Error()))
	}
	var info deviceInfo
	bindDeviceInfo(&info, e.Request.Form)
	return s.register(e, info)
}

// bindDeviceInfo reads the registration fields out of a form or query.
func bindDeviceInfo(info *deviceInfo, values map[string][]string) {
	get := func(name string) string {
		if v, ok := values[name]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	info.DeviceKey = get("device_key")
	info.DeviceToken = get("device_token")
	info.OldDeviceKey = get("key")
	info.OldDeviceToken = get("devicetoken")
}

// register stores the registration and answers with the device key.
//
// The response carries both the current ("device_key") and the legacy
// ("key") spelling, exactly as upstream does.
func (s *server) register(e *core.RequestEvent, info deviceInfo) error {
	if info.DeviceKey == "" && info.OldDeviceKey != "" {
		info.DeviceKey = info.OldDeviceKey
	}

	if info.DeviceToken == "" {
		if info.OldDeviceToken != "" {
			info.DeviceToken = info.OldDeviceToken
		} else {
			return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "device token is empty"))
		}
	}

	// DeviceToken length is variable, but should not be too long.
	if len(info.DeviceToken) > 160 {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "device token is invalid"))
	}

	// If the device key is empty, a new one is generated; otherwise it
	// is the caller's own key.
	newKey, err := s.store.SaveDeviceTokenByKey(info.DeviceKey, info.DeviceToken)
	if err != nil {
		return e.JSON(http.StatusInternalServerError, failed(http.StatusInternalServerError, "device registration failed: %s", err.Error()))
	}
	info.DeviceKey = newKey

	return e.JSON(http.StatusOK, data(map[string]string{
		// compatible with old resp
		"key":          info.DeviceKey,
		"device_key":   info.DeviceKey,
		"device_token": info.DeviceToken,
	}))
}

// registerCheckHandler serves GET /bark/register/{device_key}: it
// reports whether the device is registered.
func (s *server) registerCheckHandler(e *core.RequestEvent) error {
	deviceKey := e.Request.PathValue("device_key")
	if deviceKey == "" {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "device key is empty"))
	}
	if _, err := s.store.DeviceTokenByKey(deviceKey); err != nil {
		return e.JSON(http.StatusBadRequest, failed(http.StatusBadRequest, "%s", err.Error()))
	}
	return e.JSON(http.StatusOK, success())
}

// pingHandler serves GET /bark/ping.
func (*server) pingHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, CommonResp{
		Code:      http.StatusOK,
		Message:   "pong",
		Timestamp: time.Now().Unix(),
	})
}

// healthzHandler serves GET /bark/healthz.
func (*server) healthzHandler(e *core.RequestEvent) error {
	return e.String(http.StatusOK, "ok")
}

// rootHandler serves GET /bark/.
func (*server) rootHandler(e *core.RequestEvent) error {
	return e.String(http.StatusOK, "ok")
}

// decodeJSONBody decodes the request body as JSON.
//
// An empty body is not an error: it yields the zero value, which the
// caller then validates (a registration without a device token is
// answered 400).
func decodeJSONBody(e *core.RequestEvent, target any) error {
	if e.Request.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(e.Request.Body)
	if err := decoder.Decode(target); err != nil {
		if err.Error() == "EOF" {
			return nil
		}
		return err
	}
	return nil
}

// infoHandler serves GET /bark/info.
func (s *server) infoHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, s.buildInfo())
}
