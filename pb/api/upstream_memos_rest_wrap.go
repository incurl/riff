package api

import (
	"fmt"
	"net/url"
	"strings"
)

// The Memos (V0) REST surface addresses resources by path segment
// (`/memos/api/memos/{name}`), but the Connect handlers read the
// resource from the request BODY (`GetMemoRequest.name` =
// "memos/{id}"). The plain `connect_proxy` flavor forwards the
// body verbatim, so a path-parameter route reached the handler with
// an empty `name` and every such route answered
// `{"code":"invalid_argument","message":"name is required"}`.
//
// This file adds the `connect_proxy_wrap` flavor to the Memos
// route table — the V1 table already had it — together with the
// body wrappers that turn a path variable into the field the
// Connect request expects.
//
// # Two request shapes
//
// The Connect request messages fall into two groups:
//
//   - a FLAT request, whose fields sit at the top level of the
//     body (`GetMemoRequest{name}`). The wrapper injects the
//     resource name directly.
//   - a NESTED request, whose payload sits inside a message
//     (`UpdateMemoRequest{memo, update_mask}`). The wrapper nests
//     the caller's body under that field and adds the mask.
//
// The route table names which one each route needs, so the two
// cannot be confused.

// pathFieldSpec describes how a Memos path variable maps onto a
// Connect request field.
//
//	from   the path variable name, e.g. "parent"
//	to     the Connect request field name, e.g. "name"
//	prefix the resource-name prefix, e.g. "memos" — the result is
//	       prefix + "/" + value. Empty means the value is used as-is.
type pathFieldSpec struct {
	from   string
	to     string
	prefix string
}

// pathWrapSpec describes one route's path-variable mapping and the
// shape of the Connect request it feeds.
//
//	nested is the request's payload field ("" for a flat request).
//	append is the path variable appended to the resource name built
//	by the previous spec — the second segment of a two-segment
//	Memos path such as `{parent}` + `{name}`.
type pathWrapSpec struct {
	fields []pathFieldSpec
	nested string
	append string
}

// pathFieldSpecs is the Memos-table equivalent of the V1 table's
// `v1BodyWrappers` registry: a route's path pattern maps to the
// mapping that route needs.
//
// The key is the route pattern without its leading slash, so the
// registry reads like the route table itself.
//
// A route pattern can appear more than once when its methods need
// different shapes (GET /memos/api/memos/{name} is flat, PATCH on
// the same path is nested). The method-specific entries win; see
// pathWrapSpecFor.
var pathFieldSpecs = map[string]pathWrapSpec{
	// ---- users ----------------------------------------------------
	// The Memos client addresses a user by name; the Connect
	// request wants the full resource name.
	"memos/api/users/{name}/getStats": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "users"}},
	},
	"memos/api/users/{name}/exportMemos": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "users"}},
	},
	"memos/api/users/{name}/importMemos": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "users"}},
	},

	// ---- user webhooks --------------------------------------------
	// The signing-secret route carries BOTH the parent and the
	// webhook name; the Connect request wants one composed
	// resource name.
	"memos/api/users/{parent}/webhooks/{name}/getSigningSecret": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "users"}},
		append: "name",
	},
	// The webhook CRUD routes take the parent user; the delete
	// takes the composed resource name.
	"memos/api/users/{parent}/webhooks": {
		fields: []pathFieldSpec{{from: "parent", to: "parent", prefix: "users"}},
	},
	"memos/api/users/{parent}/webhooks/{name}": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "users"}},
		append: "name",
	},

	// ---- memos ----------------------------------------------------
	"memos/api/memos/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{parent}/attachments": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{name}/attachments": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{parent}/relations": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{name}/relations": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{parent}/comments": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{parent}/reactions": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "memos"}},
	},
	"memos/api/memos/{name}/reactions/{reaction_name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "memos"}},
		append: "reaction_name",
	},
	"memos/api/memos/{parent}/shares": {
		fields: []pathFieldSpec{{from: "parent", to: "parent", prefix: "memos"}},
	},
	"memos/api/memos/{name}/shares/{share_name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "memos"}},
		append: "share_name",
	},
	"memos/api/shares/{code}/memo": {
		fields: []pathFieldSpec{{from: "code", to: "share_token"}},
	},

	// ---- attachments ----------------------------------------------
	"memos/api/attachments/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "attachments"}},
	},

	// ---- spaces ---------------------------------------------------
	"memos/api/spaces/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
	},
	"memos/api/spaces/{parent}/members": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "spaces"}},
	},
	"memos/api/spaces/{name}/members/{member_name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
		append: "member_name",
	},
	"memos/api/spaces/{parent}/invitations": {
		fields: []pathFieldSpec{{from: "parent", to: "parent", prefix: "spaces"}},
	},
	"memos/api/spaces/{name}/invitations/{invitation_name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
		append: "invitation_name",
	},
	"memos/api/spaces/{name}/invitations/{invitation_name}/accept": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
		append: "invitation_name",
	},
	"memos/api/spaces/{name}/invitations/{invitation_name}/decline": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
		append: "invitation_name",
	},

	// ---- personal access tokens ------------------------------------
	// The list and create requests take the parent user; the delete
	// request takes the full token resource name.
	"memos/api/users/{parent}/personalAccessTokens": {
		fields: []pathFieldSpec{{from: "parent", to: "parent", prefix: "users"}},
	},
	"memos/api/users/{parent}/personalAccessTokens/{name}": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "users"}},
		append: "name",
	},

	// ---- linked identities -----------------------------------------
	"memos/api/users/{parent}/linkedIdentities": {
		fields: []pathFieldSpec{{from: "parent", to: "parent", prefix: "users"}},
	},
	"memos/api/users/{parent}/linkedIdentities/{name}": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "users"}},
		append: "name",
	},

	// ---- instance settings -----------------------------------------
	// The setting name is the setting key itself, not a resource
	// name, so it is injected verbatim.
	"memos/api/instance/settings/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name"}},
	},

	// ---- identity providers ---------------------------------------
	"memos/api/identity-providers/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "identity-providers"}},
	},
}

