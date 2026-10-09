package connect

import (
	"context"
	"net/http"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// discardResponseWriter satisfies http.ResponseWriter for synthetic request
// events. The record-request hooks never write a response, but router.Event
// methods such as Written() dereference the writer, so a nil one is a latent
// panic.
type discardResponseWriter struct{ header http.Header }

func (w discardResponseWriter) Header() http.Header {
	if w.header == nil {
		return http.Header{}
	}
	return w.header
}

func (w discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }

func (w discardResponseWriter) WriteHeader(_ int) {}

// newRecordRequestEvent builds a synthetic *core.RequestEvent that mirrors an
// incoming HTTP request: the caller found by the auth interceptor becomes
// RequestEvent.Auth (which the hooks read as @request.auth), and the original
// Authorization header is carried over so header-based rules resolve.
//
// This exists because Connect handlers execute with PocketBase superuser
// privileges, so the collection API rules (ListRule, CreateRule, DeleteRule, …)
// never apply to app.Save / app.Delete. Every riff business rule instead lives
// in the OnRecord*Request hooks under pb/hooks, and PocketBase fires those only
// from its own /api/collections/{collection}/records pipeline.
//
// A bare app.Save therefore skips author assignment, space-membership checks,
// hashtag parsing and the last-ADMIN invariant. Worse, for fields the schema
// marks Required (memos.author, spaces.owner) the write fails outright with
// "cannot be blank", so creating a memo or a space over Connect never worked.
//
// The helpers below rebuild the exact *core.RequestEvent shape the CRUD API
// builds (same Auth, same RequestInfo) and run the identical hook chain, with
// the persistence call as its tail. Writes issued through them behave exactly
// like the equivalent REST request, without duplicating a single rule.
func newRecordRequestEvent(app core.App, ctx context.Context, method string) (*core.RequestEvent, error) {
	req, err := http.NewRequestWithContext(ctx, method, "/", nil)
	if err != nil {
		return nil, err
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		if auth := info.RequestHeader().Get("Authorization"); auth != "" {
			req.Header.Set("Authorization", auth)
		}
	}
	return &core.RequestEvent{
		App:  app,
		Auth: CurrentUser(ctx),
		Event: router.Event{
			Request:  req,
			Response: discardResponseWriter{header: http.Header{}},
		},
	}, nil
}

// createRecordRequest persists rec through the OnRecordCreateRequest hook
// chain, so the collection's BeforeCreate rules run before the insert.
func createRecordRequest(ctx context.Context, app core.App, rec *core.Record) error {
	event, err := newRecordRequestEvent(app, ctx, http.MethodPost)
	if err != nil {
		return err
	}
	return app.OnRecordCreateRequest().Trigger(&core.RecordRequestEvent{
		RequestEvent: event,
		Collection:   rec.Collection(),
		Record:       rec,
	}, func(e *core.RecordRequestEvent) error {
		return e.App.Save(e.Record)
	})
}

// updateRecordRequest persists rec through the OnRecordUpdateRequest hook
// chain. rec must have been loaded from the database so the hooks can read the
// pre-change values via rec.Original().
func updateRecordRequest(ctx context.Context, app core.App, rec *core.Record) error {
	event, err := newRecordRequestEvent(app, ctx, http.MethodPatch)
	if err != nil {
		return err
	}
	return app.OnRecordUpdateRequest().Trigger(&core.RecordRequestEvent{
		RequestEvent: event,
		Collection:   rec.Collection(),
		Record:       rec,
	}, func(e *core.RecordRequestEvent) error {
		return e.App.Save(e.Record)
	})
}

// deleteRecordRequest removes rec through the OnRecordDeleteRequest hook
// chain, which owns the cascade rules.
func deleteRecordRequest(ctx context.Context, app core.App, rec *core.Record) error {
	event, err := newRecordRequestEvent(app, ctx, http.MethodDelete)
	if err != nil {
		return err
	}
	return app.OnRecordDeleteRequest().Trigger(&core.RecordRequestEvent{
		RequestEvent: event,
		Collection:   rec.Collection(),
		Record:       rec,
	}, func(e *core.RecordRequestEvent) error {
		return e.App.Delete(e.Record)
	})
}
