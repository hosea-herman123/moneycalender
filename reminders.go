package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

const remindDaysAhead = 3

var errNotConfigured = errors.New("email is not configured")

type Reminder struct {
	Subject string
	Body    string
	Count   int
}

type RemindersPage struct {
	Reminder   Reminder
	Configured bool
	Result     string
	Error      string
	Email      string
}

func setupReminders() {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS reminder_log (
		sent_on TEXT PRIMARY KEY
	)`)
	if err != nil {
		log.Fatal(err)
	}
}

// buildReminder collects everything due soon and everything overdue.
func buildReminder() (Reminder, error) {
	now := time.Now()
	today := time.Date(
		now.Year(), now.Month(), now.Day(),
		0, 0, 0, 0, now.Location(),
	)
	limit := today.AddDate(0, 0, remindDaysAhead)
	todayText := today.Format(dateLayout)
	limitText := limit.Format(dateLayout)

	var dueSoon, overdue []string

	obligations, err := getObligations()
	if err != nil {
		return Reminder{}, err
	}

	for _, o := range obligations {
		for _, d := range o.occurrences(today, limit) {
			line := fmt.Sprintf(
				"- %s (%s) is due %s",
				o.Name, o.Agency, d.Format("Mon 2 Jan"),
			)
			if o.Amount > 0 {
				line += fmt.Sprintf(", about KES %d", o.Amount)
			}
			dueSoon = append(dueSoon, line)
		}
	}

	invs, err := getInvoices()
	if err != nil {
		return Reminder{}, err
	}

	for _, inv := range invs {
		if inv.PaidDate != "" {
			continue
		}

		if inv.Status == "Overdue" {
			due, err := time.ParseInLocation(
				dateLayout, inv.DueDate, today.Location(),
			)
			if err != nil {
				continue
			}

			days := int(today.Sub(due).Hours() / 24)
			overdue = append(overdue, fmt.Sprintf(
				"- %s owes KES %d, %d days overdue (due %s)",
				inv.Client, inv.Amount, days, inv.DueDate,
			))
		} else if inv.DueDate >= todayText && inv.DueDate <= limitText {
			dueSoon = append(dueSoon, fmt.Sprintf(
				"- Invoice to %s for KES %d is due %s",
				inv.Client, inv.Amount, inv.DueDate,
			))
		}
	}

	var b strings.Builder
	b.WriteString(
		"Money Calendar reminder for " +
			today.Format("Mon 2 Jan 2006") + "\n",
	)

	if len(dueSoon) > 0 {
		b.WriteString(fmt.Sprintf(
			"\nDue in the next %d days:\n", remindDaysAhead,
		))
		b.WriteString(strings.Join(dueSoon, "\n") + "\n")
	}

	if len(overdue) > 0 {
		b.WriteString("\nOverdue invoices to chase:\n")
		b.WriteString(strings.Join(overdue, "\n") + "\n")
	}

	count := len(dueSoon) + len(overdue)

	return Reminder{
		Subject: fmt.Sprintf(
			"Money Calendar: %d item(s) need attention", count,
		),
		Body:  b.String(),
		Count: count,
	}, nil
}

// SMTP configuration does not depend on a fixed recipient address.
func smtpConfigured() bool {
	return os.Getenv("SMTP_HOST") != "" &&
		os.Getenv("SMTP_USER") != "" &&
		os.Getenv("SMTP_PASS") != ""
}

// sendEmail keeps the existing automatic-reminder behavior.
// Automatic reminders use REMIND_TO from the environment.
func sendEmail(subject, body string) error {
	return sendEmailTo(os.Getenv("REMIND_TO"), subject, body)
}

// sendEmailTo sends the message to the address supplied by the caller.
func sendEmailTo(to, subject, body string) error {
	if !smtpConfigured() {
		return errNotConfigured
	}

	to = strings.TrimSpace(to)
	parsed, err := mail.ParseAddress(to)
	if err != nil || parsed.Address != to {
		return errors.New("enter a valid recipient email address")
	}

	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}

	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")

	message := "From: " + user + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n")

	conn, err := net.DialTimeout(
		"tcp", net.JoinHostPort(host, port), 15*time.Second,
	)
	if err != nil {
		return fmt.Errorf(
			"cannot reach %s:%s (%v)", host, port, err,
		)
	}

	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return fmt.Errorf("could not set email connection timeout: %w", err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("connected but no mail greeting: %v", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("TLS failed: %v", err)
		}
	}

	if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
		return fmt.Errorf("login failed: %v", err)
	}

	if err := c.Mail(user); err != nil {
		return fmt.Errorf("sender rejected: %v", err)
	}

	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("recipient rejected: %v", err)
	}

	w, err := c.Data()
	if err != nil {
		return err
	}

	if _, err := w.Write([]byte(message)); err != nil {
		w.Close()
		return err
	}

	if err := w.Close(); err != nil {
		return err
	}

	return c.Quit()
}

// runReminders preserves automatic reminders using REMIND_TO.
func runReminders(force bool) (string, error) {
	return runRemindersTo(force, strings.TrimSpace(os.Getenv("REMIND_TO")))
}

// runRemindersTo sends a reminder to the specified recipient.
// Automatic reminders respect the once-a-day rule and 8am start time.
// Manual sends can be triggered at any time.
func runRemindersTo(force bool, recipient string) (string, error) {
	recipient = strings.TrimSpace(recipient)

	if recipient == "" {
		return "", errors.New("enter the email address that should receive the reminder")
	}

	parsed, err := mail.ParseAddress(recipient)
	if err != nil || parsed.Address != recipient {
		return "", errors.New("enter a valid recipient email address")
	}

	today := time.Now().Format(dateLayout)

	if !force {
		if time.Now().Hour() < 8 {
			return "too early, waiting until 8am", nil
		}

		var n int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM reminder_log WHERE sent_on = ?",
			today,
		).Scan(&n)
		if err != nil {
			return "", err
		}

		if n > 0 {
			return "already sent today", nil
		}
	}

	rem, err := buildReminder()
	if err != nil {
		return "", err
	}

	if rem.Count == 0 {
		return "nothing needs attention today", nil
	}

	err = sendEmailTo(recipient, rem.Subject, rem.Body)
	if errors.Is(err, errNotConfigured) {
		log.Println("Email is not configured. Reminder content:\n" + rem.Body)
		return "", errNotConfigured
	}
	if err != nil {
		return "", err
	}

	// Preserve the existing daily log behavior for automatic sends.
	// Manual sends do not consume the automatic daily reminder.
	if !force {
		_, err = db.Exec(
			"INSERT OR IGNORE INTO reminder_log (sent_on) VALUES (?)",
			today,
		)
		if err != nil {
			return "", err
		}
	}

	return "Reminder emailed to " + recipient, nil
}

// startReminders launches the automatic reminder loop.
func startReminders() {
	log.Println("reminder loop started")

	go func() {
		for {
			msg, err := runReminders(false)
			if err != nil {
				log.Println("reminder error:", err)
			} else {
				log.Println("reminders:", msg)
			}

			time.Sleep(6 * time.Hour)
		}
	}()
}

func remindersPage(w http.ResponseWriter, r *http.Request) {
	page := RemindersPage{
		Configured: smtpConfigured(),
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			page.Error = "Could not read the form. Please try again."
		} else {
			page.Email = strings.TrimSpace(r.FormValue("email"))

			msg, err := runRemindersTo(true, page.Email)
			if err != nil {
				if errors.Is(err, errNotConfigured) {
					page.Error = "Email is not configured. Check your SMTP settings."
				} else {
					page.Error = err.Error()
				}
			} else {
				page.Result = msg
			}
		}
	}

	rem, err := buildReminder()
	if err != nil {
		http.Error(w, "Could not build reminder", http.StatusInternalServerError)
		return
	}

	page.Reminder = rem
	render(w, "reminders.html", page)
}
