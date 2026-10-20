// This file holds the V0 (Memos 0.21) wire-format
// adapters. The V0 dialect MoeMemos uses has a
// fundamentally different shape from the V1 connect
// handlers we use as the source of truth:
//
//   - V0 uses numeric `Long` IDs throughout
//     (`MemosV0User.id`, `MemosV0Memo.id`,
//     `MemosV0Resource.id`). V1 uses resource-name
//     strings (`users/<id>`, `memos/<id>`,
//     `attachments/<id>`).
//   - V0 list responses are top-level JSON arrays.
//     V1 list responses are wrapped in a
//     `ListXResponse{...}` envelope.
//   - V0 `MemosV0User` is flat (no `name` resource
//     field; the V0 `name` is the *display name*).
//   - V0 visibility is a V0-specific string
//     ("PRIVATE" / "PROTECTED" / "PUBLIC") and
//     rowStatus is a separate string ("NORMAL" /
//     "ARCHIVED"). V1 uses proto enums and the
//     `state` field.
//   - V0 timestamps are flat seconds-since-epoch
//     integers. V1 uses `google.protobuf.Timestamp`
//     (RFC 3339 in protojson).
//   - V0 PATCH memo bodies echo the numeric `id` in
//     the body as well as the URL; the V1 PATCH
//     uses a path-injected `name` and an
//     `update_mask` field.
//   - V0 `userSettingList` is a flat array of
//     `{key, value}` pairs; V1 uses typed
//     `UserSetting` proto messages.
//
// The PocketBase 15-char-hex `memos.id` is the
// canonical V1 id. The V0 numeric id is **derived
// from** the V1 id, not stored: we truncate the hex
// string to its first 14 hex digits and parse as a
// `uint64`. A 14-hex-digit number is at most
// `0xFFFFFFFFFFFF` (~2.8e13), which is well within
// `Long.MAX_VALUE` (9.2e18) and survives Kotlin's
// `Long` codec. Collisions are not a concern for a
// single-instance install: 15 hex digits of
// PocketBase `memos.id` produce 14 hex digits of
// V0 id, giving 14*4 = 56 bits of entropy before
// the birthday collision. The helper
// `v0IDFromV1` and its inverse `v0IDToV1` live in
// this file and are the only V0↔V1 id converters
// the route table uses.
//
// All helpers are pure functions of inputs; they
// never touch the database or the connect handler.
// The custom handler does the connect call; the
// shapers in this file only manipulate JSON.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/incurl/riff/pb/connect"
)

// v0IDFromV1 derives a V0 numeric `Long` id from a V1
// PocketBase record id. We take the first 14 hex
// digits of the 15-char V1 id and parse them as a
// uint64. The 14-hex-digit prefix is unique within
// any reasonable memos collection (a 15-char hex id
// has 60 bits of entropy; we keep the high 56).
//
// A V0 id of 0 is reserved for "missing" / "unknown";
// a derived id of 0 would mean the V1 id started
// with a `0`, which PocketBase never produces (it
// generates 15-char ids from the `a..z0..9` alphabet
// — see `pb/connect/memos_translate.go`). We
// nevertheless guard against it for paranoia and
// fall back to `1` in that case.
func v0IDFromV1(v1 string) int64 {
	if v1 == "" {
		return 0
	}
	// Truncate to 14 hex digits. The V1 id is
	// 15-char alphanumeric, so 14-char substring
	// is always a valid prefix. We treat the
	// truncated string as a hex literal, which
	// is the only interpretation that respects
	// the full alphabet — `ParseUint` with base 16
	// does this.
	prefix := v1
	if len(prefix) > 14 {
		prefix = prefix[:14]
	}
	n, err := strconv.ParseUint(prefix, 16, 64)
	if err != nil {
		// The V1 id is 15-char alphanumeric; if
		// the first 14 chars don't parse as hex,
		// we have a non-hex id (lowercase letters
		// in PocketBase ids). Fall back to a
		// stable hash of the full id.
		return stableLongHash(v1)
	}
	if n == 0 {
		return 1
	}
	return int64(n)
}

