package common

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// SMTPConfig describes how to reach the SMTP server.
// Port 465 uses implicit TLS; otherwise StartTLS selects STARTTLS vs plaintext.
type SMTPConfig struct {
	Host     string
	Port     int
	StartTLS bool
	User     string
	Password string
}

// plainAuth performs AUTH PLAIN without net/smtp's TLS-only restriction,
// matching aiosmtplib which also logs in over plaintext connections.
type plainAuth struct{ user, password string }

func (a plainAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("unexpected SMTP auth challenge")
	}
	return nil, nil
}

func SendMail(cfg SMTPConfig, from, to string, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	tlsConfig := &tls.Config{ServerName: cfg.Host}

	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if cfg.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer client.Close()

	if cfg.Port != 465 && cfg.StartTLS {
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if cfg.User != "" {
		if err := client.Auth(plainAuth{cfg.User, cfg.Password}); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO failed: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP data close failed: %w", err)
	}
	return client.Quit()
}

func FormatAddress(name, addr string) string {
	return fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", name), addr)
}

func writeHeaders(buf *bytes.Buffer, subject, from, to string) {
	fmt.Fprintf(buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(buf, "From: %s\r\n", from)
	fmt.Fprintf(buf, "To: %s\r\n", to)
	buf.WriteString("MIME-Version: 1.0\r\n")
}

// BuildPlainMessage builds a text/plain message.
func BuildPlainMessage(subject, from, to, body string) []byte {
	var buf bytes.Buffer
	writeHeaders(&buf, subject, from, to)
	buf.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
	buf.WriteString(body)
	return buf.Bytes()
}

// BuildAlternativeMessage builds a multipart/alternative message with text and HTML parts.
func BuildAlternativeMessage(subject, from, to, textBody, htmlBody string) []byte {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{
		{"text/plain; charset=\"utf-8\"", textBody},
		{"text/html; charset=\"utf-8\"", htmlBody},
	} {
		w, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.ctype}})
		w.Write([]byte(strings.ReplaceAll(part.content, "\r\n", "\n")))
	}
	mw.Close()

	var buf bytes.Buffer
	writeHeaders(&buf, subject, from, to)
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary())
	buf.Write(body.Bytes())
	return buf.Bytes()
}
