package rules

import (
	"database/sql"
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed data/obligations.yaml
var embedded []byte

// Load parses and validates the embedded obligations.yaml.
func Load() ([]Rule, error) { return Parse(embedded) }

// Parse parses and validates rules YAML.
func Parse(data []byte) ([]Rule, error) {
	var rs []Rule
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	seen := map[string]bool{}
	for i, r := range rs {
		where := fmt.Sprintf("rule #%d (%q)", i+1, r.ID)
		switch {
		case r.ID == "" || r.Name == "":
			return nil, fmt.Errorf("%s: id and name are required", where)
		case seen[r.ID]:
			return nil, fmt.Errorf("%s: duplicate id", where)
		case r.Frequency != "monthly" && r.Frequency != "annual":
			return nil, fmt.Errorf("%s: frequency must be monthly or annual", where)
		case r.DueDay < 1 || r.DueDay > 31:
			return nil, fmt.Errorf("%s: due_day must be 1-31", where)
		case r.Frequency == "annual" && (r.DueMonth < 1 || r.DueMonth > 12):
			return nil, fmt.Errorf("%s: annual rules need due_month 1-12", where)
		case r.Link == "":
			return nil, fmt.Errorf("%s: official link is required", where)
		}
		seen[r.ID] = true
	}
	return rs, nil
}

// SyncToDB mirrors the YAML into the obligation_rules table (the YAML stays
// the source of truth; the table is for reporting and future admin screens).
func SyncToDB(db *sql.DB, rs []Rule) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rs {
		_, err := tx.Exec(`
			INSERT INTO obligation_rules(id, name, agency, frequency, due_rule, penalty, note, link, last_verified)
			VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, agency=excluded.agency, frequency=excluded.frequency,
				due_rule=excluded.due_rule, penalty=excluded.penalty, note=excluded.note,
				link=excluded.link, last_verified=excluded.last_verified`,
			r.ID, r.Name, r.Agency, r.Frequency, r.DueRule, r.Penalty, r.Note, r.Link, r.LastVerified)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
