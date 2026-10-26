package api

import (
	"github.com/pocketbase/pocketbase/core"
)

// The `pb_records_crud` flavour of the Memos-shaped REST surface
// calls `app.FindRecordsByFilter` / `app.FindRecordById` directly.
// Those helpers do NOT consult the collection's access rules — the
// rule engine only runs inside PocketBase's own record API.
//
// So without an explicit check, the flavour served every row of
// every collection it was mounted on, whatever the collection's
// ListRule/ViewRule said. The rules below close that gap by
// evaluating the same rule the record API would have evaluated.

// canAccessRecord evaluates a collection's access rule for the
// caller.
//
// The rule is the one PocketBase's own record API uses, so the two
// surfaces agree on who may read or write what. A nil rule means
// "superusers only" and an empty rule means "everyone" — both are
// handled by [core.BaseApp.CanAccessRecord] itself.
func canAccessRecord(app core.App, e *core.RequestEvent, rec *core.Record, rule *string) (bool, error) {
	info, err := e.RequestInfo()
	if err != nil {
		return false, err
	}
	return app.CanAccessRecord(rec, info, rule)
}

// filterAccessibleRecords drops the records the caller may not read.
//
// It is the list-side counterpart of [canAccessRecord]: the query
// itself cannot carry the rule (PocketBase's filter grammar has no
// way to express an arbitrary access rule), so the rule is
// evaluated per row.
//
// The returned count is the number of PERMITTED records, not the
// number of rows the query matched — a raw count would leak the
// existence of records the caller cannot see.
func filterAccessibleRecords(app core.App, e *core.RequestEvent, coll *core.Collection, records []*core.Record, rule *string) ([]*core.Record, error) {
	out := make([]*core.Record, 0, len(records))
	for _, rec := range records {
		ok, err := canAccessRecord(app, e, rec, rule)
		if err != nil {
			// A malformed rule must not silently widen access;
			// skip the row.
			continue
		}
		if ok {
			out = append(out, rec)
		}
	}
	return out, nil
}