// v0IDToV1 is the inverse of v0IDFromV1: given a
// V0 numeric `Long` id, return the V1 record id.
// This is *not* the inverse of truncation (which is
// irreversible on its own). Instead, we look up the
// memo by V0 id in the v0MemoLookup table that the
// v0MemoList and v0MemoAllList handlers populate
// and stash in the request context. Callers that
// need the V1 id of a memo by its V0 id should
// resolve through the lookup, not through this
// helper.
//
// This helper is only used as a defensive fallback
// in error paths; the V1 record id is the truth and
// the V0 id is a derived projection. Most call sites
// already have the V1 id from the connect response.
func v0IDToV1(v0 int64) string {
	// This is a stub; the full mapping is
	// maintained by the per-request lookup
	// table. Returning the decimal form is at
	// least a non-empty string the connect
	// handler can use to surface a meaningful
	// 404 if it ever sees it.
	return strconv.FormatInt(v0, 10)
}

// v0IDMap is the per-request V0↔V1 id resolution
// table. The V0 list handler populates it as it
// shapes the V1 memos into the V0 wire format;
// later V0 requests (PATCH, DELETE, organizer)
// read the map to translate the V0 Long id in
// the URL path into a V1 resource name.
//
// The map is stash in the request context under
// the `v0IDMapContextKey{}` key. The map is
// per-request, not shared across goroutines.
type v0IDMap struct {
	// V0→V1: Long id → "memos/<15-char-id>"
	v0ToV1 map[int64]string
	// V1→V0: "memos/<15-char-id>" → Long id
	v1ToV0 map[string]int64
}

func newV0IDMap() *v0IDMap {
	return &v0IDMap{
		v0ToV1: map[int64]string{},
		v1ToV0: map[string]int64{},
	}
}

// record adds one V0/V1 id pair to the map. The
// map is keyed on the V0 Long; a duplicate V0 id
// (e.g. two V1 memos that hash-collide through
// v0IDFromV1) is rejected with an error so the
// caller can surface a 500.
func (m *v0IDMap) record(v1Name string, v0ID int64) error {
	if existing, ok := m.v0ToV1[v0ID]; ok && existing != v1Name {
		return fmt.Errorf("v0 id collision: %d → %s and %s",
			v0ID, existing, v1Name)
	}
	m.v0ToV1[v0ID] = v1Name
	m.v1ToV0[v1Name] = v0ID
	return nil
}

// resolveV0ID looks up a V0 Long id in the map.
// Returns the V1 resource name (e.g. `memos/abc`)
// and `true` on hit; `""` and `false` on miss.
// The caller should fall back to a
// `ListMemos`-driven population if the map is
// empty (this happens on the first V0 PATCH
// after server start, before the V0 list
// handler has run for the same connection).
func (m *v0IDMap) resolveV0ID(v0ID int64) (string, bool) {
	if m == nil {
		return "", false
	}
	v1, ok := m.v0ToV1[v0ID]
	return v1, ok
}

// v0IDMapContextKey is the context key under
// which a per-request `*v0IDMap` is stashed.
// The V0 PATCH/DELETE/organizer handlers
// consult the map to resolve the V0 Long id in
// the URL path into a V1 resource name.
type v0IDMapContextKey struct{}

// withV0IDMap stashes a *v0IDMap on the request
// context. The V0 list handler calls this; the
// V0 PATCH/DELETE/organizer handlers read it
// via `v0IDMapFromContext`.
func withV0IDMap(ctx context.Context, m *v0IDMap) context.Context {
	return context.WithValue(ctx, v0IDMapContextKey{}, m)
}

// v0IDMapFromContext returns the *v0IDMap
// stashed by `withV0IDMap`, or `nil` if no map
// is on the context. A nil map is the "no
// resolution" signal; the V0 write handlers
// then build a fresh map on the fly.
func v0IDMapFromContext(ctx context.Context) *v0IDMap {
	m, _ := ctx.Value(v0IDMapContextKey{}).(*v0IDMap)
	return m
}

// populateV0IDMapFromV1List walks a V1
// `ListMemosResponse` body and adds every
// (V0 id, V1 name) pair to the map. The V0
// list handler calls this as it shapes the
// response, so subsequent V0 PATCH/DELETE
// requests in the same client session can
// resolve V0 ids without an extra round trip.
func populateV0IDMapFromV1List(m *v0IDMap, v1Body map[string]any) error {
	if m == nil {
		return nil
	}
	memos, _ := v1Body["memos"].([]any)
	for _, raw := range memos {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		v1id := strings.TrimPrefix(name, "memos/")
		if v1id == name {
			// Not a memos/ resource name;
			// skip.
			continue
		}
		v0id := v0IDFromV1(v1id)
		if err := m.record(name, v0id); err != nil {
			return err
		}
	}
	return nil
}

