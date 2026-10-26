// apns.go is an adaptation of finb/bark-server's apns/apns.go
// (https://github.com/Finb/bark-server), which is MIT licensed:
//
//	Copyright (c) 2018 Feng
//
// See the LICENSE file in that repository for the full text.
//
// The changes relative to upstream are:
//
//   - the client pool is created lazily behind a sync.Once and reports a
//     failure as an error, instead of upstream's logger.Fatalf at package
//     init time. A library must not kill the host process on import.
//   - the topic, key ID and team ID are configurable, so a third-party Bark
//     client can use its own APNs key. Upstream hard-codes the official
//     Bark app's identifiers.
//   - the push call goes through the Pusher interface so tests can inject a
//     fake instead of talking to Apple's servers.
//
// Upstream's bbolt-free design is preserved: this file never touched the
// database, so nothing had to be stripped.
package bark

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"

	"github.com/pkg/errors"
)

// PushMessage is the payload of a single Bark push.
//
// The field set is upstream bark-server's apns.PushMessage verbatim,
// including the `form`/`json`/`xml`/`query` struct tags. The tags are
// unused here (the HTTP layer uses its own decoding) but they are kept so
// the type stays a drop-in for anything that embedded upstream's.
type PushMessage struct {
	// Id is upstream's field name. It is kept verbatim so the type
	// stays a drop-in for anything that embedded upstream's
	// apns.PushMessage, even though Go style would call it ID.
	Id          string `form:"id,omitempty" json:"id,omitempty" xml:"id,omitempty" query:"id,omitempty"` //nolint:revive // the name must match upstream's apns.PushMessage
	DeviceToken string `form:"-" json:"-" xml:"-" query:"-"`
	DeviceKey   string `form:"device_key,omitempty" json:"device_key,omitempty" xml:"device_key,omitempty" query:"device_key,omitempty"`
	Subtitle    string `form:"subtitle,omitempty" json:"subtitle,omitempty" xml:"subtitle,omitempty" query:"subtitle,omitempty"`
	Title       string `form:"title,omitempty" json:"title,omitempty" xml:"title,omitempty" query:"title,omitempty"`
	Body        string `form:"body,omitempty" json:"body,omitempty" xml:"body,omitempty" query:"body,omitempty"`
	// ios notification sound(system sound please refer to http://iphonedevwiki.net/index.php/AudioServices)
	Sound     string                 `form:"sound,omitempty" json:"sound,omitempty" xml:"sound,omitempty" query:"sound,omitempty"`
	ExtParams map[string]interface{} `form:"ext_params,omitempty" json:"ext_params,omitempty" xml:"ext_params,omitempty" query:"ext_params,omitempty"`
}

// IsEmptyAlert reports whether the message carries no user-visible text.
//
// An empty alert is a legitimate request: it is how a silent (background)
// push is expressed, and how an encrypted push is delivered. Upstream
// substitutes a placeholder body for an empty alert unless the message is
// explicitly a delete/silent one.
func (p PushMessage) IsEmptyAlert() bool {
	return p.Title == "" && p.Body == "" && p.Subtitle == ""
}

// IsDelete reports whether the message asks for a silent push.
//
// Upstream treats the "delete" extension parameter set to 1 (as a string,
// an int, or a float) as the marker for a background push.
func (p PushMessage) IsDelete() bool {
	val := p.ExtParams["delete"]
	return val == "1" || val == 1 || val == 1.0
}

// The APNs identifiers of the official Bark iOS app.
//
// The embedded apnsPrivateKey (see apns_certs.go) is bound to the
// `me.fin.bark` topic, so these are the only values that key can sign
// for. A different key needs a different topic, key ID and team ID; see
// Options.
const (
	defaultTopic  = "me.fin.bark"
	defaultKeyID  = "LH4T9V5U4R"
	defaultTeamID = "5U8LBRXG3A"

	// PayloadMaximum is APNs' hard limit on a notification payload.
	PayloadMaximum = 4096
)

// Pusher delivers a push message to APNs.
//
// It exists so the push pipeline can be exercised without an APNs
// account: the production implementation is apnsPusher, and tests
// substitute a fake. The returned code is the HTTP status APNs replied
// with (200 on success); a transport failure is reported as a non-200
// code together with an error.
type Pusher interface {
	Push(msg *PushMessage) (code int, err error)
}

// apnsPusher is the real APNs client.
//
// It holds a pool of *apns2.Client so concurrent pushes do not serialise
// on a single HTTP/2 connection, mirroring upstream's channel-of-clients
// design.
type apnsPusher struct {
	clients chan *apns2.Client
	// topic is the bundle ID the push is addressed to. It must match
	// the entitlement of the signing key, or APNs rejects the push.
	topic string
}

// apnsConfig holds the identifiers an APNs client is built with.
type apnsConfig struct {
	topic  string
	keyID  string
	teamID string
	// privateKey is the PEM-encoded .p8 content. Empty means "use the
	// embedded Bark key".
	privateKey string
	// maxClients caps the size of the client pool.
	maxClients int
}

