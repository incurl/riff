package connect

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithErrorLogging_LogsInternalErrors pins the observability
// property that was missing: a connect handler returning
// CodeInternal produced a 500 response and *no log line at all*.
//
// connect-go does not log handler errors, and the only interceptor
// riff registered (WithAuth) passed them through untouched. A 500 a
// user reported therefore had nothing to diagnose it from — the log
// stream was clean and only the response body carried the message.
//
// It also pins that a client error is not logged as a server error:
// InvalidArgument is expected traffic, and logging it at Error would
// drown the signal that matters.
func TestWithErrorLogging_LogsInternalErrors(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	const procedure = "/memos.api.v1.MemoService/ListMemos"

	t.Run("internal error is logged with the procedure", func(t *testing.T) {
		buf.Reset()
		interceptor := WithErrorLogging()
		handler := interceptor(func(_ context.Context, _ connect.Spec, _ connect.ServerStream) error {
			return connect.NewError(connect.CodeInternal, "boom")
		})
		err := handler(t.Context(), connect.Spec{Procedure: procedure}, nil)
		require.Error(t, err)

		out := buf.String()
		assert.Contains(t, out, "ListMemos", "the procedure must be logged")
		assert.Contains(t, out, "boom", "the error must be logged")
		assert.Contains(t, out, "level=ERROR", "an internal error is a server error")
	})

	t.Run("client error is not logged as an error", func(t *testing.T) {
		buf.Reset()
		interceptor := WithErrorLogging()
		handler := interceptor(func(_ context.Context, _ connect.Spec, _ connect.ServerStream) error {
			return connect.NewError(connect.CodeInvalidArgument, "bad filter")
		})
		_ = handler(t.Context(), connect.Spec{Procedure: procedure}, nil)

		assert.NotContains(t, buf.String(), "level=ERROR",
			"a client error is expected traffic, not a server error")
	})

	t.Run("success logs nothing", func(t *testing.T) {
		buf.Reset()
		interceptor := WithErrorLogging()
		handler := interceptor(func(_ context.Context, _ connect.Spec, _ connect.ServerStream) error {
			return nil
		})
		_ = handler(t.Context(), connect.Spec{Procedure: procedure}, nil)
		assert.Empty(t, buf.String(), "a successful call must not log")
	})
}
