package connect

import (
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/pkg/errors"

	"github.com/incurl/riff/internal/cel"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// This file maps the CEL field names the SPA and the proto send
// onto the PocketBase schema behind them. It is the single place
// where the two vocabularies are reconciled, because they disagree
// in three ways:
//
//   - naming: the proto says `created_ts` / `updated_ts`, the
//     migration calls the columns `created` / `updated`.
//   - identity: the SPA sends `space == "spaces/<name>"` and
//     `creator == "users/<name>"`, but the columns store a record
//     id. resolveSpaceNameToID turns the name into the id before
//     the expression is compiled (see memoCELFilter).
//   - derived state: `has_link`, `has_task_list`, `has_code` and
//     `has_location` have no column at all; they are computed by
//     the composer into the `payload` JSON column and read back by
//     pb/connect/memos_translate.go.
//
// Everything not listed here is rejected by Compile. That is
// deliberate: the alternative is what used to happen, which was to
// ignore the whole filter string and return every row.

// memoVisibilityValues is the closed set the `visibility` column
// accepts (pb/migrations/1700000020_create_memos.go). Handing it to
// the compiler turns `visibility == "PUBLIK"` into a 400 instead of
// a query that silently matches nothing.
var memoVisibilityValues = []string{"PRIVATE", "PROTECTED", "PUBLIC", "SPACE"}

// memoStateValues mirrors the `state` column added by
// pb/migrations/1840000006_add_memo_state.go. The proto enum spells
// the values `NORMAL` / `ARCHIVED` / `DRAFT` / `HIDDEN` (only the
// zero value carries the `STATE_` prefix), which matches the column
// values, so no translation is needed beyond rejecting the zero value.
var memoStateValues = []string{"NORMAL", "ARCHIVED", "DRAFT", "HIDDEN"}

// memoCELFields is the schema for the `memos` collection.
//
// Every name here must resolve to something PocketBase can actually
// filter on. The collection is the upstream-Memos shape: `content`,
// `author`, `space`, `visibility`, `tags`, `payload`, `state`,
// `pinned`, `sketch`, `created`, `updated` (see
// pb/migrations/1700000020_create_memos.go plus the add-migrations
// that followed). The riff-only flat PO-33 columns (`type`, `bpm`,
// `key`, `sample_source`, `difficulty`, `license`, `attribution`,
// `video_url`) were **dropped** by
// pb/migrations/1860000001_drop_riff_only_memo_columns.go — their
// data lives inside the `sketch` JSON column now — so they are
// deliberately absent: referencing a dropped column would make
// PocketBase's own filter parser fail the query, and they are not
// part of the documented filter vocabulary in
// proto/api/v1/memo_service.proto either. `title` was never a column
// at all. A filter naming one of them is an `unknown field` 400,
// which is the honest answer.
func memoCELFields() []cel.Field {
	return []cel.Field{
		{Name: "content", Kind: cel.FieldColumn, Column: "content"},
		{Name: "creator", Kind: cel.FieldColumn, Column: "author"},
		{Name: "space", Kind: cel.FieldColumn, Column: "space"},
		{Name: "visibility", Kind: cel.FieldColumn, Column: "visibility", Const: memoVisibilityValues},
		{Name: "state", Kind: cel.FieldColumn, Column: "state", Const: memoStateValues},
		{Name: "tags", Kind: cel.FieldJSONList, Column: "tags"},
		// The SPA's tag filter is spelled with the singular field
		// name (`tag in ["work"]`) in useMemoFilters.ts, so both
		// spellings have to resolve to the same JSON array.
		{Name: "tag", Kind: cel.FieldJSONList, Column: "tags"},
		{Name: "created_ts", Kind: cel.FieldColumn, Column: "created"},
		{Name: "updated_ts", Kind: cel.FieldColumn, Column: "updated"},
		// Boolean: the SPA's pinned chip emits a bare `pinned`
		// (useMemoFilters.ts), which a bare column reference would
		// otherwise fail as a half-typed filter.
		{Name: "pinned", Kind: cel.FieldColumn, Column: "pinned", Boolean: true},

		// Derived predicates, computed by the composer and stored
		// in the `payload` JSON column. PocketBase resolves a dotted
		// name into a json_extract on the column, so these are
		// ordinary column references as far as the filter language is
		// concerned — `payload.property.has_link = true`.
		//
		// Each one carries its complement too: `!has_location` is a
		// shipped saved-view example, and a derived predicate has no
		// operator for the compiler to flip. `= null` covers the rows
		// written before the payload column existed (json_extract on a
		// NULL column yields NULL, and a comparison against `false`
		// does not match NULL).
		{
			Name:        "has_link",
			Kind:        cel.FieldBoolExpr,
			Expr:        "payload.property.has_link = true",
			NegatedExpr: "payload.property.has_link = null || payload.property.has_link = false",
		},
		{
			Name:        "has_task_list",
			Kind:        cel.FieldBoolExpr,
			Expr:        "payload.property.has_task_list = true",
			NegatedExpr: "payload.property.has_task_list = null || payload.property.has_task_list = false",
		},
		{
			Name:        "has_code",
			Kind:        cel.FieldBoolExpr,
			Expr:        "payload.property.has_code = true",
			NegatedExpr: "payload.property.has_code = null || payload.property.has_code = false",
		},
		{
			Name:        "has_incomplete_tasks",
			Kind:        cel.FieldBoolExpr,
			Expr:        "payload.property.has_incomplete_tasks = true",
			NegatedExpr: "payload.property.has_incomplete_tasks = null || payload.property.has_incomplete_tasks = false",
		},
		{
			Name: "has_location",
			Kind: cel.FieldBoolExpr,
			// A location needs both the place name and a coordinate;
			// either one alone is a half-filled draft.
			Expr:        "payload.location.placeholder != null && payload.location.latitude != null",
			NegatedExpr: "payload.location.placeholder = null || payload.location.latitude = null",
		},
	}
}

// attachmentCELFields is the schema for the `attachments` collection.
// The SPA's Attachments page filters with `memo_id == null` (the
// unused-attachment library) combined with whatever the caller
// typed, and the sidebar filters by name and type.
//
// The column names come from pb/migrations/1850000000_create_attachments.go:
// there is no `type` column (the MIME lives in `mime_type`) and no
// `space` column at all. `space` is nevertheless part of the public
// vocabulary — ListAttachmentsRequest documents it as "the linked
// memo's space resource name, or null when the attachment is
// unlinked" — so it is mapped onto the documented meaning by
// following the `memo` relation into `memos.space`. PocketBase's
// RecordFieldResolver resolves dotted relation paths itself (that is
// the same mechanism that makes `author.name = "x"` work in a
// collection rule), and it normalises a comparison against `null`
// into the IS NULL form, so both `space == "spaces/<name>"` and
// `space == null` keep working.
//
// `type` is kept as an alias of `mime_type` because the upstream
// Memos clients and older saved views spell it that way; it is not a
// column, only another name for the one that exists.
func attachmentCELFields() []cel.Field {
	return []cel.Field{
		{Name: "memo_id", Kind: cel.FieldColumn, Column: "memo"},
		{Name: "memo", Kind: cel.FieldColumn, Column: "memo"},
		{Name: "filename", Kind: cel.FieldColumn, Column: "filename"},
		{Name: "mime_type", Kind: cel.FieldColumn, Column: "mime_type"},
		{Name: "type", Kind: cel.FieldColumn, Column: "mime_type"},
		{Name: "size", Kind: cel.FieldColumn, Column: "size"},
		{Name: "space", Kind: cel.FieldColumn, Column: "memo.space"},
		// `creator` is documented as "the linked memo's creator, or
		// the uploader for an unlinked attachment". Only the orphan
		// case is a column: the upload pipeline stamps `creator` when
		// the row lands without a memo
		// (pb/connect/attachment_upload.go), so a bound row's creator
		// is its memo's author. The compiler renders the fallback.
		{
			Name:           "creator",
			Kind:           cel.FieldColumn,
			Column:         "creator",
			FallbackColumn: "memo.author",
			FallbackWhen:   "memo = null",
		},
		// ListAttachmentsRequest documents `create_time`; the SPA
		// uses the `created_ts` spelling that ListMemos uses. Both
		// name the same column.
		{Name: "create_time", Kind: cel.FieldColumn, Column: "created"},
		{Name: "created_ts", Kind: cel.FieldColumn, Column: "created"},
		{Name: "updated_ts", Kind: cel.FieldColumn, Column: "updated"},
	}
}

// CELIdentityRule names one field whose literals carry a resource
// name instead of the id the column stores, e.g.
// {Field: "space", Prefix: "spaces/"} for `space == "spaces/<name>"`.
type CELIdentityRule struct {
	// Field is the CEL field name to rewrite (e.g. "space").
	Field string
	// Prefix is the resource-name prefix the SPA sends (e.g.
	// "spaces/").
	Prefix string
}

// CELIdentityRewriter turns a name-bearing field value into the id
// the column stores. `space == "spaces/<name>"` has to become
// `space = "<id>"` before the expression can run, because the
// column is a PocketBase relation and PocketBase's filter backend
// compares it against record ids only. `creator == "users/<name>"`
// is the same problem in the other direction: `memos.author` and
// `attachments.creator` are relations to `users`, but the profile
// feed and the link-memo picker send the *username*.
//
// The rewrite happens on the *expression text*, before Compile, and
// only for the literal-shaped clauses the SPA emits:
//
//	space == "spaces/<name>"
//	space != "spaces/<name>"
//	space in ["spaces/<name>", ...]
//
// Anything more involved (a saved view that compares a space to a
// column, say) is left alone and will fail the field lookup later
// with a message the user can act on.
type CELIdentityRewriter struct {
	// App resolves a resource name to its record id.
	App IdentityResolver
	// Rules is the set of fields to rewrite. Each rule is applied
	// to the whole expression in turn; the rules are independent
	// because a value carries at most one resource-name prefix.
	Rules []CELIdentityRule
}

// IdentityResolver maps a resource name ("spaces/<name>",
// "users/<name>") to the PocketBase record id stored in the relation
// column. The concrete implementation is appIdentityResolver; the
// interface exists so the rewriter is testable without a live app.
type IdentityResolver interface {
	RecordID(resourceName string) (string, error)
}

// appIdentityResolver adapts a PocketBase app to IdentityResolver.
type appIdentityResolver struct{ app core.App }

// RecordID dispatches on the resource-name prefix. A users name is
// looked up the same way every other "users/<name>" lookup in this
// package is (lookupUserByName, which falls through to _superusers);
// anything else is a space name.
func (a appIdentityResolver) RecordID(resourceName string) (string, error) {
	const usersPrefix = "users/"
	if name, ok := strings.CutPrefix(resourceName, usersPrefix); ok {
		rec, err := lookupUserByName(a.app, name)
		if err != nil || rec == nil {
			return "", err
		}
		return rec.Id, nil
	}
	return resolveSpaceNameToID(a.app, resourceName)
}

// memoIdentityRules is the set of name-bearing relation literals the
// SPA emits for a memo feed. The space-scoped sidebar sends
// `space == "spaces/<name>"` and a profile feed sends
// `creator == "users/<username>"`; both columns hold record ids.
func memoIdentityRules() []CELIdentityRule {
	return []CELIdentityRule{
		{Field: "space", Prefix: "spaces/"},
		{Field: "creator", Prefix: "users/"},
	}
}

// newMemoIdentityRewriter is the rewriter every compiled feed filter
// goes through (memos and attachments share the vocabulary).
func newMemoIdentityRewriter(app core.App) CELIdentityRewriter {
	return CELIdentityRewriter{App: appIdentityResolver{app: app}, Rules: memoIdentityRules()}
}

// memoCELFilter compiles a `ListMemosRequest.filter` into the
// PocketBase filter string and the parameters it binds. It returns
// an empty string when the request carries no filter.
//
// The two-step shape matters: the space/creator names in
// `space == "spaces/<name>"` and `creator == "users/<name>"` are
// resource names, but the columns are relations holding record ids,
// so the literals have to be resolved before the expression can be
// compiled. The rewriter handles that on the text; the compiler
// handles the grammar.
func memoCELFilter(app core.App, filter string) (string, map[string]any, error) {
	if strings.TrimSpace(filter) == "" {
		return "", nil, nil
	}
	rewritten := newMemoIdentityRewriter(app).Rewrite(filter)
	compiled, err := cel.Compile(rewritten, cel.Options{Now: time.Now(), Fields: memoCELFields()})
	if err != nil {
		return "", nil, err
	}
	return compiled.Fragment, compiled.Argv, nil
}

// Rewrite returns expr with every `<Field> == "<Prefix><name>"` (and
// the `!=` / `in [...]` variants) clause replaced by the resolved
// record id. A value that does not carry the prefix is left alone —
// the caller may legitimately be filtering on a raw record id.
//
// A name that does not resolve is not an error here: an unknown
// space or username simply matches no memos, which is what the user
// asked for by typing a name that does not exist. The original
// literal renders as a value no row holds, and the query returns
// nothing.
func (r CELIdentityRewriter) Rewrite(expr string) string {
	if r.App == nil {
		return expr
	}
	out := expr
	for _, rule := range r.Rules {
		if rule.Field == "" || rule.Prefix == "" {
			continue
		}
		out = r.rewriteField(out, rule)
	}
	return out
}

// rewriteField is the single-rule pass behind Rewrite.
//
// It walks the expression as words rather than searching for the field
// name as a substring, because a substring search rewrites things that
// are not field references:
//
//	content.contains("space")   the word is a *value*, not `space`
//	payload.space == "."        a dotted JSON path, not the column
//	myspace == "spaces/x"       a longer identifier that ends in it
//
// The first case is the dangerous one: the old pass dropped the field
// name from the output, so a content search for the word "space"
// silently became `content.contains("")`, which matches every row —
// the class of bug the whole compiler exists to prevent.
func (r CELIdentityRewriter) rewriteField(expr string, rule CELIdentityRule) string {
	var out strings.Builder
	i := 0
	for i < len(expr) {
		c := expr[i]

		// A quoted string is an operand, never a field reference.
		if c == '"' || c == '\'' {
			end := quotedEnd(expr, i)
			if end < 0 {
				out.WriteString(expr[i:])
				break
			}
			out.WriteString(expr[i : end+1])
			i = end + 1
			continue
		}

		if !isWordByte(c) {
			out.WriteByte(c)
			i++
			continue
		}

		start := i
		for i < len(expr) && isWordByte(expr[i]) {
			i++
		}
		word := expr[start:i]
		// A dotted path is one word, so `payload.space` never equals
		// the field name and is left alone.
		if word != rule.Field {
			out.WriteString(word)
			continue
		}

		// Expect optional whitespace, then `==` / `!=` / `in`.
		j := i
		for j < len(expr) && (expr[j] == ' ' || expr[j] == '\t') {
			j++
		}
		op := ""
		switch {
		case strings.HasPrefix(expr[j:], "=="):
			op = "=="
			j += 2
		case strings.HasPrefix(expr[j:], "!="):
			op = "!="
			j += 2
		case strings.HasPrefix(expr[j:], "in") && !isWordContinuation(expr[j+2:]):
			op = "in"
			j += 2
		default:
			// A bare reference (`space` alone, or a method call). Not
			// a name-bearing literal; emit it and keep scanning.
			out.WriteString(word)
			continue
		}

		for j < len(expr) && (expr[j] == ' ' || expr[j] == '\t') {
			j++
		}
		// `space == null` and `space != null` test emptiness and carry
		// no value to resolve; re-emit them verbatim.
		if strings.HasPrefix(expr[j:], "null") && !isWordContinuation(expr[j+len("null"):]) {
			out.WriteString(rule.Field + " " + op + " null")
			i = j + len("null")
			continue
		}
		if j < len(expr) && expr[j] == '[' {
			end := strings.IndexByte(expr[j:], ']')
			if end < 0 {
				out.WriteString(word)
				continue
			}
			list := expr[j : j+end+1]
			out.WriteString(rule.Field + " " + op + " " + r.rewriteList(list, rule.Prefix))
			i = j + end + 1
			continue
		}
		if j >= len(expr) || (expr[j] != '"' && expr[j] != '\'') {
			// A non-literal operand (`space == other_space`): leave the
			// comparison to the compiler, which will reject the whole
			// field rather than let the rewriter guess.
			out.WriteString(word)
			continue
		}
		end := quotedEnd(expr, j)
		if end < 0 {
			out.WriteString(word)
			continue
		}
		value := expr[j+1 : end]
		out.WriteString(rule.Field + " " + op + " " + strconv.Quote(r.rewriteValue(value, rule.Prefix)))
		i = end + 1
	}
	return out.String()
}

// quotedEnd returns the index of the quote closing the literal that
// starts at start, or -1 when the literal is unterminated. A backslash
// escapes the next byte so `"a\"b"` does not end at the inner quote.
func quotedEnd(s string, start int) int {
	quote := s[start]
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case quote:
			return i
		default:
			// An ordinary byte inside the literal.
		}
	}
	return -1
}

