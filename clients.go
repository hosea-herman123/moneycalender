package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

type MessagePage struct {
	Heading string
	Message string
	IsError bool
	Back    string
}

// dbPath lets Docker choose where the database file lives.
func dbPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "moneycalendar.db"
}

func setupClientReminders() {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS client_emails (
			client TEXT PRIMARY KEY,
			email TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS client_reminder_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			invoice_id INTEGER NOT NULL,
			sent_on TEXT NOT NULL
		)`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			log.Fatal(err)
		}
	}
}

func showMessage(w http.ResponseWriter, heading, message string, isError bool) {
	render(w, "message.html", MessagePage{
		Heading: heading,
		Message: message,
		IsError: isError,
		Back:    "/invoices",
	})
}

func remindClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/invoices", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid invoice", http.StatusBadRequest)
		return
	}

	addr, err := mail.ParseAddress(strings.TrimSpace(r.FormValue("email")))
	if err != nil {
		showMessage(w, "Could not send", "That email address is not valid.", true)
		return
	}
	email := addr.Address

	inv, err := getInvoice(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayText := today.Format(dateLayout)

	due, err := time.ParseInLocation(dateLayout, inv.DueDate, today.Location())
	if inv.PaidDate != "" || err != nil || !due.Before(today) {
		showMessage(w, "Could not send", "Only unpaid, overdue invoices can be reminded.", true)
		return
	}

	var already int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM client_reminder_log WHERE invoice_id = ? AND sent_on = ?", id, todayText,
	).Scan(&already)
	if err != nil {
		http.Error(w, "Could not check history", http.StatusInternalServerError)
		return
	}
	if already > 0 {
		showMessage(w, "Already sent", "A reminder for this invoice was already sent today.", true)
		return
	}

	// Remember this client's email for next time.
	_, err = db.Exec(
		`INSERT INTO client_emails (client, email) VALUES (?, ?)
		 ON CONFLICT(client) DO UPDATE SET email = excluded.email`,
		inv.Client, email,
	)
	if err != nil {
		http.Error(w, "Could not save email", http.StatusInternalServerError)
		return
	}

	days := int(today.Sub(due).Hours() / 24)
	business := os.Getenv("BUSINESS_NAME")
	if business == "" {
		business = "Your supplier"
	}
	clean := strings.NewReplacer("\r", " ", "\n", " ").Replace

	subject := fmt.Sprintf("Friendly reminder: invoice of KES %d", inv.Amount)
	body := fmt.Sprintf(
		"Hello %s,\n\n"+
			"This is a friendly reminder that our invoice for KES %d, due on %s, is now %d days overdue.\n\n"+
			"If you have already paid, please ignore this message and accept our thanks. "+
			"Otherwise, we would appreciate payment at your earliest convenience.\n\n"+
			"Thank you,\n%s\n",
		clean(inv.Client), inv.Amount, inv.DueDate, days, clean(business),
	)

	err = sendEmailTo(email, subject, body)
	if errors.Is(err, errNotConfigured) {
		showMessage(w, "Email not set up", "Start the app with run.bat or Docker so the email settings are loaded.", true)
		return
	}
	if err != nil {
		showMessage(w, "Could not send", err.Error(), true)
		return
	}

	_, err = db.Exec("INSERT INTO client_reminder_log (invoice_id, sent_on) VALUES (?, ?)", id, todayText)
	if err != nil {
		log.Println("could not log reminder:", err)
	}

	showMessage(w, "Reminder sent", "A reminder was emailed to "+email+".", false)
}