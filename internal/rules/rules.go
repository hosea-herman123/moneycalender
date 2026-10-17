// Package rules is the compliance engine: given a business profile it decides
// which obligations apply and when they fall due. It has no HTTP or database
// code so it can be unit tested in isolation.
package rules

import (
	"sort"
	"strings"
	"time"
)

// Business is the part of a business profile the rules care about.
type Business struct {
	ID            int64
	Type          string
	County        string
	VATRegistered bool
	HasEmployees  bool
}

// Condition says who a rule applies to. Unset fields mean "no restriction".
type Condition struct {
	HasEmployees  *bool    `yaml:"has_employees"`
	VATRegistered *bool    `yaml:"vat_registered"`
	BusinessTypes []string `yaml:"business_types"`
	Counties      []string `yaml:"counties"`
}

// Rule is one filing/payment obligation.
//
// Frequency "monthly": due on DueDay of every month (the period it covers is
// the previous month). Frequency "annual": due on DueMonth/DueDay every year.
// If DueDay is past the end of a month it is clamped to the last day.
type Rule struct {
	ID           string    `yaml:"id"`
	Name         string    `yaml:"name"`
	Agency       string    `yaml:"agency"`
	Frequency    string    `yaml:"frequency"`
	DueDay       int       `yaml:"due_day"`
	DueMonth     int       `yaml:"due_month"`
	DueRule      string    `yaml:"due_rule"`
	AppliesIf    Condition `yaml:"applies_if"`
	Penalty      string    `yaml:"penalty"`
	Note         string    `yaml:"note"`
	Link         string    `yaml:"link"`
	LastVerified string    `yaml:"last_verified"` // "YYYY-MM-DD"; empty = not verified yet
}

// Verified reports whether a human has checked this rule against the official source.
func (r Rule) Verified() bool { return r.LastVerified != "" }

// Applies reports whether the rule applies to the business.
func (r Rule) Applies(b Business) bool {
	c := r.AppliesIf
	if c.HasEmployees != nil && *c.HasEmployees != b.HasEmployees {
		return false
	}
	if c.VATRegistered != nil && *c.VATRegistered != b.VATRegistered {
		return false
	}
	if len(c.BusinessTypes) > 0 && !containsFold(c.BusinessTypes, b.Type) {
		return false
	}
	if len(c.Counties) > 0 && !containsFold(c.Counties, b.County) {
		return false
	}
	return true
}

// Deadline is one concrete due date for a rule.
type Deadline struct {
	Rule Rule
	Date time.Time
}

// Deadlines returns every due date of every applicable rule between from and
// to (inclusive), sorted by date. Dates are midnight in from's location.
func Deadlines(rs []Rule, b Business, from, to time.Time) []Deadline {
	loc := from.Location()
	from = startOfDay(from)
	to = startOfDay(to.In(loc))

	var out []Deadline
	add := func(r Rule, d time.Time) {
		if !d.Before(from) && !d.After(to) {
			out = append(out, Deadline{Rule: r, Date: d})
		}
	}

	for _, r := range rs {
		if !r.Applies(b) {
			continue
		}
		switch r.Frequency {
		case "monthly":
			cur := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, loc)
			for ; !cur.After(to); cur = cur.AddDate(0, 1, 0) {
				add(r, clampDate(cur.Year(), cur.Month(), r.DueDay, loc))
			}
		case "annual":
			for y := from.Year(); y <= to.Year(); y++ {
				add(r, clampDate(y, time.Month(r.DueMonth), r.DueDay, loc))
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].Rule.Name < out[j].Rule.Name
	})
	return out
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func clampDate(y int, m time.Month, d int, loc *time.Location) time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
	if d > last {
		d = last
	}
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}
