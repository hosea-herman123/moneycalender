package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed rules.json
var rulesJSON []byte

type Rule struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Agency    string   `json:"agency"`
	Day       int      `json:"day"`
	Months    []int    `json:"months"`
	Note      string   `json:"note"`
	Types     []string `json:"types"`
	Employees bool     `json:"employees"`
	VAT       string   `json:"vat"`
	Verify    bool     `json:"verify"`
}

type Profile struct {
	Type   string
	Size   string
	County string
	VAT    bool
	Saved  bool
}

type SetupData struct {
	Profile  Profile
	Counties []string
	Error    string
}

var allRules []Rule

var counties = []string{
	"Baringo", "Bomet", "Bungoma", "Busia", "Elgeyo-Marakwet", "Embu", "Garissa",
	"Homa Bay", "Isiolo", "Kajiado", "Kakamega", "Kericho", "Kiambu", "Kilifi",
	"Kirinyaga", "Kisii", "Kisumu", "Kitui", "Kwale", "Laikipia", "Lamu", "Machakos",
	"Makueni", "Mandera", "Marsabit", "Meru", "Migori", "Mombasa", "Murang'a",
	"Nairobi", "Nakuru", "Nandi", "Narok", "Nyamira", "Nyandarua", "Nyeri",
	"Samburu", "Siaya", "Taita-Taveta", "Tana River", "Tharaka-Nithi", "Trans Nzoia",
	"Turkana", "Uasin Gishu", "Vihiga", "Wajir", "West Pokot",
}

var validTypes = []string{"company", "partnership", "sole_proprietor"}
var validSizes = []string{"none", "small", "medium"}

// ---------- Small helpers ----------

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// parseMonths turns "4,6,9,12" into []int{4, 6, 9, 12}. Empty means every month.
func parseMonths(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && n >= 1 && n <= 12 {
			out = append(out, n)
		}
	}
	return out
}

func monthsText(months []int) string {
	parts := make([]string, 0, len(months))
	for _, m := range months {
		parts = append(parts, strconv.Itoa(m))
	}
	return strings.Join(parts, ",")
}

// Schedule describes when an obligation falls due, in plain words.
func (o Obligation) Schedule() string {
	months := parseMonths(o.Months)
	day := ordinal(o.DayOfMonth)

	switch len(months) {
	case 0:
		return day + " of every month"
	case 1:
		return day + " of " + time.Month(months[0]).String() + " every year"
	}

	names := make([]string, 0, len(months))
	for _, m := range months {
		names = append(names, time.Month(m).String()[:3])
	}
	last := names[len(names)-1]
	return day + " of " + strings.Join(names[:len(names)-1], ", ") + " and " + last
}

// occurrences returns every due date of this obligation between from and to.
func (o Obligation) occurrences(from, to time.Time) []time.Time {
	months := parseMonths(o.Months)
	var out []time.Time

	for m := 0; m <= 2; m++ {
		first := time.Date(from.Year(), from.Month()+time.Month(m), 1, 0, 0, 0, 0, from.Location())
		if len(months) > 0 && !containsInt(months, int(first.Month())) {
			continue
		}
		d := dateInMonth(first.Year(), first.Month(), o.DayOfMonth, from.Location())
		if d.Before(from) || d.After(to) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// ---------- Database setup ----------

func addColumn(table, column, definition string) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		log.Fatal(err)
	}

	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			log.Fatal(err)
		}
		if name == column {
			found = true
		}
	}
	rows.Close()

	if !found {
		if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
			log.Fatal(err)
		}
	}
}

func setupCompliance() {
	if err := json.Unmarshal(rulesJSON, &allRules); err != nil {
		log.Fatal("rules.json is not valid: ", err)
	}

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS business_profile (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		business_type TEXT NOT NULL,
		size TEXT NOT NULL,
		county TEXT NOT NULL,
		vat INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		log.Fatal(err)
	}

	addColumn("obligations", "months", "TEXT NOT NULL DEFAULT ''")
	addColumn("obligations", "verify", "INTEGER NOT NULL DEFAULT 0")
	addColumn("obligations", "source", "TEXT NOT NULL DEFAULT 'manual'")

	log.Printf("loaded %d compliance rules", len(allRules))
}

