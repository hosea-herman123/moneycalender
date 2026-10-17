package web

import (
	"testing"
	"time"

	"kalenda/internal/rules"
	"kalenda/internal/store"
)

func TestNewParsesApplicationPages(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rs, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, rs, time.UTC); err != nil {
		t.Fatal(err)
	}
}
