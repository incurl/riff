package email

import (
	"crypto/tls"
	"net"
	"net/smtp"
	"time"

	"github.com/pkg/errors"
)

// smtpOperationTimeout bounds every step of a single Send call.
// Without it, a misconfigured server that accepts the TCP
// connection but never replies could stall the caller forever.
const smtpOperationTimeout = 15 * time.Second

// Client is the SMTP sender. It is cheap to construct; the
// production code creates one per Send.
type Client struct {
	config *Config
}

// NewClient returns a Client bound to the given Config. The
// config is not validated here — Send does that — so a
// half-filled config is easy to surface in tests.
func NewClient(config *Config) *Client {
	return &Client{config: config}
}

// Send delivers a single message synchronously and returns the
// first error encountered (validation, dial, auth, or
// protocol-level).
func (c *Client) Send(message *Message) error {
	if err := c.validateConfig(); err != nil {
		return errors.Wrap(err, "invalid email configuration")
	}
	if message == nil {
		return errors.New("message is required")
	}
	if err := message.Validate(); err != nil {
		return errors.Wrap(err, "invalid email message")
	}

	body := message.Format(c.config.FromEmail, c.config.FromName)
	recipients := message.GetAllRecipients()
	auth := c.createAuth()

	if c.config.UseSSL {
		return c.sendWithSSL(auth, recipients, body)
	}
	return c.sendWithTLS(auth, recipients, body)
}

func (c *Client) validateConfig() error {
	if c.config == nil {
		return errors.New("email configuration is required")
	}
	return c.config.Validate()
}

func (c *Client) createAuth() smtp.Auth {
	if c.config.SMTPUsername == "" && c.config.SMTPPassword == "" {
		return nil
	}
	return smtp.PlainAuth("", c.config.SMTPUsername, c.config.SMTPPassword, c.config.SMTPHost)
}

func (c *Client) createTLSConfig() *tls.Config {
	return &tls.Config{
		ServerName: c.config.SMTPHost,
		MinVersion: tls.VersionTLS12,
	}
}

func (c *Client) sendWithTLS(auth smtp.Auth, recipients []string, body string) error {
	serverAddr := c.config.GetServerAddress()
	dialer := &net.Dialer{Timeout: smtpOperationTimeout}
	conn, err := dialer.Dial("tcp", serverAddr)
	if err != nil {
		return errors.Wrapf(err, "failed to connect to SMTP server: %s", serverAddr)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(smtpOperationTimeout)); err != nil {
		return errors.Wrap(err, "failed to set SMTP connection deadline")
	}

	client, err := smtp.NewClient(conn, c.config.SMTPHost)
	if err != nil {
		return errors.Wrap(err, "failed to create SMTP client")
	}
	defer client.Quit()

	if c.config.UseTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(c.createTLSConfig()); err != nil {
			return errors.Wrap(err, "failed to start SMTP STARTTLS")
		}
	}

	return c.sendWithClient(client, auth, recipients, body)
}

func (c *Client) sendWithSSL(auth smtp.Auth, recipients []string, body string) error {
	serverAddr := c.config.GetServerAddress()
	tlsConfig := c.createTLSConfig()
	dialer := &net.Dialer{Timeout: smtpOperationTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", serverAddr, tlsConfig)
	if err != nil {
		return errors.Wrapf(err, "failed to connect to SMTP server with SSL: %s", serverAddr)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(smtpOperationTimeout)); err != nil {
		return errors.Wrap(err, "failed to set SMTP connection deadline")
	}

	client, err := smtp.NewClient(conn, c.config.SMTPHost)
	if err != nil {
		return errors.Wrap(err, "failed to create SMTP client")
	}
	defer client.Quit()

	return c.sendWithClient(client, auth, recipients, body)
}

func (c *Client) sendWithClient(client *smtp.Client, auth smtp.Auth, recipients []string, body string) error {
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return errors.Wrap(err, "SMTP authentication failed")
		}
	}
	if err := client.Mail(c.config.FromEmail); err != nil {
		return errors.Wrap(err, "failed to set sender")
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return errors.Wrapf(err, "failed to set recipient: %s", recipient)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.Wrap(err, "failed to send DATA command")
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		return errors.Wrap(err, "failed to write message body")
	}
	if err := writer.Close(); err != nil {
		return errors.Wrap(err, "failed to close message writer")
	}
	return nil
}
