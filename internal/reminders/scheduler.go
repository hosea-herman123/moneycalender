// Package reminders owns the reminders table and the scheduler loop.
//
// Sender implementations include a log-only fallback and the SMTP sender.
package reminders

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"kalenda/internal/rules"
)

// Reminder is one row of the reminders table, ready to send.
type Reminder struct {
	ID         int64
	BusinessID int64
	SendAt     time.Time
	Channel    string // "email" for now
	To         string
	Subject    string
	Body       string
}

// Sender delivers a reminder. Return an error to mark it failed.
type Sender interface {
	Send(ctx context.Context, r Reminder) error
}

// LogSender logs reminders when SMTP is not configured.
type LogSender struct{}

func (LogSender) Send(_ context.Context, r Reminder) error {
	log.Printf("[reminder #%d via %s] to=%s subject=%q\n%s", r.ID, r.Channel, r.To, r.Subject, r.Body)
	return nil
}

// Run polls for due reminders every `every` until ctx is cancelled. The
// database is the source of truth, so a restart loses nothing.
func Run(ctx context.Context, db *sql.DB, sender Sender, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := processDue(ctx, db, sender, time.Now()); err != nil {
			log.Printf("scheduler: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func processDue(ctx context.Context, db *sql.DB, sender Sender, now time.Time) error {
	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(business_id, 0), send_at, channel, to_addr, subject, body
		FROM reminders
		WHERE status = 'pending' AND send_at <= ?
		ORDER BY send_at LIMIT 50`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	var due []Reminder
	for rows.Next() {
		var r Reminder
		var sendAt string
		if err := rows.Scan(&r.ID, &r.BusinessID, &sendAt, &r.Channel, &r.To, &r.Subject, &r.Body); err != nil {
			rows.Close()
			return err
		}
		r.SendAt, _ = time.Parse(time.RFC3339, sendAt)
		due = append(due, r)
	}
	rows.Close() // must close before issuing more queries (single connection)
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range due {
		if err := sender.Send(ctx, r); err != nil {
			log.Printf("scheduler: reminder %d failed: %v", r.ID, err)
			if _, updateErr := db.ExecContext(ctx, `UPDATE reminders SET status='failed', error=? WHERE id=?`, err.Error(), r.ID); updateErr != nil {
				return fmt.Errorf("mark reminder %d failed: %w", r.ID, updateErr)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, `UPDATE reminders SET status='sent', sent_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339), r.ID); err != nil {
			return fmt.Errorf("mark reminder %d sent: %w", r.ID, err)
		}
	}
	return nil
}

const horizonDays = 90

// ScheduleForBusiness creates 7-day and 1-day email reminders (08:00 Nairobi
// time) for every deadline in the next 90 days. Safe to call repeatedly:
// dedupe_key stops duplicates.
func ScheduleForBusiness(db *sql.DB, rs []rules.Rule, bizID int64, email string, b rules.Business, loc *time.Location, now time.Time) error {
	now = now.In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, horizonDays)

	for _, d := range rules.Deadlines(rs, b, from, to) {
		for _, offset := range []int{7, 1} {
			sendAt := time.Date(d.Date.Year(), d.Date.Month(), d.Date.Day()-offset, 8, 0, 0, 0, loc)
			if sendAt.Before(now) {
				continue
			}
			key := fmt.Sprintf("deadline:%d:%s:%s:%d", bizID, d.Rule.ID, d.Date.Format("2006-01-02"), offset)
			subject, body := deadlineEmail(d, offset)
			_, err := db.Exec(`
				INSERT OR IGNORE INTO reminders(business_id, send_at, channel, to_addr, subject, body, dedupe_key)
				VALUES(?, ?, 'email', ?, ?, ?, ?)`,
				bizID, sendAt.UTC().Format(time.RFC3339), email, subject, body, key)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// TopUp schedules reminders for every business. Run it at start-up and every
// few hours so reminders keep appearing as the 90-day window moves forward.
func TopUp(ctx context.Context, db *sql.DB, rs []rules.Rule, loc *time.Location) error {
	rows, err := db.QueryContext(ctx, `
		SELECT b.id, u.email, b.business_type, b.county, b.vat_registered, b.has_employees
		FROM businesses b JOIN users u ON u.id = b.user_id`)
	if err != nil {
		return err
	}
	type biz struct {
		id    int64
		email string
		b     rules.Business
	}
	var all []biz
	for rows.Next() {
		var x biz
		var vat, emp int
		if err := rows.Scan(&x.id, &x.email, &x.b.Type, &x.b.County, &vat, &emp); err != nil {
			rows.Close()
			return err
		}
		x.b.ID, x.b.VATRegistered, x.b.HasEmployees = x.id, vat == 1, emp == 1
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range all {
		if err := ScheduleForBusiness(db, rs, x.id, x.email, x.b, loc, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func deadlineEmail(d rules.Deadline, offset int) (subject, body string) {
	when := fmt.Sprintf("in %d days", offset)
	if offset == 1 {
		when = "tomorrow"
	}
	date := d.Date.Format("Monday 2 January 2006")
	subject = fmt.Sprintf("Reminder: %s is due %s", d.Rule.Name, when)
	body = fmt.Sprintf("%s (%s) is due on %s.\n\n%s\n\nIf you pay late: %s\n\nOfficial source: %s\n\n",
		d.Rule.Name, d.Rule.Agency, date, d.Rule.Note, d.Rule.Penalty, d.Rule.Link)
	if !d.Rule.Verified() {
		body += "Note: we have not finished verifying this date. Please confirm it with the official source.\n\n"
	}
	body += "Kalenda is a reminder tool, not professional advice."
	return subject, body
}
