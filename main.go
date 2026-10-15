package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const dateLayout = "2006-01-02"
const windowDays = 30

type Obligation struct {
	ID         int
	Name       string
	Agency     string
	DayOfMonth int
	Amount     int
	Note       string
	Months     string
	Verify     bool
	Source     string
}

func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

type Invoice struct {
	ID           int
	Client       string
	Amount       int
	IssueDate    string
	DueDate      string
	PaidDate     string
	Status       string
	ClientEmail  string
	LastReminded string
	WhatsAppURL  string
}

type ClientScore struct {
	Client      string
	PaidCount   int
	AvgDaysLate int
	Rating      string
}

type InvoicesPage struct {
	Invoices []Invoice
	Scores   []ClientScore
}

type Event struct {
	Date     time.Time
	DateText string
	Kind     string // "Out", "In" or "Overdue"
	Label    string
	Detail   string
	Amount   int
	Balance  int
	Short    bool
}

type TimelinePage struct {
	Cash     int
	HasCash  bool
	Events   []Event
	Overdue  []Event
	Warning  string
	TotalOut int
	TotalIn  int
}

var db *sql.DB

func setupDatabase() {
	var err error
	db, err = sql.Open("sqlite", dbPath())
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS obligations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		agency TEXT NOT NULL,
		day_of_month INTEGER NOT NULL,
		amount INTEGER NOT NULL DEFAULT 0,
		note TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS invoices (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client TEXT NOT NULL,
		amount INTEGER NOT NULL,
		issue_date TEXT NOT NULL,
		due_date TEXT NOT NULL,
		paid_date TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		log.Fatal(err)
	}
}

// ---------- Reading data ----------

func getObligations() ([]Obligation, error) {
	rows, err := db.Query("SELECT id, name, agency, day_of_month, amount, note, months, verify, source FROM obligations ORDER BY day_of_month")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Obligation
	for rows.Next() {
		var o Obligation
		err := rows.Scan(&o.ID, &o.Name, &o.Agency, &o.DayOfMonth, &o.Amount, &o.Note, &o.Months, &o.Verify, &o.Source)
		if err != nil {
			return nil, err
		}
		list = append(list, o)
	}
	return list, rows.Err()
}

func getObligation(id int) (Obligation, error) {
	var o Obligation
	err := db.QueryRow(
		"SELECT id, name, agency, day_of_month, amount, note, months, verify, source FROM obligations WHERE id = ?", id,
	).Scan(&o.ID, &o.Name, &o.Agency, &o.DayOfMonth, &o.Amount, &o.Note, &o.Months, &o.Verify, &o.Source)
	return o, err
}

// whatsappLink builds a wa.me link with a polite reminder already typed in.
func whatsappLink(inv Invoice) string {
	business := os.Getenv("BUSINESS_NAME")
	if business == "" {
		business = "Your supplier"
	}
	text := fmt.Sprintf(
		"Hello %s, a friendly reminder that our invoice for KES %d, due on %s, is still unpaid. "+
			"Please let us know when to expect payment. Thank you, %s.",
		inv.Client, inv.Amount, inv.DueDate, business,
	)
	return "https://wa.me/?text=" + url.QueryEscape(text)
}