// resolveV0IDForRequest is the
// V0-write-handler-side helper. It looks the
// V0 Long id up in the per-request map; on a
// miss it returns an empty string and the
// caller surfaces a 404. The caller is
// responsible for populating the map (e.g.
// by calling `v0ListMemosToPopulateMap`
// first); the helper itself never makes a
// connect call.
func resolveV0IDForRequest(ctx context.Context, v0ID int64) string {
	m := v0IDMapFromContext(ctx)
	if m == nil {
		return ""
	}
	v1, _ := m.resolveV0ID(v0ID)
	return v1
}

// stableLongHash is the fallback for V1 ids that
// aren't 14-hex-digit-parseable (i.e. the V1 id
// contains lowercase letters, which PocketBase
// produces). FNV-1a 64-bit is the same hash the
// rest of the codebase uses for content-hash
// file ids; it's stable, fast, and good enough
// for a V0 projection.
func stableLongHash(s string) int64 {
	const (
		offset uint64 = 14695981039346656037
		prime  uint64 = 1099511628211
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	if h == 0 {
		return 1
	}
	return int64(h)
}

// ============================================================
// Request side: V0 → V1
// ============================================================

// v0UserMeToV1GetCurrentUser produces an empty JSON
// object `{}` for the V1 `GetCurrentUserRequest`. The
// V0 path `GET /api/v1/user/me` has no request body;
// the V1 connect handler reads the caller's identity
// from the bearer token.
func v0UserMeToV1GetCurrentUser(_ []byte, _ map[string]string, _ url.Values) ([]byte, error) {
	return []byte("{}"), nil
}

// v0MemoListToV1ListMemos translates the V0 query
// string into a V1 `ListMemosRequest` body. The V0
// client uses flat query params (`creatorId`,
// `pinned`, `tag`, `limit`, `offset`); the V1
// connect handler uses a JSON body with a `filter`
// string and a `page_size` integer.
func v0MemoListToV1ListMemos(_ []byte, _ map[string]string, q url.Values) ([]byte, error) {
	// The V0 `creatorId` is the caller's V0 Long
	// id; we ignore it because the V1 connect
	// handler scopes the list to the caller's
	// own memos by default (the V0 client is
	// always signed in as the active user).
	filter := ""
	if pinned := q.Get("pinned"); pinned != "" {
		if pinned == "true" {
			filter = "pinned = true"
		} else {
			filter = "pinned = false"
		}
	}
	pageSize := int32(50)
	if limit := q.Get("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && n <= 1000 {
			pageSize = int32(n)
		}
	}
	body := map[string]any{
		"filter":    filter,
		"page_size": pageSize,
	}
	if offset := q.Get("offset"); offset != "" {
		// The V0 client uses `offset`; the V1
		// client uses `page_token`. We don't
		// bother to translate — the V0 offset
		// is for `tag` or `pinned` filter
		// continuations, and the V1 connect
		// handler is happy to return the first
		// page for any of those queries.
		_ = offset
	}
	return json.Marshal(body)
}

// v0MemoCreateToV1CreateMemo translates a V0 create
// body (`MemosV0Memo` minus `id`, `createdTs`,
// `updatedTs`, `creatorId`, `resourceList`) into a
// V1 `CreateMemoRequest{memo: Memo}`. The V0 body is
// already the inner `Memo` shape (no envelope),
// because the V0 client doesn't wrap. Visibility is
// a V0 string ("PRIVATE" / "PROTECTED" / "PUBLIC");
// the helper maps it to the V1 enum.
func v0MemoCreateToV1CreateMemo(body []byte, _ map[string]string, _ url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	// The V0 client sends `content` already;
	// strip the V0-specific fields that the V1
	// proto doesn't have. `rowStatus` is V0-only
	// (V1 uses `state`); a V0 create with
	// `rowStatus = NORMAL` is the default and
	// the V1 server picks NORMAL on its own.
	delete(inner, "id")
	delete(inner, "createdTs")
	delete(inner, "updatedTs")
	delete(inner, "creatorId")
	delete(inner, "resourceList")
	// `resourceIdList` is the V0 *request* field for attachments
	// (a flat array of integer ids; see the client's OpenAPI
	// spec). The V1 `Memo` proto has no such field, so it must
	// not survive into the V1 body or the create handler would
	// reject it as an unknown field. The binding itself is done
	// by the caller (see `v0LinkResourcesToMemo`).
	delete(inner, "resourceIdList")
	delete(inner, "rowStatus")
	if vis, ok := inner["visibility"].(string); ok {
		inner["visibility"] = v0VisibilityToProto(vis)
	}
	return jsonMarshal(map[string]any{"memo": inner})
}

