package reminders

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// SMTPSender sends email using the SMTP credentials supplied in environment
// variables. SMTP_HOST, SMTP_USER and SMTP_PASS are required; SMTP_PORT
// defaults to 587 and SMTP_FROM defaults to SMTP_USER.
type SMTPSender struct {
	Host, Port, Username, Password, From string
}

func SMTPFromEnv() (SMTPSender, bool) {
	s := SMTPSender{
		Host: os.Getenv("SMTP_HOST"), Port: os.Getenv("SMTP_PORT"),
		Username: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASS"), From: os.Getenv("SMTP_FROM"),
	}
	if s.Port == "" {
		s.Port = "587"
	}
	if s.From == "" {
		s.From = s.Username
	}
	return s, s.Host != "" && s.Username != "" && s.Password != ""
}

func SendEmail(ctx context.Context, to, subject, body string) error {
	sender, ok := SMTPFromEnv()
	if !ok {
		return errors.New("SMTP is not configured")
	}
	return sender.Send(ctx, Reminder{To: to, Subject: subject, Body: body})
}

func (s SMTPSender) Send(ctx context.Context, r Reminder) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	to, err := mail.ParseAddress(strings.TrimSpace(r.To))
	if err != nil || to.Address != strings.TrimSpace(r.To) {
		return errors.New("invalid recipient email")
	}
	from, err := mail.ParseAddress(strings.TrimSpace(s.From))
	if err != nil || from.Address != strings.TrimSpace(s.From) {
		return errors.New("invalid sender email")
	}
	if strings.ContainsAny(r.Subject, "\r\n") {
		return errors.New("invalid email subject")
	}
	if s.Port == "" {
		s.Port = "587"
	}
	address := net.JoinHostPort(s.Host, s.Port)
	message := "From: " + from.Address + "\r\n" +
		"To: " + to.Address + "\r\n" +
		"Subject: " + r.Subject + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(strings.ReplaceAll(r.Body, "\r\n", "\n"), "\n", "\r\n")
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to email server: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return fmt.Errorf("set email timeout: %w", err)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("email server greeting: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start email TLS: %w", err)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
		return fmt.Errorf("email server login: %w", err)
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("email sender rejected: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("email recipient rejected: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start email body: %w", err)
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		writer.Close()
		return fmt.Errorf("write email body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish email body: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("close email session: %w", err)
	}
	return nil
}