func getInvoices() ([]Invoice, error) {
	rows, err := db.Query(`
		SELECT i.id, i.client, i.amount, i.issue_date, i.due_date, i.paid_date,
		       COALESCE(c.email, ''),
		       COALESCE((SELECT MAX(l.sent_on) FROM client_reminder_log l WHERE l.invoice_id = i.id), '')
		FROM invoices i
		LEFT JOIN client_emails c ON c.client = i.client
		ORDER BY i.due_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	today := time.Now().Format(dateLayout)

	var list []Invoice
	for rows.Next() {
		var inv Invoice
		err := rows.Scan(&inv.ID, &inv.Client, &inv.Amount, &inv.IssueDate, &inv.DueDate,
			&inv.PaidDate, &inv.ClientEmail, &inv.LastReminded)
		if err != nil {
			return nil, err
		}

		if inv.PaidDate != "" {
			inv.Status = "Paid"
		} else if inv.DueDate < today {
			inv.Status = "Overdue"
			inv.WhatsAppURL = whatsappLink(inv)
		} else {
			inv.Status = "Pending"
		}

		list = append(list, inv)
	}
	return list, rows.Err()
}

func getInvoice(id int) (Invoice, error) {
	var inv Invoice
	err := db.QueryRow(
		"SELECT id, client, amount, issue_date, due_date, paid_date FROM invoices WHERE id = ?", id,
	).Scan(&inv.ID, &inv.Client, &inv.Amount, &inv.IssueDate, &inv.DueDate, &inv.PaidDate)
	return inv, err
}

func ratingFor(avgDaysLate int) string {
	if avgDaysLate <= 0 {
		return "Reliable"
	}
	if avgDaysLate <= 14 {
		return "Slightly late"
	}
	return "Risky"
}

func getScores() ([]ClientScore, error) {
	rows, err := db.Query("SELECT client, due_date, paid_date FROM invoices WHERE paid_date != '' ORDER BY client")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := map[string]int{}
	counts := map[string]int{}
	var names []string

	for rows.Next() {
		var client, due, paid string
		err := rows.Scan(&client, &due, &paid)
		if err != nil {
			return nil, err
		}

		dueTime, err1 := time.Parse(dateLayout, due)
		paidTime, err2 := time.Parse(dateLayout, paid)
		if err1 != nil || err2 != nil {
			continue
		}

		daysLate := int(paidTime.Sub(dueTime).Hours() / 24)

		if _, seen := counts[client]; !seen {
			names = append(names, client)
		}
		totals[client] += daysLate
		counts[client]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var scores []ClientScore
	for _, name := range names {
		avg := totals[name] / counts[name]
		scores = append(scores, ClientScore{
			Client:      name,
			PaidCount:   counts[name],
			AvgDaysLate: avg,
			Rating:      ratingFor(avg),
		})
	}
	return scores, nil
}

// ---------- Timeline ----------

// dateInMonth returns the given day of a month, using the last day of the
// month if the month is too short (for example day 31 in November).
func dateInMonth(year int, month time.Month, day int, loc *time.Location) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, loc)
}

func buildTimeline(cash int, hasCash bool) (TimelinePage, error) {
	page := TimelinePage{Cash: cash, HasCash: hasCash}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := today.AddDate(0, 0, windowDays)

	var events []Event

	// 1. Money going out: every due date of each obligation in the window.
	obligations, err := getObligations()
	if err != nil {
		return page, err
	}
	for _, o := range obligations {
		for _, d := range o.occurrences(today, end) {
			events = append(events, Event{
				Date:   d,
				Kind:   "Out",
				Label:  o.Name + " (" + o.Agency + ")",
				Detail: o.Note,
				Amount: o.Amount,
			})
		}
	}

	// 2. Money coming in: unpaid invoices, pushed back by the client's usual lateness.
	invs, err := getInvoices()
	if err != nil {
		return page, err
	}
	scores, err := getScores()
	if err != nil {
		return page, err
	}
	avgLate := map[string]int{}
	for _, s := range scores {
		avgLate[s.Client] = s.AvgDaysLate
	}

	for _, inv := range invs {
		if inv.PaidDate != "" {
			continue
		}
		due, err := time.ParseInLocation(dateLayout, inv.DueDate, today.Location())
		if err != nil {
			continue
		}
		late := avgLate[inv.Client]
		if late < 0 {
			late = 0
		}
		expected := due.AddDate(0, 0, late)

		ev := Event{Date: expected, Label: inv.Client + " invoice", Amount: inv.Amount}

		if expected.Before(today) {
			ev.Kind = "Overdue"
			ev.DateText = due.Format("Mon 2 Jan 2006")
			ev.Detail = "Chase this one. It is not counted as incoming cash below."
			page.Overdue = append(page.Overdue, ev)
			continue
		}
		if expected.After(end) {
			continue
		}

		ev.Kind = "In"
		if late > 0 {
			ev.Detail = fmt.Sprintf("Due %s, but this client usually pays %d days late.", inv.DueDate, late)
		} else {
			ev.Detail = "Due " + inv.DueDate + "."
		}
		events = append(events, ev)
	}

	// 3. Sort by date. On the same day, money out comes first (the cautious choice).
	sort.Slice(events, func(i, j int) bool {
		if !events[i].Date.Equal(events[j].Date) {
			return events[i].Date.Before(events[j].Date)
		}
		return events[i].Kind == "Out" && events[j].Kind != "Out"
	})

	// 4. Walk through the events, tracking the running balance.
	balance := cash
	for i := range events {
		e := &events[i]
		e.DateText = e.Date.Format("Mon 2 Jan")

		if e.Kind == "Out" {
			balance -= e.Amount
			page.TotalOut += e.Amount
		} else {
			balance += e.Amount
			page.TotalIn += e.Amount
		}
		e.Balance = balance

		if hasCash && balance < 0 {
			e.Short = true
			if page.Warning == "" {
				page.Warning = fmt.Sprintf(
					"Cash crunch: on %s you will be short by KES %d after %s.",
					e.DateText, -balance, e.Label,
				)
			}
		}
	}

	page.Events = events
	return page, nil
}

// ---------- Rendering ----------

// render joins the shared layout with one page template and sends the result.
// It also tells the layout who is logged in, so the menu can show the right links.
func render(w http.ResponseWriter, page string, data any) {
	email := loggedInEmail(w)
	funcs := template.FuncMap{"userEmail": func() string { return email }}

	tmpl, err := template.New("layout.html").Funcs(funcs).ParseFiles("templates/layout.html", "templates/"+page)
	if err != nil {
		log.Println("template error:", err)
		http.Error(w, "Could not load page", http.StatusInternalServerError)
		return
	}
	err = tmpl.ExecuteTemplate(w, "layout", data)
	if err != nil {
		log.Println("render error:", err)
	}
}

// ---------- Form helpers ----------

// obligationFromForm reads and validates the obligation form fields.
func obligationFromForm(r *http.Request) (Obligation, error) {
	o := Obligation{
		Name:   r.FormValue("name"),
		Agency: r.FormValue("agency"),
		Note:   r.FormValue("note"),
	}

	day, err := strconv.Atoi(r.FormValue("day_of_month"))
	if o.Name == "" || o.Agency == "" || err != nil || day < 1 || day > 31 {
		return o, errors.New("Name, agency and a day between 1 and 31 are required")
	}
	o.DayOfMonth = day

	if amountText := r.FormValue("amount"); amountText != "" {
		amount, err := strconv.Atoi(amountText)
		if err != nil || amount < 0 {
			return o, errors.New("Amount must be a positive number")
		}
		o.Amount = amount
	}
	return o, nil
}

// invoiceFromForm reads and validates the invoice form fields.
func invoiceFromForm(r *http.Request) (Invoice, error) {
	inv := Invoice{
		Client:    r.FormValue("client"),
		IssueDate: r.FormValue("issue_date"),
		DueDate:   r.FormValue("due_date"),
	}

	amount, err := strconv.Atoi(r.FormValue("amount"))
	if inv.Client == "" || err != nil || amount <= 0 || inv.IssueDate == "" || inv.DueDate == "" {
		return inv, errors.New("Please fill in all fields with valid values")
	}
	inv.Amount = amount
	return inv, nil
}

// ---------- Obligation handlers ----------

func home(w http.ResponseWriter, r *http.Request) {
	obligations, err := getObligations()
	if err != nil {
		http.Error(w, "Could not read data", http.StatusInternalServerError)
		return
	}
	render(w, "home.html", obligations)
}

func addObligation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	o, err := obligationFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, err = db.Exec(
		"INSERT INTO obligations (name, agency, day_of_month, amount, note) VALUES (?, ?, ?, ?, ?)",
		o.Name, o.Agency, o.DayOfMonth, o.Amount, o.Note,
	)
	if err != nil {
		http.Error(w, "Could not save", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func editObligation(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		id, err := strconv.Atoi(r.FormValue("id"))
		if err != nil {
			http.Error(w, "Invalid obligation", http.StatusBadRequest)
			return
		}

		o, err := obligationFromForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		_, err = db.Exec(
			"UPDATE obligations SET name = ?, agency = ?, day_of_month = ?, amount = ?, note = ? WHERE id = ?",
			o.Name, o.Agency, o.DayOfMonth, o.Amount, o.Note, id,
		)
		if err != nil {
			http.Error(w, "Could not update", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	o, err := getObligation(id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Could not read data", http.StatusInternalServerError)
		return
	}

	render(w, "edit_obligation.html", o)
}

func deleteObligation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid obligation", http.StatusBadRequest)
		return
	}

	_, err = db.Exec("DELETE FROM obligations WHERE id = ?", id)
	if err != nil {
		http.Error(w, "Could not delete", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------- Invoice handlers ----------

func invoices(w http.ResponseWriter, r *http.Request) {
	invList, err := getInvoices()
	if err != nil {
		http.Error(w, "Could not read invoices", http.StatusInternalServerError)
		return
	}

	scores, err := getScores()
	if err != nil {
		http.Error(w, "Could not calculate scores", http.StatusInternalServerError)
		return
	}

	render(w, "invoices.html", InvoicesPage{Invoices: invList, Scores: scores})
}

func addInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/invoices", http.StatusSeeOther)
		return
	}

	inv, err := invoiceFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, err = db.Exec(
		"INSERT INTO invoices (client, amount, issue_date, due_date) VALUES (?, ?, ?, ?)",
		inv.Client, inv.Amount, inv.IssueDate, inv.DueDate,
	)
	if err != nil {
		http.Error(w, "Could not save invoice", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/invoices", http.StatusSeeOther)
}

func editInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		id, err := strconv.Atoi(r.FormValue("id"))
		if err != nil {
			http.Error(w, "Invalid invoice", http.StatusBadRequest)
			return
		}

		inv, err := invoiceFromForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		_, err = db.Exec(
			"UPDATE invoices SET client = ?, amount = ?, issue_date = ?, due_date = ? WHERE id = ?",
			inv.Client, inv.Amount, inv.IssueDate, inv.DueDate, id,
		)
		if err != nil {
			http.Error(w, "Could not update invoice", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/invoices", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	inv, err := getInvoice(id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Could not read invoice", http.StatusInternalServerError)
		return
	}

	render(w, "edit_invoice.html", inv)
}

func deleteInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/invoices", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid invoice", http.StatusBadRequest)
		return
	}

	_, err = db.Exec("DELETE FROM invoices WHERE id = ?", id)
	if err != nil {
		http.Error(w, "Could not delete invoice", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/invoices", http.StatusSeeOther)
}

func markPaid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/invoices", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid invoice", http.StatusBadRequest)
		return
	}

	today := time.Now().Format(dateLayout)
	_, err = db.Exec("UPDATE invoices SET paid_date = ? WHERE id = ?", today, id)
	if err != nil {
		http.Error(w, "Could not update invoice", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/invoices", http.StatusSeeOther)
}

// ---------- Other pages ----------

func timeline(w http.ResponseWriter, r *http.Request) {
	cash, hasCash := 0, false
	if text := r.URL.Query().Get("cash"); text != "" {
		n, convErr := strconv.Atoi(text)
		if convErr == nil && n >= 0 {
			cash, hasCash = n, true
		}
	}

	page, err := buildTimeline(cash, hasCash)
	if err != nil {
		http.Error(w, "Could not build timeline", http.StatusInternalServerError)
		return
	}

	render(w, "timeline.html", page)
}

func about(w http.ResponseWriter, r *http.Request) {
	render(w, "about.html", nil)
}

func main() {
	setupDatabase()
	defer db.Close()
	setupCompliance()
	setupAuth()

	setupReminders()
	setupClientReminders()
	startReminders()

	http.HandleFunc("/", home)
	http.HandleFunc("/about", about)
	http.HandleFunc("/signup", signupPage)
	http.HandleFunc("/login", loginPage)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/setup", setupPage)
	http.HandleFunc("/add", addObligation)
	http.HandleFunc("/obligations/edit", editObligation)
	http.HandleFunc("/obligations/delete", deleteObligation)
	http.HandleFunc("/invoices", invoices)
	http.HandleFunc("/invoices/add", addInvoice)
	http.HandleFunc("/invoices/edit", editInvoice)
	http.HandleFunc("/invoices/delete", deleteInvoice)
	http.HandleFunc("/invoices/paid", markPaid)
	http.HandleFunc("/invoices/remind", remindClient)
	http.HandleFunc("/timeline", timeline)
	http.HandleFunc("/reminders", remindersPage)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	fmt.Println("Server running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", withUser(requireLogin(http.DefaultServeMux))))
}