// apnsDialTLSContext dials a TLS connection for the APNs transport.
//
// It mirrors apns2.DialTLS, which is a plain tls.Dial with a bounded
// dial timeout and TCP keep-alive. The standard library's
// http.Transport.DialTLSContext is used instead of the deprecated
// x/net/http2 dial hook, so the APNs client needs no x/net dependency.
//
// The transport's own TLSClientConfig is applied by the caller (the
// http.Transport does it when DialTLSContext is unset), so this hook
// only has to establish the connection; the config is taken from the
// transport through the closure.
func apnsDialTLSContext(cfg *tls.Config) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			Timeout:   apns2.TLSDialTimeout,
			KeepAlive: apns2.TCPKeepAlive,
		}
		return (&tls.Dialer{NetDialer: dialer, Config: cfg}).DialContext(ctx, network, addr)
	}
}

// newAPNSPusher builds the client pool for the given configuration.
func newAPNSPusher(cfg apnsConfig) (*apnsPusher, error) {
	if cfg.topic == "" {
		return nil, errors.New("bark: apns topic is required")
	}
	if cfg.keyID == "" {
		return nil, errors.New("bark: apns key id is required")
	}
	if cfg.teamID == "" {
		return nil, errors.New("bark: apns team id is required")
	}
	if cfg.maxClients < 1 {
		return nil, errors.New("bark: invalid number of apns clients")
	}

	keyPEM := cfg.privateKey
	if keyPEM == "" {
		keyPEM = apnsPrivateKey
	}

	authKey, err := token.AuthKeyFromBytes([]byte(keyPEM))
	if err != nil {
		return nil, errors.Wrap(err, "bark: failed to create apns auth key")
	}

	var rootCAs *x509.CertPool
	if runtime.GOOS == "windows" {
		rootCAs = x509.NewCertPool()
	} else {
		rootCAs, err = x509.SystemCertPool()
		if err != nil {
			return nil, errors.Wrap(err, "bark: failed to get root cas")
		}
	}

	for _, ca := range apnsCAs {
		rootCAs.AppendCertsFromPEM([]byte(ca))
	}

	clients := make(chan *apns2.Client, cfg.maxClients)
	for i := 0; i < min(runtime.NumCPU(), cfg.maxClients); i++ {
		protocols := &http.Protocols{}
		protocols.SetHTTP2(true)

		tlsConfig := &tls.Config{
			RootCAs: rootCAs,
		}

		client := &apns2.Client{
			Token: &token.Token{
				AuthKey: authKey,
				KeyID:   cfg.keyID,
				TeamID:  cfg.teamID,
			},
			HTTPClient: &http.Client{
				// The standard library's HTTP/2 support is used
				// rather than x/net/http2's Transport, which is
				// deprecated in favour of http.Transport.
				//
				// APNs is HTTP/2 over TLS only, so the protocol
				// set is pinned to HTTP2 and the TLS dialer
				// mirrors apns2.DialTLS (a plain tls.Dial with a
				// 20s dial timeout and keep-alive).
				Transport: &http.Transport{
					Protocols:       protocols,
					TLSClientConfig: tlsConfig,
					DialTLSContext:  apnsDialTLSContext(tlsConfig),
				},
				Timeout: apns2.HTTPClientTimeout,
			},
			Host: apns2.HostProduction,
		}
		clients <- client
	}

	return &apnsPusher{clients: clients, topic: cfg.topic}, nil
}

// Push sends the message to APNs.
//
// The payload is built exactly as upstream builds it: a mutable-content
// alert with the title, subtitle, body, sound and category, plus every
// extension parameter copied into the payload as a custom field with a
// lower-cased name. A "delete" message becomes a background push with
// content-available instead of an alert.
func (p *apnsPusher) Push(msg *PushMessage) (code int, err error) {
	pl := payload.NewPayload().MutableContent()
	pushType := apns2.PushTypeAlert
	if msg.IsDelete() {
		// Silent push notification
		pl = pl.ContentAvailable()
		pushType = apns2.PushTypeBackground
	} else {
		// Regular push notification
		pl = pl.AlertTitle(msg.Title).
			AlertSubtitle(msg.Subtitle).
			AlertBody(msg.Body).
			Sound(msg.Sound).
			Category("myNotificationCategory")
		group, exist := msg.ExtParams["group"]
		if exist {
			// Upstream asserts the group is a string; a non-string
			// value would panic there, so it is formatted instead.
			pl = pl.ThreadID(fmt.Sprintf("%v", group))
		}
	}

	for k, v := range msg.ExtParams {
		// Change all parameter names to lowercase to prevent inconsistent capitalization
		pl.Custom(strings.ToLower(k), fmt.Sprintf("%v", v))
	}

	client := <-p.clients // grab a client from the pool
	p.clients <- client   // add the client back to the pool

	resp, err := client.Push(&apns2.Notification{
		CollapseID:  msg.Id,
		DeviceToken: msg.DeviceToken,
		Topic:       p.topic,
		Payload:     pl,
		Expiration:  time.Now().Add(24 * time.Hour),
		PushType:    pushType,
	})
	if err != nil {
		return 500, err
	}
	if resp.StatusCode != 200 {
		return resp.StatusCode, errors.New(resp.Reason)
	}
	return 200, nil
}