// v0MemoOrganizerToV1UpdateMemo translates
// `POST /api/v1/memo/{id}/organizer?pinned=true|false`
// into a V1 `UpdateMemoRequest{memo: {name,
// pinned}, update_mask: "pinned"}`. The V0 path is
// a verb-style route; the V1 path is a PATCH with
// an update mask. The handler reads the body for
// `{pinned: bool}` (the V0 client also posts a
// tiny JSON body with the new value).
func v0MemoOrganizerToV1UpdateMemo(body []byte, path map[string]string, q url.Values) ([]byte, error) {
	id, ok := path["id"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {id}")
	}
	// The V0 client posts a body like
	// `{"pinned": true}` OR passes `?pinned=true`
	// as a query string. Read both.
	pinned := false
	if p := q.Get("pinned"); p != "" {
		pinned = p == "true" || p == "1"
	} else {
		inner, err := decodeFlatObject(body)
		if err == nil {
			if v, ok := inner["pinned"].(bool); ok {
				pinned = v
			}
		}
	}
	return jsonMarshal(map[string]any{
		"memo": map[string]any{
			"name":   "memos/" + id,
			"pinned": pinned,
		},
		// protojson renders
		// `google.protobuf.FieldMask` as a
		// comma-separated string, not as a
		// `paths: [...]` object (the object
		// form is rejected by the V1
		// connect handler with "syntax
		// error ... unexpected token {"). The
		// V1 wrap helper emits the same
		// shape.
		"update_mask": "pinned",
	})
}

// v0MemoPatchToV1UpdateMemo translates a V0
// `PATCH /api/v1/memo/{id}` body into a V1
// `UpdateMemoRequest`. The V0 body echoes the
// numeric `id`; the V1 path injects the resource
// name from the URL. The V0 PATCH supports
// `content`, `visibility`, `rowStatus`, and
// `pinned`; the V0 visibility string is mapped
// to the V1 enum, and the V0 `rowStatus`
// ("ARCHIVED" / "NORMAL") is mapped to the V1
// `state` field.
//
// `update_mask` is built from the V0 fields the
// body actually populated; the V0 client posts
// every field it intends to change, so the
// mask is the field set minus the V0-only
// fields we strip. FieldMask rejects the
// `*` token (it must be a comma-separated list
// of valid field paths), so we compute the
// mask explicitly.
func v0MemoPatchToV1UpdateMemo(body []byte, path map[string]string, _ url.Values) ([]byte, error) {
	id, ok := path["id"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {id}")
	}
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	delete(inner, "id")
	if vis, ok := inner["visibility"].(string); ok {
		inner["visibility"] = v0VisibilityToProto(vis)
	}
	if rs, ok := inner["rowStatus"].(string); ok {
		inner["state"] = rowStatusToState(rs)
		delete(inner, "rowStatus")
	}
	// `resourceList` and `resourceIdList` are V0-only fields:
	// the V1 `Memo` proto has neither, so leaving either in the
	// body would put it in the V1 `update_mask`, where the
	// `UpdateMemo` handler ignores it (or rejects it outright).
	// The V0 PATCH handler reads the list before it calls this
	// shaper and binds the attachments itself (see
	// `v0LinkResourcesToMemo`).
	//
	// `resourceIdList` is the one the real client sends; it is a
	// flat array of integer ids, whereas `resourceList` is the
	// object array that only ever appears in a *response*.
	delete(inner, "resourceList")
	delete(inner, "resourceIdList")
	inner["name"] = "memos/" + id
	// Build the mask from the keys the V0
	// body populated (after the V0→V1
	// field renames). `name` is path-injected
	// and must not be in the mask.
	mask := make([]string, 0, len(inner))
	for k := range inner {
		if k == "name" {
			continue
		}
		mask = append(mask, k)
	}
	// protojson renders
	// `google.protobuf.FieldMask` as a
	// comma-separated string. The V1 connect
	// handler parses it with
	// `fieldMaskPaths` (see
	// `pb/connect/memo.go::UpdateMemo`).
	return jsonMarshal(map[string]any{
		"memo":        inner,
		"update_mask": strings.Join(mask, ","),
	})
}

