package rules

import (
	"testing"
	"time"
)

var eat = time.FixedZone("EAT", 3*3600)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, eat) }
func ptr(b bool) *bool                         { return &b }

func TestApplies(t *testing.T) {
	paye := Rule{AppliesIf: Condition{HasEmployees: ptr(true)}}
	vat := Rule{AppliesIf: Condition{VATRegistered: ptr(true)}}
	nairobiOnly := Rule{AppliesIf: Condition{Counties: []string{"Nairobi"}}}

	tests := []struct {
		name string
		rule Rule
		biz  Business
		want bool
	}{
		{"paye needs employees", paye, Business{HasEmployees: false}, false},
		{"paye with employees", paye, Business{HasEmployees: true}, true},
		{"vat needs registration", vat, Business{VATRegistered: false}, false},
		{"vat registered", vat, Business{VATRegistered: true}, true},
		{"county match is case-insensitive", nairobiOnly, Business{County: "nairobi"}, true},
		{"county mismatch", nairobiOnly, Business{County: "Mombasa"}, false},
		{"no conditions applies to all", Rule{}, Business{}, true},
	}
	for _, tc := range tests {
		if got := tc.rule.Applies(tc.biz); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestMonthlyAcrossYearBoundary(t *testing.T) {
	r := Rule{ID: "paye", Name: "PAYE", Frequency: "monthly", DueDay: 9}
	got := Deadlines([]Rule{r}, Business{}, day(2026, 12, 1), day(2027, 1, 31))
	if len(got) != 2 || !got[0].Date.Equal(day(2026, 12, 9)) || !got[1].Date.Equal(day(2027, 1, 9)) {
		t.Fatalf("unexpected deadlines: %+v", got)
	}
}

func TestMonthlyIncludesToday(t *testing.T) {
	r := Rule{ID: "x", Name: "X", Frequency: "monthly", DueDay: 13}
	got := Deadlines([]Rule{r}, Business{}, day(2026, 10, 13), day(2026, 10, 13))
	if len(got) != 1 {
		t.Fatalf("a deadline due today must be included, got %d", len(got))
	}
}

func TestDayClampedToMonthEnd(t *testing.T) {
	r := Rule{ID: "x", Name: "X", Frequency: "monthly", DueDay: 31}
	got := Deadlines([]Rule{r}, Business{}, day(2027, 2, 1), day(2027, 2, 28))
	if len(got) != 1 || !got[0].Date.Equal(day(2027, 2, 28)) {
		t.Fatalf("expected clamp to 28 Feb, got %+v", got)
	}
}

func TestAnnual(t *testing.T) {
	r := Rule{ID: "tax", Name: "Return", Frequency: "annual", DueMonth: 6, DueDay: 30}
	got := Deadlines([]Rule{r}, Business{}, day(2026, 10, 13), day(2027, 7, 31))
	if len(got) != 1 || !got[0].Date.Equal(day(2027, 6, 30)) {
		t.Fatalf("expected only 30 Jun 2027, got %+v", got)
	}
}

func TestSoleTraderWithoutEmployeesOrVAT(t *testing.T) {
	rs := []Rule{
		{ID: "paye", Name: "PAYE", Frequency: "monthly", DueDay: 9, AppliesIf: Condition{HasEmployees: ptr(true)}},
		{ID: "nssf", Name: "NSSF", Frequency: "monthly", DueDay: 15, AppliesIf: Condition{HasEmployees: ptr(true)}},
		{ID: "vat", Name: "VAT", Frequency: "monthly", DueDay: 20, AppliesIf: Condition{VATRegistered: ptr(true)}},
		{ID: "tax", Name: "Return", Frequency: "annual", DueMonth: 6, DueDay: 30},
	}
	got := Deadlines(rs, Business{}, day(2026, 10, 1), day(2027, 12, 31))
	for _, d := range got {
		if d.Rule.ID != "tax" {
			t.Errorf("rule %s should not apply", d.Rule.ID)
		}
	}
	if len(got) == 0 {
		t.Fatal("annual return should still apply")
	}
}

func TestSortedByDate(t *testing.T) {
	rs := []Rule{
		{ID: "b", Name: "B", Frequency: "monthly", DueDay: 20},
		{ID: "a", Name: "A", Frequency: "monthly", DueDay: 9},
	}
	got := Deadlines(rs, Business{}, day(2026, 11, 1), day(2026, 11, 30))
	if len(got) != 2 || got[0].Rule.ID != "a" {
		t.Fatalf("not sorted by date: %+v", got)
	}
}

// TestEmbeddedRules guards the curated data file against typos.
func TestEmbeddedRules(t *testing.T) {
	rs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("no rules loaded")
	}
}
