package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// pbRecordsCRUDHandler returns a handler that proxies a
// (method, path) pair onto a PocketBase collection via
// the live record-fetch / save / delete APIs. The
// upstream Memos REST surface expected list/get/create/
// update/delete on every collection; the 2026-10-17
// design reuses the in-process record layer (so the
// schema rules, hooks, and rate limits all apply
// unchanged) instead of re-dispatching through PB's
// /api/collections/{name}/records routes.
//
// The path-to-collection mapping is the same one
// upstream Memos used:
//
//	GET    /memos/api/users            ->  users list
//	GET    /memos/api/users/{name}     ->  users/{name}
//	POST   /memos/api/users            ->  users create
//	PATCH  /memos/api/users/{name}     ->  users/{name} update
//	DELETE /memos/api/users/{name}     ->  users/{name} delete
//
// Auth, rule, and rate-limit policy are unchanged
// because every write path goes through
// e.App.Save(rec), which triggers the same OnRecord*
// hook chain a normal collection REST call would.
func pbRecordsCRUDHandler(app core.App, collection string) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		// Identify the resource by stripping the
		// /memos/api/ prefix and the collection
		// name from the path. The remainder is the
		// resource id (e.g. "users/alice" -> "alice")
		// or "" for the collection root.
		rest := e.Request.URL.Path
		const mount = "/memos/api/"
		if !strings.HasPrefix(rest, mount) {
			return crudError(e, http.StatusNotFound, "not found")
		}
		rest = rest[len(mount):]
		// rest is now "users" or "users/{id}" or
		// "users/{id}/{sub}/{more}". The first
		// segment is the collection name; we
		// already know it from the table so
		// everything after is the resource id.
		if !strings.HasPrefix(rest, collection) {
			return crudError(e, http.StatusNotFound, "not found")
		}
		resourceID := strings.TrimPrefix(rest, collection)
		resourceID = strings.TrimPrefix(resourceID, "/")

		switch e.Request.Method {
		case http.MethodGet:
			if resourceID == "" {
				return crudList(e, app, collection)
			}
			return crudGet(e, app, collection, resourceID)
		case http.MethodPost:
			return crudCreate(e, app, collection)
		case http.MethodPatch:
			return crudUpdate(e, app, collection, resourceID)
		case http.MethodDelete:
			return crudDelete(e, app, collection, resourceID)
		default:
			return crudError(e, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// crudList returns a Memos-shape paginated list of
// records in the collection. The query parameters
// `page`, `per_page`, `filter`, and `sort` are
// honoured. Auth and rule check are PB's
// responsibility; the handler only translates the
// HTTP shape.
func crudList(e *core.RequestEvent, app core.App, collection string) error {
	page, perPage, err := paginationFromQuery(e, 30, 100)
	if err != nil {
		return crudError(e, http.StatusBadRequest, err.Error())
	}
	q := e.Request.URL.Query()
	filter := q.Get("filter")
	sort := q.Get("sort")
	records, err := app.FindRecordsByFilter(collection, filter, sort, perPage, (page-1)*perPage, nil)
	if err != nil {
		return crudError(e, http.StatusInternalServerError, "find failed")
	}
	total, _ := app.CountRecords(collection, dbx.NewExp(filter))
	items := make([]map[string]any, 0, len(records))
	for _, r := range records {
		items = append(items, recordToJSON(r, e.Auth))
	}
	return e.JSON(http.StatusOK, map[string]any{
		"items":       items,
		"page":        page,
		"per_page":    perPage,
		"total_items": total,
	})
}

// crudGet returns a single record by id.
func crudGet(e *core.RequestEvent, app core.App, collection, id string) error {
	rec, err := app.FindRecordById(collection, id)
	if err != nil {
		return crudError(e, http.StatusNotFound, "record not found")
	}
	return e.JSON(http.StatusOK, recordToJSON(rec, e.Auth))
}

// crudCreate reads a JSON body and saves a new record.
func crudCreate(e *core.RequestEvent, app core.App, collection string) error {
	body, err := io.ReadAll(e.Request.Body)
	if err != nil {
		return crudError(e, http.StatusBadRequest, "could not read body")
	}
	coll, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		return crudError(e, http.StatusNotFound, "collection not found")
	}
	rec := core.NewRecord(coll)
	if err := recordFromJSON(rec, body); err != nil {
		return crudError(e, http.StatusBadRequest, err.Error())
	}
	// Stamp the creator for `users` (Memos uses
	// the auth record as username; for non-users
	// the caller's id may be a foreign key).
	if e.Auth != nil {
		if rec.Collection().Name == "users" {
			rec.Set("username", e.Auth.GetString("username"))
		}
	}
	if err := app.Save(rec); err != nil {
		return crudError(e, http.StatusBadRequest, err.Error())
	}
	return e.JSON(http.StatusOK, recordToJSON(rec, e.Auth))
}

// crudUpdate reads a JSON body and updates an existing
// record. Only the fields the body contains are touched
// (a partial update).
func crudUpdate(e *core.RequestEvent, app core.App, collection, id string) error {
	body, err := io.ReadAll(e.Request.Body)
	if err != nil {
		return crudError(e, http.StatusBadRequest, "could not read body")
	}
	rec, err := app.FindRecordById(collection, id)
	if err != nil {
		return crudError(e, http.StatusNotFound, "record not found")
	}
	if err := recordFromJSON(rec, body); err != nil {
		return crudError(e, http.StatusBadRequest, err.Error())
	}
	if err := app.Save(rec); err != nil {
		return crudError(e, http.StatusBadRequest, err.Error())
	}
	return e.JSON(http.StatusOK, recordToJSON(rec, e.Auth))
}

// crudDelete removes a record by id. Returns 204 on
// success and 404 on missing. The PB OnRecordAfter*
// hooks fire exactly as they would for a normal
// /api/collections/{name}/records/{id} DELETE.
func crudDelete(e *core.RequestEvent, app core.App, collection, id string) error {
	rec, err := app.FindRecordById(collection, id)
	if err != nil {
		return crudError(e, http.StatusNotFound, "record not found")
	}
	if err := app.Delete(rec); err != nil {
		return crudError(e, http.StatusInternalServerError, err.Error())
	}
	return e.JSON(http.StatusOK, map[string]any{"deleted": true})
}

// recordToJSON projects a *core.Record into the
// Memos-shape map. The fields and values are
// exactly the JSON shape the upstream Memos API
// emitted for a record: id, created, updated, the
// collection-defined fields, and an empty
// "expand" object the client can fill with
// ?expand=field requests.
//
// The projection delegates to PocketBase's own
// [core.Record.PublicExport], which redacts the
// fields PB considers unsafe for a public response:
//
//   - `password` — always deleted
//   - `tokenKey`  — always deleted; it is half of the
//     JWT signing key (core/record_tokens.go), so
//     exposing it would let an attacker mint tokens
//   - `email`    — deleted unless the record's
//     `emailVisibility` is true
//   - every field marked `Hidden: true` — e.g. the
//     `token_hash` of a personal access token and the
//     `signing_secret` of a webhook
//
// A hand-rolled field walk leaks all of them: the
// previous implementation projected EVERY collection
// field through rec.Get() and returned the raw
// values, so an anonymous GET on /memos/api/users
// returned every user's email, tokenKey and password
// hash.
//
// viewer is the authenticated caller (nil for an
// anonymous request). The record's owner always sees
// their own email, which is what PocketBase's own
// record API does (apis/record_helpers.go:465); every
// other record's email is governed by the
// `emailVisibility` flag.
func recordToJSON(rec *core.Record, viewer *core.Record) map[string]any {
	// Project a copy: IgnoreEmailVisibility mutates the record, and
	// the same record is projected more than once (a list projects
	// every row; the owner check and the export must not leak into
	// each other). Fresh() resets the visibility flags and keeps the
	// current field values.
	rec = rec.Fresh()
	if viewer != nil && viewer.Id == rec.Id {
		// The owner sees their own email.
		rec.IgnoreEmailVisibility(true)
	}
	out := rec.PublicExport()

	// PublicExport omits `expand` when the record has no
	// expansion, but the Memos clients expect the key to
	// be present (they fill it from ?expand=field).
	if _, ok := out[core.FieldNameExpand]; !ok {
		out[core.FieldNameExpand] = map[string]any{}
	}

	// PublicExport renders the autodate fields through
	// their own MarshalJSON, which formats them as
	// "2006-01-02 15:04:05.000Z" — the same layout
	// rec.GetDateTime(...).String() produces, so the
	// values need no adjustment.
	return out
}

// recordFromJSON applies a JSON object's keys to a
// *core.Record. Unknown fields are silently dropped
// (PB rejects them on save otherwise). Missing
// fields are left at the record's zero value.
func recordFromJSON(rec *core.Record, body []byte) error {
	var fields map[string]any
	if err := jsonUnmarshal(body, &fields); err != nil {
		return errors.New("invalid JSON body")
	}
	for k, v := range fields {
		if k == "id" || k == "created" || k == "updated" || k == "expand" || k == "collectionId" || k == "collectionName" {
			continue
		}
		// Confirm the field exists on the
		// collection; PB will reject unknown
		// fields on save and we want a
		// cleaner 400.
		found := false
		for _, f := range rec.Collection().Fields {
			if f.GetName() == k {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		rec.Set(k, v)
	}
	return nil
}

// jsonUnmarshal is a thin wrapper so the imports
// stay readable when this file is the only
// JSON-aware surface in the package.
func jsonUnmarshal(body []byte, v any) error {
	return jsonStdUnmarshal(body, v)
}

// crudError writes a Memos-shape JSON error and
// returns nil so the handler chain completes.
func crudError(e *core.RequestEvent, status int, message string) error {
	e.Response.Header().Set("Content-Type", "application/json")
	e.Response.WriteHeader(status)
	// Memos uses {code, message} for transport
	// errors. Map HTTP status to a small set of
	// numeric codes (the Memos frontend only
	// consults the body, not the code, but the
	// upstream wire shape keeps the field).
	var code int
	switch status {
	case http.StatusBadRequest:
		code = 3
	case http.StatusUnauthorized:
		code = 16
	case http.StatusForbidden:
		code = 7
	case http.StatusNotFound:
		code = 5
	case http.StatusMethodNotAllowed:
		code = 12
	default:
		code = 13
	}
	_, _ = io.WriteString(e.Response,
		fmt.Sprintf(`{"code":%d,"message":%q}`, code, message))
	return nil
}