// ---------- Profile and rule matching ----------

func loadProfile() Profile {
	var p Profile
	var vat int
	err := db.QueryRow(
		"SELECT business_type, size, county, vat FROM business_profile WHERE id = 1",
	).Scan(&p.Type, &p.Size, &p.County, &vat)
	if err != nil {
		return Profile{Type: "company", Size: "small"}
	}
	p.VAT = vat == 1
	p.Saved = true
	return p
}

func saveProfile(p Profile) error {
	vat := 0
	if p.VAT {
		vat = 1
	}
	_, err := db.Exec(`INSERT INTO business_profile (id, business_type, size, county, vat)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET business_type = excluded.business_type,
		size = excluded.size, county = excluded.county, vat = excluded.vat`,
		p.Type, p.Size, p.County, vat)
	return err
}

func rulesFor(p Profile) []Rule {
	var out []Rule
	for _, r := range allRules {
		if len(r.Types) > 0 && !containsStr(r.Types, p.Type) {
			continue
		}
		if r.Employees && p.Size == "none" {
			continue
		}
		if r.VAT == "yes" && !p.VAT {
			continue
		}
		if r.VAT == "no" && p.VAT {
			continue
		}
		out = append(out, r)
	}
	return out
}

// generateObligations replaces earlier generated obligations with the ones
// that match the profile. Amounts you entered before are kept.
func generateObligations(p Profile) (int, error) {
	amounts := map[string]int{}
	rows, err := db.Query("SELECT name, amount FROM obligations")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var name string
		var amount int
		if err := rows.Scan(&name, &amount); err != nil {
			rows.Close()
			return 0, err
		}
		amounts[strings.ToLower(name)] = amount
	}
	rows.Close()

	matching := rulesFor(p)

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM obligations WHERE source = 'rule'"); err != nil {
		return 0, err
	}

	for _, r := range matching {
		key := strings.ToLower(r.Name)

		// Remove a hand-typed duplicate (for example the old sample rows).
		if _, err := tx.Exec("DELETE FROM obligations WHERE source = 'manual' AND lower(name) = ?", key); err != nil {
			return 0, err
		}

		verify := 0
		if r.Verify {
			verify = 1
		}
		agency := strings.ReplaceAll(r.Agency, "{county}", p.County)

		_, err := tx.Exec(
			`INSERT INTO obligations (name, agency, day_of_month, amount, note, months, verify, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 'rule')`,
			r.Name, agency, r.Day, amounts[key], r.Note, monthsText(r.Months), verify,
		)
		if err != nil {
			return 0, err
		}
	}

	return len(matching), tx.Commit()
}

// ---------- Page ----------

func setupPage(w http.ResponseWriter, r *http.Request) {
	data := SetupData{Profile: loadProfile(), Counties: counties}

	if r.Method == http.MethodPost {
		p := Profile{
			Type:   r.FormValue("business_type"),
			Size:   r.FormValue("size"),
			County: r.FormValue("county"),
			VAT:    r.FormValue("vat") == "yes",
		}
		data.Profile = p

		if !containsStr(validTypes, p.Type) || !containsStr(validSizes, p.Size) || !containsStr(counties, p.County) {
			data.Error = "Please choose a business type, a size and a county."
			render(w, "setup.html", data)
			return
		}

		if err := saveProfile(p); err != nil {
			http.Error(w, "Could not save profile", http.StatusInternalServerError)
			return
		}
		if _, err := generateObligations(p); err != nil {
			log.Println("generate error:", err)
			http.Error(w, "Could not build your calendar", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	render(w, "setup.html", data)
}