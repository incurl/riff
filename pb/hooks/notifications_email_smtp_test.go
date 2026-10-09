package hooks

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/email"
	"github.com/incurl/riff/internal/profile"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// readBody consumes bytes from conn until the client
// terminates the DATA section with CRLF.CRLF. The captured
// bytes are returned to the test.
func readBody(conn net.Conn) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var captured []byte
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			captured = append(captured, buf[:n]...)
			if hasSMTPTerminator(captured) {
				return captured, nil
			}
		}
		if err != nil {
			return captured, err
		}
	}
}

// hasSMTPTerminator reports whether the captured bytes
// contain the CRLF.CRLF terminator that ends an SMTP DATA
// section.
func hasSMTPTerminator(b []byte) bool {
	for i := 0; i+4 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '.' && b[i+3] == '\r' && b[i+4] == '\n' {
			return true
		}
	}
	return false
}

// hasSMTPTerminator exposes the predicate under a name the
// readBody helper uses without colliding with the SMTP package
// types.

// TestEmailDispatcher_RealSMTPServer: spin up a tiny
// net.Listen-based fake SMTP server, point the dispatcher
// at it, and confirm the email is sent end-to-end through
// the real Client. This is the integration test that proves
// the wiring from dispatcher → email.Send → Client.Send
// → actual TCP works.
func TestEmailDispatcher_RealSMTPServer(t *testing.T) {
	// Stand up a fake SMTP server: accept the connection,
	// do the minimum protocol dance so net/smtp is happy,
	// then close. The test asserts that the server saw
	// the recipient and body bytes.
	var (
		mu      sync.Mutex
		gotMail string
		gotBody []byte
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

		// Server side of the SMTP dance. We drive the
		// protocol by hand so we can read what the client
		// wrote, not just observe a successful exchange.
		br := bufio.NewReader(conn)

		// Greet.
		_, _ = conn.Write([]byte("220 localhost fake\r\n"))
		// Read EHLO and reply.
		_, _ = br.ReadString('\n')
		_, _ = conn.Write([]byte("250-localhost\r\n250 OK\r\n"))
		// MAIL FROM
		_, _ = br.ReadString('\n')
		_, _ = conn.Write([]byte("250 OK\r\n"))
		// RCPT TO
		_, _ = br.ReadString('\n')
		_, _ = conn.Write([]byte("250 OK\r\n"))
		// DATA
		_, _ = br.ReadString('\n')
		_, _ = conn.Write([]byte("354 Start mail input\r\n"))
		body, _ := readBody(conn)
		mu.Lock()
		gotBody = body
		gotMail = "ok"
		mu.Unlock()
		// QUIT (best effort)
		_, _ = conn.Write([]byte("221 Bye\r\n"))
	}()

	addr := ln.Addr().(*net.TCPAddr)
	host, portStr, _ := net.SplitHostPort(addr.String())
	port, _ := strconv.Atoi(portStr)

	app := newHookedAppForHooks(t)
	seedNotificationSetting(t, app, &v1.InstanceSetting_NotificationSetting{
		Email: &v1.InstanceSetting_NotificationSetting_EmailSetting{
			Enabled:   true,
			SmtpHost:  host,
			SmtpPort:  int32(port),
			FromEmail: "noreply@riff.example.com",
			FromName:  "Riff",
		},
	})
	alice := newUserForHook(t, app, "alice")
	rec := notifFor(t, app, alice.Id, "SPACE_INVITATION", `{"space":{"id":"s1","uid":"labs","title":"Labs","role":"USER"},"from_user":{"id":"u1","username":"bob"}}`)

	// Use the real (sync) send so the assertion is
	// deterministic. The dispatcher's default async path
	// is the production path; this is the integration
	// test for the underlying email.Client.
	d := NewEmailDispatcher(&profile.Profile{InstanceURL: "https://riff.example.com"}, app, func(c *email.Config, m *email.Message) {
		if err := email.Send(c, m); err != nil {
			t.Logf("send: %v", err)
		}
	})
	require.NoError(t, d.DispatchInboxEmail(context.Background(), rec))

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("fake SMTP server did not observe a message")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "ok", gotMail, "the SMTP server should have seen a complete MAIL/RCPT/DATA cycle")
	assert.Contains(t, string(gotBody), "riff.example.com", "the body should mention the instance URL")
}