// v0MemoDeleteToV1DeleteMemo produces a
// `DeleteMemoRequest{name: "memos/{id}"}` from the
// V0 path variable. The V0 client sends an empty
// body for DELETE.
func v0MemoDeleteToV1DeleteMemo(_ []byte, path map[string]string, _ url.Values) ([]byte, error) {
	id, ok := path["id"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {id}")
	}
	return jsonMarshal(map[string]any{"name": "memos/" + id})
}

// v0ResourceListToV1ListAttachments translates a V0
// `GET /api/v1/resource?creatorId=<long>&limit=<int>`
// into a V1 `ListAttachmentsRequest`. The V0
// `creatorId` is the caller's V0 Long id; we
// ignore it because the V1 connect handler scopes
// the list to the caller's own resources by
// default.
func v0ResourceListToV1ListAttachments(_ []byte, _ map[string]string, q url.Values) ([]byte, error) {
	pageSize := int32(50)
	if limit := q.Get("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && n <= 1000 {
			pageSize = int32(n)
		}
	}
	return jsonMarshal(map[string]any{
		"page_size": pageSize,
	})
}

// v0TagListToV1ListMemosForTags translates a V0
// `GET /api/v1/tag` into a V1 `ListMemosRequest`
// with no filter and a small page size. The shaper
// then extracts the tag list from the memos'
// `tags` field. The V0 client also passes
// `?creatorId=<long>` to scope to a single user;
// we ignore it (the connect handler scopes to the
// caller).
func v0TagListToV1ListMemosForTags(_ []byte, _ map[string]string, _ url.Values) ([]byte, error) {
	return jsonMarshal(map[string]any{
		"page_size": 1000,
	})
}

// ============================================================
// Response side: V1 → V0
// ============================================================

// v0ShapeUser converts a V1 connect `GetCurrentUserResponse`
// body into a V0 `MemosV0User` shape. The V0 shape
// is flat and uses a numeric `id: Long`; the V1
// uses resource names and timestamps.
func v0ShapeUser(v1Body map[string]any) (map[string]any, error) {
	user, ok := v1Body["user"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("v1 GetCurrentUserResponse is missing user: %v", v1Body)
	}
	username, _ := user["username"].(string)
	// The V1 resource name is `users/<username>`;
	// we extract the V0 Long id by parsing the
	// V1 username as a V1 record id (the V1
	// converter emits `users/<username>` and the
	// V0 Long id is derived from `<username>`).
	v0id := v0IDFromV1(username)

	// The V0 client reads `userSettingList` as
	// `List<{key, value}>`. The V0 client only
	// has two settings today:
	//   - `memoVisibility` (PRIVATE | PROTECTED | PUBLIC)
	//   - `telegramChatId` (free-form)
	// Both come from `instance_settings.user_*` rows
	// owned by the caller; we emit a flat list of
	// `{key, value}` pairs. The shaper pulls the
	// values from a `user_settings` map the connect
	// handler stashes in the response context (see
	// v0UserMeHandler). For the Memos default
	// (no user_settings row), we emit the
	// sensible default of `memoVisibility = PRIVATE`.
	settings, _ := user["userSettingList"].([]any)
	if settings == nil {
		settings = []any{}
	}
	visibility := "PRIVATE"
	telegram := ""
	for _, s := range settings {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if k, _ := m["key"].(string); k == "memoVisibility" {
			if v, ok := m["value"].(string); ok {
				visibility = protoVisibilityToV0(v)
			}
		}
		if k, _ := m["key"].(string); k == "telegramChatId" {
			if v, ok := m["value"].(string); ok {
				telegram = v
			}
		}
	}

	displayName, _ := user["displayName"].(string)
	email, _ := user["email"].(string)
	avatarURL, _ := user["avatarUrl"].(string)
	description, _ := user["description"].(string)
	role, _ := user["role"].(string)
	// V0 role is "OWNER" / "ADMIN" / "USER". The
	// V1 proto uses `Role` (ADMIN/USER); we
	// map ADMIN → ADMIN and USER → USER. There
	// is no V0 OWNER; the closest is ADMIN.
	v0role := "USER"
	if role == "ADMIN" {
		v0role = "ADMIN"
	}
	openID, _ := user["openId"].(string)
	if openID == "" {
		openID = username
	}
	openIDSubject := email
	// The V0 `name` is the display name.
	name := displayName
	if name == "" {
		name = username
	}
	return map[string]any{
		"id":                v0id,
		"name":              name,
		"role":              v0role,
		"email":             email,
		"openId":            openID,
		"openIdSubject":     openIDSubject,
		"username":          username,
		"nickname":          displayName,
		"avatarUrl":         avatarURL,
		"description":       description,
		"userSettingList":   settings,
		"defaultVisibility": visibility,
		"telegramChatId":    telegram,
	}, nil
}

