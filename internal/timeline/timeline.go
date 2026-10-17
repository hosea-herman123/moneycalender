// Package timeline defines the shared item shape rendered on the calendar page.
package timeline

import "time"

type Kind string

const (
	KindDeadline Kind = "deadline" // money going out (red)
	KindInvoice  Kind = "invoice"  // money coming in (green)
)

type Item struct {
	Date     time.Time
	Kind     Kind
	SourceID string
	Title    string
	Amount   int64 // KES, 0 if unknown
	Agency   string
	Note     string
	Penalty  string
	Link     string
	Verified bool
}

// Provider returns extra timeline items for a business between from and to
// (inclusive), such as unpaid invoices.
type Provider func(businessID int64, from, to time.Time) ([]Item, error)
