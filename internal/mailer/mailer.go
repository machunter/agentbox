// Package mailer delivers agentbox's daily digest to the user by email (SMTP).
// It reuses the IMAP credentials — the same Gmail/Workspace app password
// authorizes SMTP — and sends to the user's own address by default. It's a
// self-addressed delivery channel, not a general "send mail to anyone" tool, so
// it stays within the project's read-only-for-arbitrary-mail stance.
package mailer

import (
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Config holds SMTP settings. From/To default to the IMAP user (deliver to self).
type Config struct {
	Host, Port, User, Pass, From, To string
}

// LoadConfig reads SMTP settings from the environment, reusing the IMAP user/
// password. The second return value is false unless SMTP is fully configured.
func LoadConfig() (Config, bool) {
	c := Config{
		Host: strings.TrimSpace(os.Getenv("AGENTBOX_SMTP_HOST")),
		Port: strings.TrimSpace(os.Getenv("AGENTBOX_SMTP_PORT")),
		User: strings.TrimSpace(os.Getenv("AGENTBOX_IMAP_USER")),
		Pass: os.Getenv("AGENTBOX_IMAP_PASS"),
		To:   strings.TrimSpace(os.Getenv("AGENTBOX_DELIVER_EMAIL")),
	}
	if c.Port == "" {
		c.Port = "587"
	}
	c.From = c.User
	if c.To == "" {
		c.To = c.User // deliver to self
	}
	configured := c.Host != "" && c.User != "" && c.Pass != "" && c.To != ""
	return c, configured
}

// Configured reports whether email delivery is set up.
func Configured() bool {
	_, ok := LoadConfig()
	return ok
}

// Mailer sends plain-text digests over SMTP.
type Mailer struct{ cfg Config }

func New(cfg Config) *Mailer { return &Mailer{cfg: cfg} }

// Deliver sends a plain-text email with subject and body to the configured
// recipient. SMTP STARTTLS + auth is handled by smtp.SendMail (port 587).
func (m *Mailer) Deliver(subject, body string) error {
	c := m.cfg
	auth := smtp.PlainAuth("", c.User, c.Pass, c.Host)
	msg := buildMessage(c.From, c.To, subject, body, time.Now())
	if err := smtp.SendMail(c.Host+":"+c.Port, auth, c.From, []string{c.To}, msg); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

// buildMessage renders an RFC 822 message. The subject is flattened to one line
// (header-injection safe); the body is CRLF-normalized.
func buildMessage(from, to, subject, body string, now time.Time) []byte {
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
	b.WriteString(strings.ReplaceAll(strings.TrimRight(body, "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}