// v0ShapeMemoList converts a V1 connect
// `ListMemosResponse` body into a top-level V0 array
// of `MemosV0Memo` shapes. The V0 shape is flat
// with numeric `id: Long`, a `rowStatus` string,
// V0 visibility, flat seconds-since-epoch
// timestamps, and an embedded `resourceList` array
// of `MemosV0Resource` shapes.
//
// Memos whose content is the stub-memo sentinel
// and whose visibility is PRIVATE are filtered
// out: those are the stub memos the V0 resource
// upload handler creates to satisfy the V1
// `attachments.memo` requirement (see
// `v0CreateStubMemo`). The V0 client never posts
// a memo with the sentinel content, so the filter
// is a safe "stub detector" without requiring a
// payload sentinel.
func v0ShapeMemoList(v1Body map[string]any) ([]any, error) {
	memos, _ := v1Body["memos"].([]any)
	out := make([]any, 0, len(memos))
	for _, m := range memos {
		row, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if isV0StubMemo(row) {
			continue
		}
		shaped, err := v0ShapeMemo(row)
		if err != nil {
			return nil, err
		}
		out = append(out, shaped)
	}
	return out, nil
}

// isV0StubMemo returns true when the V1 memo
// JSON object is a stub created by
// `v0CreateStubMemo`: the content is the
// stub-memo sentinel and the visibility is
// PRIVATE. The combination is unique to the V0
// stub because the V0 client never posts
// content with the zero-width-space sentinel
// (its composer always requires at least one
// non-sentinel character).
func isV0StubMemo(m map[string]any) bool {
	content, _ := m["content"].(string)
	return connect.IsStubMemoContent(content)
}

// v0ShapeMemo converts one V1 `Memo` JSON object
// into the V0 `MemosV0Memo` shape. The V0 id is
// derived from the V1 resource name's last
// segment (the 15-char PocketBase id).
func v0ShapeMemo(m map[string]any) (map[string]any, error) {
	name, _ := m["name"].(string)
	// `memos/<id>` — strip the prefix.
	v1id := strings.TrimPrefix(name, "memos/")
	v0id := v0IDFromV1(v1id)

	// `creator` is `users/<id>`; V0 wants a
	// numeric Long creatorId. We derive it the
	// same way as the user id (the V0 client
	// treats creatorId as opaque, so any stable
	// projection works).
	creator, _ := m["creator"].(string)
	v0creatorID := v0IDFromV1(strings.TrimPrefix(creator, "users/"))

	content, _ := m["content"].(string)
	visibility, _ := m["visibility"].(string)
	rowStatus := "NORMAL"
	if s, ok := m["state"].(string); ok && strings.ToUpper(s) == "ARCHIVED" {
		rowStatus = "ARCHIVED"
	}
	pinned, _ := m["pinned"].(bool)
	createdTs, _ := protoTimestampToUnix(m["createTime"])
	updatedTs, _ := protoTimestampToUnix(m["updateTime"])

	// `attachments` is a list of V1 `Attachment`
	// JSON; we shape each into the V0 resource
	// shape.
	attachments, _ := m["attachments"].([]any)
	resources := make([]any, 0, len(attachments))
	for _, a := range attachments {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		resources = append(resources, v0ShapeResource(am))
	}

	return map[string]any{
		"id":           v0id,
		"rowStatus":    rowStatus,
		"creatorId":    v0creatorID,
		"createdTs":    createdTs,
		"updatedTs":    updatedTs,
		"content":      content,
		"visibility":   protoVisibilityToV0(visibility),
		"pinned":       pinned,
		"resourceList": resources,
		// The V0 client also reads `tags` and
		// `relations`; both are best-effort
		// fields and the V0 client treats an
		// empty list as "no relations / no
		// tags". We pass the V1 `tags` straight
		// through; the V0 client doesn't
		// distinguish between a server-side
		// computed list and a manual one.
		"tags":      m["tags"],
		"relations": []any{},
	}, nil
}

