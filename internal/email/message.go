package email

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Message represents one email envelope.
type Message struct {
	// To is the recipient list.
	To []string
	// Cc is the carbon-copy list.
	Cc []string
	// Bcc is the blind carbon-copy list.
	Bcc []string
	// Subject is the message subject.
	Subject string
	// Body is the message body. Treated as plain text unless
	// IsHTML is true.
	Body string
	// IsHTML selects the Content-Type header.
	IsHTML bool
	// ReplyTo overrides the SMTP envelope's reply-to.
	ReplyTo string
}

// Validate returns an error when the message is missing fields
// the SMTP protocol requires to be present.
func (m *Message) Validate() error {
	if len(m.To) == 0 {
		return errors.New("at least one recipient is required")
	}
	if m.Subject == "" {
		return errors.New("subject is required")
	}
	if m.Body == "" {
		return errors.New("body is required")
	}
	return nil
}

// Format returns the RFC 5322 wire format for the message,
// with fromName and fromEmail baked into the From header and
// header values sanitised to defeat header-injection. The
// result is what gets written to the SMTP DATA stream.
func (m *Message) Format(fromEmail, fromName string) string {
	var sb strings.Builder
	fromEmail = sanitizeEmailHeaderValue(fromEmail)
	fromName = sanitizeEmailHeaderValue(fromName)
	to := sanitizeEmailHeaderValues(m.To)
	cc := sanitizeEmailHeaderValues(m.Cc)
	replyTo := sanitizeEmailHeaderValue(m.ReplyTo)
	subject := sanitizeEmailHeaderValue(m.Subject)

	if fromName != "" {
		fmt.Fprintf(&sb, "From: %s <%s>\r\n", fromName, fromEmail)
	} else {
		fmt.Fprintf(&sb, "From: %s\r\n", fromEmail)
	}
	fmt.Fprintf(&sb, "To: %s\r\n", strings.Join(to, ", "))
	if len(cc) > 0 {
		fmt.Fprintf(&sb, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	if replyTo != "" {
		fmt.Fprintf(&sb, "Reply-To: %s\r\n", replyTo)
	}
	fmt.Fprintf(&sb, "Subject: %s\r\n", subject)
	fmt.Fprintf(&sb, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	sb.WriteString("MIME-Version: 1.0\r\n")
	if m.IsHTML {
		sb.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	} else {
		sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	}
	sb.WriteString("\r\n")
	sb.WriteString(m.Body)
	return sb.String()
}

// sanitizeEmailHeaderValue strips CR/LF and folds runs of
// whitespace to a single space. This is the minimum needed to
// keep an attacker-controlled subject or display name from
// breaking out of the header line.
func sanitizeEmailHeaderValue(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func sanitizeEmailHeaderValues(values []string) []string {
	sanitized := make([]string, 0, len(values))
	for _, value := range values {
		sanitized = append(sanitized, sanitizeEmailHeaderValue(value))
	}
	return sanitized
}

// GetAllRecipients returns To+Cc+Bcc as one slice — the order
// the SMTP Rcpt command should be issued in.
func (m *Message) GetAllRecipients() []string {
	var recipients []string
	recipients = append(recipients, m.To...)
	recipients = append(recipients, m.Cc...)
	recipients = append(recipients, m.Bcc...)
	return recipients
}
