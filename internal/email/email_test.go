package email

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "valid",
			config: &Config{
				SMTPHost:     "smtp.example.com",
				SMTPPort:     587,
				SMTPUsername: "user",
				SMTPPassword: "pw",
				FromEmail:    "noreply@example.com",
				FromName:     "Riff",
			},
		},
		{
			name:    "missing host",
			config:  &Config{SMTPPort: 587, FromEmail: "noreply@example.com"},
			wantErr: "SMTP host is required",
		},
		{
			name:    "missing port",
			config:  &Config{SMTPHost: "smtp.example.com", FromEmail: "noreply@example.com"},
			wantErr: "SMTP port must be between 1 and 65535",
		},
		{
			name:    "out-of-range port",
			config:  &Config{SMTPHost: "smtp.example.com", SMTPPort: 70000, FromEmail: "noreply@example.com"},
			wantErr: "SMTP port must be between 1 and 65535",
		},
		{
			name:    "missing from",
			config:  &Config{SMTPHost: "smtp.example.com", SMTPPort: 587},
			wantErr: "from email is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestMessage_Validate(t *testing.T) {
	m := &Message{}
	require.Error(t, m.Validate(), "empty message should fail")
	m = &Message{To: []string{"a@b.com"}, Subject: "hi"}
	require.Error(t, m.Validate(), "missing body should fail")
	m = &Message{To: []string{"a@b.com"}, Body: "x"}
	require.Error(t, m.Validate(), "missing subject should fail")
	m = &Message{Subject: "hi", Body: "x"}
	require.Error(t, m.Validate(), "missing to should fail")
	m = &Message{To: []string{"a@b.com"}, Subject: "hi", Body: "x"}
	require.NoError(t, m.Validate(), "complete message should pass")
}

func TestMessage_Format_StripsHeaderInjection(t *testing.T) {
	m := &Message{
		To:      []string{"a@b.com"},
		Subject: "hi\r\nBcc: attacker@evil.com",
		Body:    "hello",
		ReplyTo: "ok\r\nBcc: another@evil.com",
	}
	out := m.Format("noreply@example.com", "Riff")
	// The injection attempt was a CR/LF that would let a
	// second header line ("Bcc: ...") appear on its own
	// line. The sanitiser folds the CR/LF into a single
	// space, so the result must NOT contain "\r\nBcc:" or
	// "\nBcc:" — a real injection would. The substring
	// "Bcc: attacker" is still allowed to appear inline in
	// the Subject field, which is the safe outcome.
	assert.NotContains(t, out, "\r\nBcc:")
	assert.NotContains(t, out, "\nBcc:")
	assert.Contains(t, out, "Subject: hi Bcc: attacker@evil.com")
}

func TestMessage_GetAllRecipients(t *testing.T) {
	m := &Message{
		To:  []string{"a@b.com"},
		Cc:  []string{"c@b.com", "d@b.com"},
		Bcc: []string{"e@b.com"},
	}
	got := m.GetAllRecipients()
	assert.Equal(t, []string{"a@b.com", "c@b.com", "d@b.com", "e@b.com"}, got)
}