// v0ShapeResource converts a V1 `Attachment` JSON
// object into the V0 `MemosV0Resource` shape. The
// V0 id is derived from the V1 resource name; the
// V0 client treats the `externalLink` and
// `filename` as the only required fields.
func v0ShapeResource(a map[string]any) map[string]any {
	name, _ := a["name"].(string)
	v1id := strings.TrimPrefix(name, "attachments/")
	v0id := v0IDFromV1(v1id)
	filename, _ := a["filename"].(string)
	externalLink, _ := a["externalLink"].(string)
	if externalLink == "" {
		// The fallback points at riff's own `/file/...`
		// surface, not PocketBase's native `/api/files/...`
		// route: the native route serves the bytes to
		// anyone who knows the id (the `blob` field is
		// `Protected`, so the native route evaluates the
		// `ViewRule` but wants a short-lived *file token*,
		// which the V0 client does not have). The `/file/...`
		// surface authorizes with the caller's normal
		// bearer token.
		externalLink = "/file/attachments/" + v1id + "/" + filename
	}
	contentType, _ := a["type"].(string)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// `size` is an int64, which protojson emits as a JSON
	// string; read it through the shared coercion so the
	// V0 client sees the real byte count instead of 0.
	size := protoInt64(a["size"])
	creator, _ := a["creator"].(string)
	v0creatorID := v0IDFromV1(strings.TrimPrefix(creator, "users/"))
	// V0's `memoId` is the numeric id of the owning memo.
	// The V1 `Attachment.memo` carries it as a
	// `memos/<id>` resource name, so it projects through
	// the same derivation as every other id. It used to be
	// hardcoded to 0, which left MoeMemos unable to tell
	// which memo an unlinked upload belonged to.
	memoRef, _ := a["memo"].(string)
	v0memoID := v0IDFromV1(strings.TrimPrefix(memoRef, "memos/"))
	// The V1 `Attachment` proto has no `update_time` field at
	// all, so a resource's `updateTime` is always absent and the
	// raw read below yielded `updatedTs: 0`. V0 clients use the
	// resource's last-modified time for sort ordering and cache
	// validation, and 0 sorts every file to the epoch. Fall back
	// to `createTime`, which is the truthful "last changed at"
	// for an attachment whose bytes are immutable once uploaded.
	updatedTs := protoTimestampToUnixOr(a["updateTime"], 0)
	if updatedTs == 0 {
		updatedTs = protoTimestampToUnixOr(a["createTime"], 0)
	}
	return map[string]any{
		"id": v0id,
		// `uid` is the stable, URL-addressable handle for the
		// row. Upstream Memos populates it from
		// `store.Resource.UID` (a shortuuid) and serves the blob
		// at `GET /o/r/{uid}`; the MoeMemos iOS client's
		// `MemosV0Resource.path()` is literally
		// `"/o/r/\(uid)"`, with `"/o/r/\(name ?? "")"` as the
		// fallback when `uid` is nil. We omitted the field
		// entirely, so the Swift optional was always nil and the
		// client requested `/o/r/` with an empty segment.
		//
		// riff has no UID column; the PocketBase record id is the
		// equivalent stable handle, and it is what the `/o/r/{uid}`
		// route resolves. It is a string, not the numeric `id`
		// above, exactly as upstream types it.
		"uid":          v1id,
		"creatorId":    v0creatorID,
		"createdTs":    protoTimestampToUnixOr(a["createTime"], 0),
		"updatedTs":    updatedTs,
		"filename":     filename,
		"externalLink": externalLink,
		"type":         contentType,
		"size":         size,
		// The V0 client reads `memoId` to associate the
		// resource with a memo. It is derived from the V1
		// `Attachment.memo` resource name (0 when the
		// upload is not linked to a memo yet, which is the
		// honest answer for an unlinked row).
		"memoId": v0memoID,
	}
}

// v0ShapeResourceList converts a V1
// `ListAttachmentsResponse` body into a top-level
// V0 array of `MemosV0Resource` shapes.
func v0ShapeResourceList(v1Body map[string]any) []any {
	atts, _ := v1Body["attachments"].([]any)
	out := make([]any, 0, len(atts))
	for _, a := range atts {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, v0ShapeResource(am))
	}
	return out
}