// pathNestedSpecs overrides the shape for the methods whose Connect
// request nests the payload inside a message.
//
// The key is "<METHOD> <path pattern>", so a route whose GET is
// flat and whose PATCH is nested can carry both.
var pathNestedSpecs = map[string]pathWrapSpec{
	"PATCH memos/api/memos/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "memos"}},
		nested: "memo",
	},
	"POST memos/api/memos/{parent}/comments": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "memos"}},
		nested: "comment",
	},
	"POST memos/api/memos/{parent}/reactions": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "memos"}},
		nested: "reaction",
	},
	"POST memos/api/memos/{parent}/shares": {
		fields: []pathFieldSpec{{from: "parent", to: "parent", prefix: "memos"}},
		nested: "memo_share",
	},
	"PATCH memos/api/attachments/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "attachments"}},
		nested: "attachment",
	},
	"PATCH memos/api/spaces/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
		nested: "space",
	},
	"PATCH memos/api/spaces/{name}/members/{member_name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "spaces"}},
		append: "member_name",
		nested: "space_member",
	},
	"PATCH memos/api/instance/settings/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name"}},
		nested: "setting",
	},
	"PATCH memos/api/users/{parent}/webhooks/{name}": {
		fields: []pathFieldSpec{{from: "parent", to: "name", prefix: "users"}},
		append: "name",
		nested: "webhook",
	},
	"PATCH memos/api/identity-providers/{name}": {
		fields: []pathFieldSpec{{from: "name", to: "name", prefix: "identity-providers"}},
		nested: "identity_provider",
	},
}

// pathWrapSpecFor returns the mapping for a route, preferring the
// method-specific entry when the route has one.
func pathWrapSpecFor(method, path string) (pathWrapSpec, bool) {
	key := method + " " + memosPathPatternKey(path)
	if spec, ok := pathNestedSpecs[key]; ok {
		return spec, true
	}
	spec, ok := pathFieldSpecs[memosPathPatternKey(path)]
	return spec, ok
}

// wrapMemosPathBody builds a bodyWrapper from a path-wrap spec.
//
// A FLAT request (spec.nested == "") gets the resource name
// injected at the top level; the caller's own body is preserved so
// a POST that carries a payload (the reaction, the comment) still
// works.
//
// A NESTED request puts the caller's body inside spec.nested and
// adds the update mask, which is how `UpdateMemoRequest{memo,
// update_mask}` is assembled from a Memos PATCH body.
func wrapMemosPathBody(spec pathWrapSpec) bodyWrapper {
	return func(body []byte, path map[string]string, query url.Values) ([]byte, error) {
		inner, err := decodeFlatObject(body)
		if err != nil {
			return nil, err
		}

		for _, f := range spec.fields {
			value, ok := path[f.from]
			if !ok || value == "" {
				return nil, newWrapErr(400, fmt.Sprintf("missing path variable {%s}", f.from))
			}
			if f.prefix != "" {
				inner[f.to] = f.prefix + "/" + value
			} else {
				inner[f.to] = value
			}
		}

		// The second segment of a two-segment Memos path is
		// appended to the resource name built above.
		if spec.append != "" {
			value, ok := path[spec.append]
			if !ok || value == "" {
				return nil, newWrapErr(400, fmt.Sprintf("missing path variable {%s}", spec.append))
			}
			base, _ := inner["name"].(string)
			if base == "" {
				return nil, newWrapErr(400, fmt.Sprintf("cannot append {%s} without a base name", spec.append))
			}
			inner["name"] = base + "/" + value
		}

		if spec.nested == "" {
			return jsonMarshal(inner)
		}

		// A nested request: the caller's body becomes the payload
		// and the resource name moves inside it.
		name := inner["name"]
		delete(inner, "name")
		out := map[string]any{spec.nested: inner}
		if name != nil {
			out[spec.nested].(map[string]any)["name"] = name
		}
		// The update mask names the fields the request may touch.
		// An absent mask means "every field in the body"; see
		// fieldMaskJSON for why it must not be the literal "*".
		out["update_mask"] = fieldMaskJSON(query.Get("updateMask"))
		return jsonMarshal(out)
	}
}

// memosPathPatternKey normalises a route path into the key used by
// pathFieldSpecs. The route table writes the pattern as
// "/memos/api/memos/{name}"; the registry key is the same string
// without the leading slash.
func memosPathPatternKey(path string) string {
	return strings.TrimPrefix(path, "/")
}
