package suspend

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// suspendHandler serves POST /api/users/{id}/suspend.
//
// The body carries `suspendedUntil` (an RFC 3339 timestamp, or empty for an
// indefinite suspension), `suspendReason` and `suspendMessage`.
func suspendHandler(e *core.RequestEvent) error {
	return setSuspendHandler(e, true)
}

// unsuspendHandler serves DELETE /api/users/{id}/suspend.
func unsuspendHandler(e *core.RequestEvent) error {
	return setSuspendHandler(e, false)
}

// setSuspendHandler suspends or reinstates an account.
func setSuspendHandler(e *core.RequestEvent, suspend bool) error {
	actor, err := api.RequireAuth(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnauthorized, api.CodeNotAuthenticated, "You must be signed in to do that.")
	}
	if err := api.VerifyCSRF(e); err != nil {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "The request could not be verified.")
	}

	userID := e.Request.PathValue("id")
	if userID == "" {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "An account is required.")
	}

	allowed, err := api.Can(e, domain.PermissionSuspendUser, api.TypeUser, userID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The account could not be suspended.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to suspend that account.")
	}

	// Upstream refuses a self-suspension and an administrator, because a
	// suspension is a moderation tool and neither target is moderatable.
	if userID == actor.User.ID {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You cannot suspend yourself.")
	}

	collection, err := e.App.FindCollectionByNameOrId(repository.SharedUsersCollection)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The account could not be suspended.")
	}

	record, err := e.App.FindRecordById(collection, userID)
	if err != nil {
		return api.WriteError(e, http.StatusNotFound, api.CodeNotFound, "That account does not exist.")
	}

	server := api.ServerFrom(e)
	if server != nil && server.IsAdministrator(userID) {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You cannot suspend an administrator.")
	}

	if !suspend {
		// Reinstating clears the note and the message as well as the
		// date: leaving them behind would show a stale suspension to the
		// account and to the moderators.
		record.Set(suspendedUntilField, nil)
		record.Set(reasonField, "")
		record.Set(messageField, "")

		if err := e.App.Save(record); err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The account could not be reinstated.")
		}

		return api.WriteDocument(e, http.StatusOK, api.Document{
			Data: suspendedUserDocument(userID, time.Time{}, "", ""),
		})
	}

	body, err := decodeSuspendBody(e)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The suspension could not be read.")
	}

	until, err := parseSuspendedUntil(body.until)
	if err != nil {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "The suspension date could not be read.")
	}

	record.Set(suspendedUntilField, until)
	record.Set(reasonField, body.reason)
	record.Set(messageField, body.message)

	if err := e.App.Save(record); err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The account could not be suspended.")
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data: suspendedUserDocument(userID, until, body.reason, body.message),
	})
}

// suspendBody is the accepted request shape.
type suspendBody struct {
	until   string
	reason  string
	message string
}

// decodeSuspendBody reads the body, accepting both the JSON:API shape
// (`{"data":{"attributes":{...}}}`) and the flat one, because the forum's
// own front end posts the flat form and an upstream client posts the other.
func decodeSuspendBody(e *core.RequestEvent) (suspendBody, error) {
	raw, err := decodeAnyObject(e)
	if err != nil {
		return suspendBody{}, err
	}

	attributes := raw
	if data, ok := raw["data"].(map[string]any); ok {
		if nested, ok := data["attributes"].(map[string]any); ok {
			attributes = nested
		}
	}

	return suspendBody{
		until:   stringField(attributes, "suspendedUntil"),
		reason:  stringField(attributes, "suspendReason"),
		message: stringField(attributes, "suspendMessage"),
	}, nil
}

// decodeAnyObject decodes the request body into a map.
//
// It reads the body directly rather than through the framework's
// `BindBody`: that helper targets a struct, and this body's shape is
// deliberately loose (the JSON:API envelope or the flat form).
func decodeAnyObject(e *core.RequestEvent) (map[string]any, error) {
	body := map[string]any{}

	decoder := json.NewDecoder(e.Request.Body)
	if err := decoder.Decode(&body); err != nil {
		return nil, errors.Wrap(err, "suspend: the body could not be read")
	}

	return body, nil
}

// stringField reads a trimmed string field.
func stringField(body map[string]any, name string) string {
	value, ok := body[name].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// parseSuspendedUntil reads the suspension date.
//
// An empty value means an INDEFINITE suspension, which upstream models as a
// date far in the future rather than as a null, so the "is suspended" test
// is the same for both. The port keeps the null in the column and derives
// the far-future date in the resource, so an operator can tell the two
// apart in the database.
func parseSuspendedUntil(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, errors.Wrap(err, "suspend: invalid suspension date")
	}

	return parsed.UTC(), nil
}

// suspendedUserDocument builds the response document.
func suspendedUserDocument(userID string, until time.Time, reason, message string) map[string]any {
	attributes := map[string]any{
		"canSuspend":     true,
		"suspendedUntil": timestampOrNil(until),
	}

	if reason != "" {
		attributes["suspendReason"] = reason
	}
	if message != "" {
		attributes["suspendMessage"] = message
	}

	return map[string]any{
		"type":       api.TypeUser,
		"id":         userID,
		"attributes": attributes,
	}
}

// timestampOrNil renders a date as RFC 3339, or null when it is unset.
func timestampOrNil(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}
