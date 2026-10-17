package invoices

import (
	"database/sql"
	"testing"
	"time"

	"kalenda/internal/store"
)

func invoiceFixture(t *testing.T) (*sql.DB, *Store, int64, int64) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, email := range []string{"one@example.test", "two@example.test"} {
		if _, err := db.Exec(`INSERT INTO users(email,password_hash) VALUES(?, 'unused')`, email); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{1, 2} {
		if _, err := db.Exec(`INSERT INTO businesses(user_id,name,business_type,county) VALUES(?,?,?,?)`, id, "Business", "Other", "Nairobi"); err != nil {
			t.Fatal(err)
		}
	}
	return db, New(db), 1, 2
}

func TestInvoicesAreScopedToBusiness(t *testing.T) {
	_, s, first, second := invoiceFixture(t)
	if err := s.Create(first, "Acme", "billing@acme.test", "12500", "2026-10-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(first, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || len(list) != 1 || list[0].Client != "Acme" {
		t.Fatalf("first business invoice list = %#v, error %v", list, err)
	}
	other, err := s.List(second, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || len(other) != 0 {
		t.Fatalf("second business saw another business's invoice: %#v, error %v", other, err)
	}
	if err := s.MarkPaid(second, list[0].ID, "2026-10-05"); err != sql.ErrNoRows {
		t.Fatalf("cross-business payment update error = %v, want sql.ErrNoRows", err)
	}
}

func TestInvoiceValidationAndTimeline(t *testing.T) {
	_, s, first, _ := invoiceFixture(t)
	if err := s.Create(first, "Acme", "", "100", "2026-10-10", "2026-10-09"); err != ErrInvalid {
		t.Fatalf("invalid date range error = %v, want ErrInvalid", err)
	}
	if err := s.Create(first, "Acme", "", "100", "2026-10-01", "2026-10-02"); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	items, err := s.Timeline(first, from, from.AddDate(0, 0, 90))
	if err != nil || len(items) != 1 || !items[0].Date.Equal(from) || items[0].Amount != 100 {
		t.Fatalf("overdue invoice timeline = %#v, error %v", items, err)
	}
}