// isWordByte reports whether c can appear inside a CEL identifier or a
// dotted path. Treating `.` as part of a word is what keeps a dotted
// reference from being mistaken for a bare column.
func isWordByte(c byte) bool {
	return c == '_' || c == '.' ||
		c >= '0' && c <= '9' ||
		c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z'
}

// isWordContinuation reports whether rest begins with another
// identifier byte, which means the token just matched was only the
// prefix of a longer word (`in` in `index`, `null` in `nullable`).
func isWordContinuation(rest string) bool {
	return len(rest) > 0 && isWordByte(rest[0])
}

// rewriteList rewrites each quoted element of a `[...]` list. Only
// quoted strings are of interest; everything else (commas, spaces) is
// passed through verbatim so an unusual but valid list survives.
func (r CELIdentityRewriter) rewriteList(list, prefix string) string {
	var out strings.Builder
	out.WriteByte('[')
	rest := list[1:]
	for len(rest) > 0 && rest[0] != ']' {
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				break
			}
			out.WriteString(strconv.Quote(r.rewriteValue(rest[1:1+end], prefix)))
			rest = rest[end+2:]
			continue
		}
		out.WriteByte(rest[0])
		rest = rest[1:]
	}
	out.WriteByte(']')
	return out.String()
}

// rewriteValue resolves one operand. A value that is not a
// resource name (no prefix) is returned verbatim so a caller
// filtering on a raw record id keeps working. A name that does not
// resolve is also returned verbatim: the filter then compares
// against a value no row holds, which matches nothing — the right
// answer for "a space that does not exist" — rather than silently
// degrading into a filter that matches everything.
func (r CELIdentityRewriter) rewriteValue(value, prefix string) string {
	if !strings.HasPrefix(value, prefix) {
		return value
	}
	id, err := r.App.RecordID(value)
	if err != nil || id == "" {
		return value
	}
	return id
}

