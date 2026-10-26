// push.go is an adaptation of finb/bark-server's route_push.go
// (https://github.com/Finb/bark-server), which is MIT licensed:
//
//	Copyright (c) 2018 Feng
//
// See the LICENSE file in that repository for the full text.
//
// The push pipeline is upstream's verbatim:
//
//   - V1 (form/query/path) and V2 (JSON) requests are normalised into one
//     flat parameter map, with the path winning over the query string,
//     which wins over the form body.
//   - push() turns that map into a PushMessage, looks the device token up,
//     and hands it to APNs.
//   - A "delete" parameter turns the push into a silent one.
//
// The differences are the two the port requires:
//
//   - The parameter map is built by the standard library rather than
//     Fiber's BodyParser, so the V1 form/query decoding and the V2 JSON
//     decoding are done explicitly.
//   - The APNs-invalid path clears the stored token instead of writing an
//     empty one, which is the same thing but does not resurrect a
//     deleted row.
package bark

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	pkgerrors "github.com/pkg/errors"
)

// pushParams holds the normalised request parameters.
//
// The map is deliberately untyped (any) because the V2 JSON body may
// carry numbers and booleans as well as strings, and upstream copies
// every unrecognised value into the APNs payload as a string.
type pushParams map[string]any

// pushService is the push pipeline: it resolves a device key to an APNs
// token and delivers the message.
type pushService struct {
	store  DeviceStore
	pusher Pusher
}

// newPushService wires a push service.
func newPushService(store DeviceStore, pusher Pusher) *pushService {
	return &pushService{store: store, pusher: pusher}
}

// push delivers one message and returns the HTTP status code the Bark
// API should answer with.
//
// The status codes match upstream's push() exactly:
//
//	400 - the device key is missing, or the device is not registered
//	500 - the push itself failed
//	200 - delivered
func (s *pushService) push(params pushParams) (int, error) {
	// default value
	msg := PushMessage{
		Body:      "",
		Sound:     "1107",
		ExtParams: make(map[string]any),
	}

	for key, val := range params {
		switch val := val.(type) {
		case string:
			switch strings.ToLower(key) {
			case "id":
				msg.Id = val
				msg.ExtParams["id"] = val
			case "device_key":
				msg.DeviceKey = val
			case "subtitle":
				msg.Subtitle = val
			case "title":
				msg.Title = val
			case "body":
				msg.Body = val
			case "sound":
				// Compatible with old parameters
				if strings.HasSuffix(val, ".caf") {
					msg.Sound = val
				} else {
					msg.Sound = val + ".caf"
				}
			default:
				msg.ExtParams[strings.ToLower(key)] = val
			}
		case map[string]any:
			for k, v := range val {
				msg.ExtParams[k] = v
			}
		default:
			msg.ExtParams[key] = val
		}
	}

	if msg.DeviceKey == "" {
		return http.StatusBadRequest, pkgerrors.New("device key is empty")
	}

	if msg.IsEmptyAlert() {
		// For encrypted push notifications, a Body is required; otherwise, APNs will discard the notification
		msg.Body = "Empty Message"
	}

	deviceToken, err := s.store.DeviceTokenByKey(msg.DeviceKey)
	if err != nil {
		return http.StatusBadRequest, pkgerrors.Wrap(err, "failed to get device token")
	}

	msg.DeviceToken = deviceToken

	code, err := s.pusher.Push(&msg)

	// Invalid token, delete it from database.
	if code == 410 || (code == 400 && err != nil && strings.Contains(err.Error(), "BadDeviceToken")) {
		_, _ = s.store.SaveDeviceTokenByKey(msg.DeviceKey, "")
	}
	if err != nil {
		return http.StatusInternalServerError, pkgerrors.Wrap(err, "push failed")
	}
	return http.StatusOK, nil
}