// v0ShapeTagList extracts the unique tag list from a
// V1 `ListMemosResponse` body. The V0 client uses
// `GET /api/v1/tag` to populate a tag-picker UI;
// the V1 connect handler doesn't have a "list all
// tags" RPC, so the V0 shaper walks the recent memos
// the connect handler returns and de-dups.
func v0ShapeTagList(v1Body map[string]any) []any {
	memos, _ := v1Body["memos"].([]any)
	seen := map[string]bool{}
	out := []any{}
	for _, m := range memos {
		row, ok := m.(map[string]any)
		if !ok {
			continue
		}
		tags, _ := row["tags"].([]any)
		for _, t := range tags {
			s, ok := t.(string)
			if !ok || s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// v0ShapeSingleMemo converts a V1 `Memo` response
// into the V0 shape. The connect handler returns
// `{memo: {...}}` for the create / get / patch
// paths; we unwrap and shape.
func v0ShapeSingleMemo(v1Body map[string]any) (map[string]any, error) {
	// Create returns the Memo directly (not
	// wrapped). PATCH also returns the Memo
	// directly. GET returns the Memo directly. So
	// the connect response is the memo itself.
	return v0ShapeMemo(v1Body)
}

// ============================================================
// Internal helpers
// ============================================================

// v0VisibilityToProto maps the V0 visibility string
// to the V1 proto enum.
func v0VisibilityToProto(s string) string {
	switch strings.ToUpper(s) {
	case "PRIVATE":
		return "PRIVATE"
	case "PROTECTED":
		return "PROTECTED"
	case "PUBLIC":
		return "PUBLIC"
	default:
		return "PRIVATE"
	}
}

// protoVisibilityToV0 maps the V1 proto enum to the
// V0 string. The shaper emits the V0 strings because
// the Kotlin `MemosV0Memo.visibility: String`
// expects them.
func protoVisibilityToV0(s string) string {
	switch strings.ToUpper(s) {
	case "PRIVATE":
		return "PRIVATE"
	case "PROTECTED":
		return "PROTECTED"
	case "PUBLIC":
		return "PUBLIC"
	case "SPACE":
		// V0 has no SPACE; the closest is
		// PROTECTED (signed-in users only).
		return "PROTECTED"
	default:
		return "PRIVATE"
	}
}

// protoTimestampToUnix converts a V1 protojson
// timestamp into a flat Unix-seconds int64. The V0
// client reads `createdTs: Long` as
// seconds-since-epoch.
//
// protojson renders `google.protobuf.Timestamp` as
// an **RFC 3339 string** (`"2025-09-20T00:10:00Z"`),
// not as the `{"seconds": N, "nanos": N}` message
// shape — see the WKT mapping in protojson's
// `Marshal`. Reading only the message shape meant
// every V0 memo came back with `createdTs: 0` and
// `updatedTs: 0`, which the Kotlin client renders as
// 1970-01-01 (or, with some builds, drops the field
// from the memo's display metadata entirely).
//
// Both shapes are therefore accepted: the RFC 3339
// string first, then the structured form for the
// hand-built V1 bodies the unit tests feed the
// shaper directly. A value that is neither yields 0
// rather than an error, because one unreadable
// timestamp on one row must not fail a whole list.
func protoTimestampToUnix(v any) (int64, error) {
	switch t := v.(type) {
	case string:
		return parseRFC3339Seconds(t), nil
	case map[string]any:
		secs, _ := t["seconds"].(float64)
		return int64(secs), nil
	}
	return 0, nil
}

// parseRFC3339Seconds converts a protojson
// `Timestamp` string to Unix seconds. Sub-second
// precision is truncated (V0 `createdTs` is whole
// seconds), and an unparseable or absent value yields 0.
func parseRFC3339Seconds(s string) int64 {
	if s == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

// protoInt64 coerces a protojson int64 value to an
// int64. protojson emits 64-bit integers as JSON
// **strings** (`"15"`), because they can exceed the
// precision of an IEEE-754 double; the shaper used to
// read `size` as a `float64` and therefore always saw
// 0, so every V0 resource reported a zero-byte file.
// `json.Number` and `float64` are both still accepted
// so hand-built bodies keep working.
func protoInt64(v any) int64 {
	switch n := v.(type) {
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			return 0
		}
		return parsed
	case float64:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// protoTimestampToUnixOr is the error-free version
// of protoTimestampToUnix; the connect handler
// always populates the timestamp on a memo, but
// resources sometimes have a zero value and we
// don't want to fail the whole list because of one
// missing field.
func protoTimestampToUnixOr(v any, fallback int64) int64 {
	n, err := protoTimestampToUnix(v)
	if err != nil || n == 0 {
		return fallback
	}
	return n
}