// memoStateFilter compiles the `ListMemosRequest.state` enum into a
// filter fragment and the parameter it binds.
//
// An unspecified state yields an empty fragment and no parameters:
// the caller asked for no state constraint, not for the zero value.
// (The SPA relies on this — the home feed leaves `state` unset and
// must see both NORMAL and ARCHIVED memos.)
func memoStateFilter(state v1.State) (string, map[string]any, error) {
	if state == v1.State_STATE_UNSPECIFIED {
		return "", nil, nil
	}
	name := state.String()
	for _, allowed := range memoStateValues {
		if name == allowed {
			return "state = {:__state}", map[string]any{"__state": name}, nil
		}
	}
	return "", nil, errors.Errorf("unknown state %q", state.String())
}

// joinFilters ANDs two compiled fragments together, merging their
// parameters. PocketBase's filter grammar has no grouping operator
// other than parentheses, so the incoming fragment is wrapped before
// it is joined.
func joinFilters(a string, aArgs map[string]any, b string, bArgs map[string]any) (string, map[string]any) {
	if strings.TrimSpace(b) == "" {
		return a, aArgs
	}
	if strings.TrimSpace(a) == "" {
		return b, bArgs
	}
	merged := make(map[string]any, len(aArgs)+len(bArgs))
	for k, v := range aArgs {
		merged[k] = v
	}
	for k, v := range bArgs {
		merged[k] = v
	}
	return a + " && (" + b + ")", merged
}

// attachmentCELFilter compiles a `ListAttachmentsRequest.filter`. It
// is the same shape as memoCELFilter but over the attachments schema.
// The Attachments page joins `memo_id == null` with the sidebar's
// clauses through combineCELFilters, which wraps each one in
// parentheses, and a space-scoped library sends `space ==
// "spaces/<name>"` — so the space name is resolved to the space id
// before the expression is compiled, exactly as ListMemos does.
func attachmentCELFilter(app core.App, filter string) (string, map[string]any, error) {
	if strings.TrimSpace(filter) == "" {
		return "", nil, nil
	}
	rewritten := newMemoIdentityRewriter(app).Rewrite(filter)
	compiled, err := cel.Compile(rewritten, cel.Options{Now: time.Now(), Fields: attachmentCELFields()})
	if err != nil {
		return "", nil, err
	}
	return compiled.Fragment, compiled.Argv, nil
}
