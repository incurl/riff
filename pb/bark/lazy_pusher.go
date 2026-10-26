// lazy_pusher.go creates the real APNs client on first use.
//
// The client is not built at registration time because building it
// parses the signing key and opens the system trust store — work an
// instance that never receives a push should not pay for at startup, and
// a misconfigured key should surface as a push error rather than as a
// server that refuses to boot.
package bark

import (
	"sync"
)

// lazyPusher defers the construction of the APNs client pool to the
// first push.
//
// The result (client or error) is cached, so a broken key is reported
// identically to every later push instead of retrying the setup.
type lazyPusher struct {
	opts Options

	once   sync.Once
	pusher Pusher
	err    error
}

// Push delivers the message, building the client pool on first use.
func (l *lazyPusher) Push(msg *PushMessage) (int, error) {
	l.once.Do(func() {
		maxClients := l.opts.APNsMaxClients
		if maxClients < 1 {
			maxClients = 1
		}
		l.pusher, l.err = newAPNSPusher(apnsConfig{
			topic:      firstNonEmpty(l.opts.APNsTopic, defaultTopic),
			keyID:      firstNonEmpty(l.opts.APNsKeyID, defaultKeyID),
			teamID:     firstNonEmpty(l.opts.APNsTeamID, defaultTeamID),
			privateKey: l.opts.APNsPrivateKey,
			maxClients: maxClients,
		})
	})
	if l.err != nil {
		return 500, l.err
	}
	return l.pusher.Push(msg)
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
