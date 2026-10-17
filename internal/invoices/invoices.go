// Package invoices stores per-business client invoices and exposes them to the
// shared calendar timeline.
package invoices

import (
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"kalenda/internal/timeline"
)

const dateLayout = "2006-01-02"

var ErrInvalid = errors.New("invalid invoice")

type Invoice struct {
	ID           int64
	Client       string
	Email        string
	Amount       int64
	IssueDate    string
	DueDate      string
	PaidDate     string
	Status       string
	LastReminded string
}

type ClientScore struct {
	Client      string
	PaidCount   int
	AvgDaysLate int
	Rating      string
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(businessID int64, client, email string, amountText, issueDate, dueDate string) error {
	client = strings.TrimSpace(client)
	amount, err := strconv.ParseInt(strings.TrimSpace(amountText), 10, 64)
	issue, issueErr := time.Parse(dateLayout, issueDate)
	due, dueErr := time.Parse(dateLayout, dueDate)
	if businessID <= 0 || client == "" || len(client) > 120 || amount <= 0 || err != nil || issueErr != nil || dueErr != nil || due.Before(issue) {
		return ErrInvalid
	}
	if email != "" {
		addr, err := mail.ParseAddress(strings.TrimSpace(email))
		if err != nil || addr.Address != strings.TrimSpace(email) || len(email) > 254 {
			return ErrInvalid
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO clients(business_id, name, email) VALUES(?, ?, ?)
		ON CONFLICT(business_id, name) DO UPDATE SET email=CASE WHEN excluded.email <> '' THEN excluded.email ELSE clients.email END`, businessID, client, email)
	if err != nil {
		return err
	}
	var clientID int64
	if err := tx.QueryRow(`SELECT id FROM clients WHERE business_id=? AND name=?`, businessID, client).Scan(&clientID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO invoices(business_id, client_id, amount, issue_date, due_date) VALUES(?, ?, ?, ?, ?)`, businessID, clientID, amount, issueDate, dueDate); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) List(businessID int64, today time.Time) ([]Invoice, error) {
	rows, err := s.db.Query(`SELECT i.id, c.name, c.email, i.amount, i.issue_date, i.due_date, i.paid_date,
		COALESCE((SELECT MAX(l.sent_on) FROM client_reminder_log l WHERE l.invoice_id=i.id), '')
		FROM invoices i JOIN clients c ON c.id=i.client_id
		WHERE i.business_id=? ORDER BY i.due_date, i.id`, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	todayText := today.Format(dateLayout)
	var list []Invoice
	for rows.Next() {
		var inv Invoice
		if err := rows.Scan(&inv.ID, &inv.Client, &inv.Email, &inv.Amount, &inv.IssueDate, &inv.DueDate, &inv.PaidDate, &inv.LastReminded); err != nil {
			return nil, err
		}
		switch {
		case inv.PaidDate != "":
			inv.Status = "Paid"
		case inv.DueDate < todayText:
			inv.Status = "Overdue"
		default:
			inv.Status = "Pending"
		}
		list = append(list, inv)
	}
	return list, rows.Err()
}

// Scores summarizes payment history for each client of one business.
func (s *Store) Scores(businessID int64) ([]ClientScore, error) {
	rows, err := s.db.Query(`SELECT c.name, i.due_date, i.paid_date
		FROM invoices i JOIN clients c ON c.id=i.client_id
		WHERE i.business_id=? AND i.paid_date<>'' ORDER BY c.name`, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type totals struct{ sum, count int }
	byClient := map[string]totals{}
	var names []string
	for rows.Next() {
		var name, dueText, paidText string
		if err := rows.Scan(&name, &dueText, &paidText); err != nil {
			return nil, err
		}
		due, dueErr := time.Parse(dateLayout, dueText)
		paid, paidErr := time.Parse(dateLayout, paidText)
		if dueErr != nil || paidErr != nil {
			continue
		}
		days := int(paid.Sub(due).Hours() / 24)
		t := byClient[name]
		if t.count == 0 {
			names = append(names, name)
		}
		t.sum += days
		t.count++
		byClient[name] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	scores := make([]ClientScore, 0, len(names))
	for _, name := range names {
		t := byClient[name]
		avg := t.sum / t.count
		rating := "Reliable"
		if avg > 14 {
			rating = "Usually late"
		} else if avg > 0 {
			rating = "Slightly late"
		}
		scores = append(scores, ClientScore{Client: name, PaidCount: t.count, AvgDaysLate: avg, Rating: rating})
	}
	return scores, nil
}

func (s *Store) MarkPaid(businessID, invoiceID int64, paidDate string) error {
	if _, err := time.Parse(dateLayout, paidDate); err != nil {
		return ErrInvalid
	}
	res, err := s.db.Exec(`UPDATE invoices SET paid_date=? WHERE id=? AND business_id=?`, paidDate, invoiceID, businessID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// OverdueInvoice returns an unpaid invoice owned by the requested business.
func (s *Store) OverdueInvoice(businessID, invoiceID int64, today time.Time) (Invoice, error) {
	var inv Invoice
	err := s.db.QueryRow(`SELECT i.id, c.name, c.email, i.amount, i.issue_date, i.due_date, i.paid_date
		FROM invoices i JOIN clients c ON c.id=i.client_id
		WHERE i.id=? AND i.business_id=? AND i.paid_date='' AND i.due_date < ?`,
		invoiceID, businessID, today.Format(dateLayout)).Scan(&inv.ID, &inv.Client, &inv.Email, &inv.Amount, &inv.IssueDate, &inv.DueDate, &inv.PaidDate)
	if errors.Is(err, sql.ErrNoRows) {
		return Invoice{}, sql.ErrNoRows
	}
	return inv, err
}

func (s *Store) Delete(businessID, invoiceID int64) error {
	res, err := s.db.Exec(`DELETE FROM invoices WHERE id=? AND business_id=?`, invoiceID, businessID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Timeline returns unpaid invoices due in the requested calendar window.
func (s *Store) Timeline(businessID int64, from, to time.Time) ([]timeline.Item, error) {
	rows, err := s.db.Query(`SELECT c.name, i.amount, i.due_date FROM invoices i
		JOIN clients c ON c.id=i.client_id
		WHERE i.business_id=? AND i.paid_date='' AND i.due_date <= ?
		ORDER BY i.due_date`, businessID, to.Format(dateLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []timeline.Item
	for rows.Next() {
		var client, dueDate string
		var amount int64
		if err := rows.Scan(&client, &amount, &dueDate); err != nil {
			return nil, err
		}
		due, err := time.ParseInLocation(dateLayout, dueDate, from.Location())
		if err != nil {
			return nil, fmt.Errorf("invoice due date %q: %w", dueDate, err)
		}
		note := "Expected payment by " + due.Format("2 Jan 2006") + "."
		if due.Before(from) {
			note = "Overdue invoice; originally due " + due.Format("2 Jan 2006") + "."
			due = from
		}
		items = append(items, timeline.Item{Date: due, Kind: timeline.KindInvoice, SourceID: "invoice", Title: "Invoice: " + client, Amount: amount, Note: note})
	}
	return items, rows.Err()
}
