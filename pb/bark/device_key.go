// device_key.go generates the device keys handed out at registration.
//
// Upstream bark-server uses lithammer/shortuuid, which produces a 22
// character URL-safe base64 string (the "short UUID" form: 16 random
// bytes encoded without padding). This package does not take that
// dependency; the same shape is produced with the standard library.
package bark

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/pkg/errors"
)

// deviceKeyBytes is the number of random bytes behind a device key.
//
// 16 bytes is 128 bits of entropy, the same as upstream's shortuuid.
const deviceKeyBytes = 16

// generateDeviceKey returns a new random device key.
//
// The result is a 22 character URL-safe base64 string (standard encoding
// with padding stripped), which is byte-for-byte the same shape as
// upstream's shortuuid output — so a Bark client that validates the key
// format keeps working.
func generateDeviceKey() (string, error) {
	buf := make([]byte, deviceKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(err, "bark: failed to generate device key")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
