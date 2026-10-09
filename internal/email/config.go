package email

import (
	"fmt"

	"github.com/pkg/errors"
)

// Config represents the SMTP configuration for email sending.
// These settings are provided by the instance administrator via
// the InstanceSetting.NotificationSetting.EmailSetting proto
// (see pb/hooks/notifications_email.go for the read side).
type Config struct {
	// SMTPHost is the SMTP server hostname (e.g., "smtp.gmail.com").
	SMTPHost string
	// SMTPPort is the SMTP server port (587 for STARTTLS, 465 for SSL).
	SMTPPort int
	// SMTPUsername is the SMTP authentication username.
	SMTPUsername string
	// SMTPPassword is the SMTP authentication password or app-specific password.
	SMTPPassword string
	// FromEmail is the address that will appear in the "From" header.
	FromEmail string
	// FromName is the display name in the "From" header.
	FromName string
	// UseTLS enables STARTTLS on the unencrypted SMTP port.
	UseTLS bool
	// UseSSL enables implicit TLS (port 465).
	UseSSL bool
}

// Validate returns an error when the config is missing the
// fields required to actually open a connection. FromEmail is
// the only one we strictly need to send a message; the other
// failures are caught by the Client, but Validate gives the
// admin's settings page a precise error before the network is
// touched.
func (c *Config) Validate() error {
	if c.SMTPHost == "" {
		return errors.New("SMTP host is required")
	}
	if c.SMTPPort <= 0 || c.SMTPPort > 65535 {
		return errors.New("SMTP port must be between 1 and 65535")
	}
	if c.FromEmail == "" {
		return errors.New("from email is required")
	}
	return nil
}

// GetServerAddress returns the SMTP server address in the
// "host:port" form expected by net.Dial.
func (c *Config) GetServerAddress() string {
	return fmt.Sprintf("%s:%d", c.SMTPHost, c.SMTPPort)
}