// parseV1Params collects the V1 parameters from the query string, the
// form body and the URL path.
//
// The precedence is upstream's: the path wins over the query string, which
// wins over the form body. Keys are lower-cased, as upstream does.
func parseV1Params(r *http.Request, pathParams map[string]string) (pushParams, error) {
	params := pushParams{}

	// parse query args (medium priority)
	for key, values := range r.URL.Query() {
		for _, v := range values {
			params[strings.ToLower(key)] = v
		}
	}

	// parse post args
	if err := parseFormParams(r, params); err != nil {
		return nil, err
	}

	// parse url path (highest priority)
	for key, val := range pathParams {
		params[key] = val
	}

	return params, nil
}

// parseV2Params collects the V2 parameters from the JSON body, the query
// string and the URL path.
//
// The precedence is upstream's: the path wins over the query string, which
// wins over the body. Unlike V1, the body may carry non-string values
// (numbers, booleans, arrays), which are preserved so the APNs payload
// can carry them.
func parseV2Params(r *http.Request, pathParams map[string]string) (pushParams, error) {
	params := pushParams{}

	// parse body
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		// An empty body is legitimate (a V2 push may carry every
		// parameter in the query string), so only a malformed body
		// is an error. Upstream tolerates the same case.
		if !errors.Is(err, io.EOF) {
			return nil, pkgerrors.Wrap(err, "request bind failed")
		}
	}

	// parse query args (medium priority)
	for key, values := range r.URL.Query() {
		for _, v := range values {
			params[strings.ToLower(key)] = v
		}
	}

	// parse url path (highest priority)
	for key, val := range pathParams {
		params[key] = val
	}

	return params, nil
}

// parseFormParams merges the form body into params.
//
// Both application/x-www-form-urlencoded and multipart/form-data are
// accepted, matching upstream's Fiber behaviour.
func parseFormParams(r *http.Request, params pushParams) error {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return nil
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case "application/x-www-form-urlencoded":
		if err := r.ParseForm(); err != nil {
			return pkgerrors.Wrap(err, "request bind failed")
		}
		for key, values := range r.PostForm {
			if len(values) > 0 {
				params[strings.ToLower(key)] = values[0]
			}
		}
	case "multipart/form-data":
		// Upstream ignores a multipart parse failure and continues
		// with whatever was parsed.
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil //nolint:nilerr // upstream ignores a multipart parse failure too
		}
		if r.MultipartForm != nil {
			for key, values := range r.MultipartForm.Value {
				if len(values) > 0 {
					params[strings.ToLower(key)] = values[0]
				}
			}
		}
	default:
		// Any other content type carries no form fields; the
		// parameters come from the query string or the path.
	}
	return nil
}

// extractPathParams pulls the Bark path parameters out of the request.
//
// The values are already percent-decoded by net/http's path matching, so
// they are NOT decoded again: a second pass would turn a literal "%20"
// in a notification body into a space. (Upstream's Fiber decodes them
// itself, which is why it calls url.QueryUnescape; the standard library
// does that work already.)
func extractPathParams(r *http.Request) map[string]string {
	params := map[string]string{}
	if v := r.PathValue("device_key"); v != "" {
		params["device_key"] = v
	}
	if v := r.PathValue("subtitle"); v != "" {
		params["subtitle"] = v
	}
	if v := r.PathValue("title"); v != "" {
		params["title"] = v
	}
	if v := r.PathValue("body"); v != "" {
		params["body"] = v
	}
	return params
}

// splitDeviceKeys pulls the device_keys list out of the parameters.
//
// Upstream accepts either a JSON array or a comma-separated string. The
// list is removed from the parameters so it does not leak into the APNs
// payload.
func splitDeviceKeys(params pushParams) ([]string, error) {
	raw, ok := params["device_keys"]
	if !ok {
		return nil, nil
	}
	delete(params, "device_keys")

	switch typed := raw.(type) {
	case string:
		keys := strings.Split(typed, ",")
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			k = strings.TrimSpace(k)
			if k != "" {
				out = append(out, k)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, k := range typed {
			out = append(out, fmt.Sprint(k))
		}
		return out, nil
	case []string:
		return typed, nil
	default:
		return nil, pkgerrors.New("invalid type for device_keys")
	}
}
