// Package web holds the HTTP handlers and server-rendered templates.
package web

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"kalenda/internal/auth"
	"kalenda/internal/invoices"
	"kalenda/internal/reminders"
	"kalenda/internal/rules"
	"kalenda/internal/timeline"
)

//go:embed templates/*.html static/*
var tplFS embed.FS

const cookieName = "kalenda_session"

type Server struct {
	DB       *sql.DB
	Auth     *auth.Store
	Rules    []rules.Rule
	Loc      *time.Location
	Invoices *invoices.Store

	// Extra adds income items such as invoices to the compliance timeline.
	Extra timeline.Provider

	pages map[string]*template.Template
}

func New(db *sql.DB, rs []rules.Rule, loc *time.Location) (*Server, error) {
	s := &Server{DB: db, Auth: auth.New(db), Rules: rs, Loc: loc, Invoices: invoices.New(db), pages: map[string]*template.Template{}}
	for _, p := range []string{"login", "signup", "onboarding", "calendar", "invoices"} {
		t, err := template.ParseFS(tplFS, "templates/layout.html", "templates/"+p+".html")
		if err != nil {
			return nil, err
		}
		s.pages[p] = t
	}
	return s, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(tplFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /signup", s.signupForm)
	mux.HandleFunc("POST /signup", s.signup)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /onboarding", s.authed(s.onboardingForm))
	mux.HandleFunc("POST /onboarding", s.authed(s.onboarding))
	mux.HandleFunc("GET /calendar", s.authed(s.calendar))
	mux.HandleFunc("POST /forecast", s.authed(s.saveForecast))
	mux.HandleFunc("GET /invoices", s.authed(s.invoiceList))
	mux.HandleFunc("POST /invoices/add", s.authed(s.invoiceAdd))
	mux.HandleFunc("POST /invoices/paid", s.authed(s.invoicePaid))
	mux.HandleFunc("POST /invoices/delete", s.authed(s.invoiceDelete))
	mux.HandleFunc("POST /invoices/remind", s.authed(s.invoiceRemind))
	return mux
}

// ---------- helpers ----------

func (s *Server) render(w http.ResponseWriter, status int, page string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.pages[page].ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

func (s *Server) currentUser(r *http.Request) (auth.User, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return auth.User{}, false
	}
	return s.Auth.UserFor(c.Value)
}

type handlerWithUser func(http.ResponseWriter, *http.Request, auth.User)

func (s *Server) authed(h handlerWithUser) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r, u)
	}
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := s.Auth.NewSession(userID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		MaxAge:   int(auth.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // also our baseline CSRF protection
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	return nil
}

func userMessage(err error) string {
	var ue auth.UserError
	if errors.As(err, &ue) {
		return ue.Error()
	}
	log.Printf("internal error: %v", err)
	return "Something went wrong on our side. Please try again."
}

// ---------- auth pages ----------

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); ok {
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) signupForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, 200, "signup", map[string]any{})
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	id, err := s.Auth.CreateUser(email, r.FormValue("password"))
	if err == nil {
		err = s.startSession(w, r, id)
	}
	if err != nil {
		s.render(w, 400, "signup", map[string]any{"Error": userMessage(err), "Email": email})
		return
	}
	http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, 200, "login", map[string]any{})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	id, err := s.Auth.Authenticate(email, r.FormValue("password"))
	if err == nil {
		err = s.startSession(w, r, id)
	}
	if err != nil {
		s.render(w, 400, "login", map[string]any{"Error": userMessage(err), "Email": email})
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.Auth.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------- onboarding ----------

// Biz is the stored business profile.
type Biz struct {
	ID             int64
	Name           string
	Type           string
	County         string
	VAT            bool
	Employees      bool
	CashBalance    int64
	CashConfigured bool
}

func (b Biz) Rules() rules.Business {
	return rules.Business{ID: b.ID, Type: b.Type, County: b.County, VATRegistered: b.VAT, HasEmployees: b.Employees}
}

func (s *Server) loadBusiness(userID int64) (Biz, error) {
	var b Biz
	var vat, emp, cashConfigured int
	err := s.DB.QueryRow(`
		SELECT id, name, business_type, county, vat_registered, has_employees, cash_balance, cash_configured
		FROM businesses WHERE user_id = ?`, userID).Scan(&b.ID, &b.Name, &b.Type, &b.County, &vat, &emp, &b.CashBalance, &cashConfigured)
	b.VAT, b.Employees = vat == 1, emp == 1
	b.CashConfigured = cashConfigured == 1
	return b, err
}

var businessTypes = []string{"Sole proprietor", "Partnership", "Limited company", "Other"}

var counties = []string{
	"Baringo", "Bomet", "Bungoma", "Busia", "Elgeyo-Marakwet", "Embu", "Garissa", "Homa Bay",
	"Isiolo", "Kajiado", "Kakamega", "Kericho", "Kiambu", "Kilifi", "Kirinyaga", "Kisii",
	"Kisumu", "Kitui", "Kwale", "Laikipia", "Lamu", "Machakos", "Makueni", "Mandera",
	"Marsabit", "Meru", "Migori", "Mombasa", "Murang'a", "Nairobi", "Nakuru", "Nandi",
	"Narok", "Nyamira", "Nyandarua", "Nyeri", "Samburu", "Siaya", "Taita-Taveta", "Tana River",
	"Tharaka-Nithi", "Trans Nzoia", "Turkana", "Uasin Gishu", "Vihiga", "Wajir", "West Pokot",
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (s *Server) onboardingForm(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "server error", 500)
		return
	}
	s.render(w, 200, "onboarding", map[string]any{
		"User": u, "Biz": b, "Types": businessTypes, "Counties": counties,
	})
}

func (s *Server) onboarding(w http.ResponseWriter, r *http.Request, u auth.User) {
	b := Biz{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Type:      r.FormValue("type"),
		County:    r.FormValue("county"),
		VAT:       r.FormValue("vat") == "yes",
		Employees: r.FormValue("employees") == "yes",
	}
	fail := func(msg string) {
		s.render(w, 400, "onboarding", map[string]any{
			"User": u, "Biz": b, "Types": businessTypes, "Counties": counties, "Error": msg,
		})
	}
	switch {
	case b.Name == "" || len(b.Name) > 120:
		fail("Please enter your business name.")
		return
	case !contains(businessTypes, b.Type):
		fail("Please choose a business type.")
		return
	case !contains(counties, b.County):
		fail("Please choose your county.")
		return
	}

	_, err := s.DB.Exec(`
		INSERT INTO businesses(user_id, name, business_type, county, vat_registered, has_employees)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			name=excluded.name, business_type=excluded.business_type, county=excluded.county,
			vat_registered=excluded.vat_registered, has_employees=excluded.has_employees`,
		u.ID, b.Name, b.Type, b.County, b2i(b.VAT), b2i(b.Employees))
	if err == nil {
		b, err = s.loadBusiness(u.ID)
	}
	if err == nil {
		// Profile may have changed: drop unsent deadline reminders and rebuild them.
		_, err = s.DB.Exec(`DELETE FROM reminders WHERE business_id = ? AND status = 'pending' AND dedupe_key LIKE 'deadline:%'`, b.ID)
	}
	if err == nil {
		err = reminders.ScheduleForBusiness(s.DB, s.Rules, b.ID, u.Email, b.Rules(), s.Loc, time.Now())
	}
	if err != nil {
		log.Printf("onboarding: %v", err)
		fail("Something went wrong on our side. Please try again.")
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- calendar ----------

type itemView struct {
	Kind      string
	Title     string
	DateStr   string
	DaysLabel string
	Agency    string
	Note      string
	Penalty   string
	Link      string
	Verified  bool
	Amount    string
	Balance   string
	Short     bool
}

type forecastRule struct {
	ID     string
	Name   string
	Amount string
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "server error", 500)
		return
	}

	now := time.Now().In(s.Loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc)
	to := from.AddDate(0, 0, 90)

	estimates, err := s.forecastEstimates(b.ID)
	if err != nil {
		http.Error(w, "Could not load forecast settings", http.StatusInternalServerError)
		return
	}
	var forecastRules []forecastRule
	for _, rule := range s.Rules {
		if rule.Applies(b.Rules()) {
			forecastRules = append(forecastRules, forecastRule{ID: rule.ID, Name: rule.Name, Amount: strconv.FormatInt(estimates[rule.ID], 10)})
		}
	}

	var items []timeline.Item
	for _, d := range rules.Deadlines(s.Rules, b.Rules(), from, to) {
		items = append(items, timeline.Item{
			Date: d.Date, Kind: timeline.KindDeadline, SourceID: d.Rule.ID, Title: d.Rule.Name, Agency: d.Rule.Agency,
			Amount: estimates[d.Rule.ID], Note: d.Rule.Note, Penalty: d.Rule.Penalty, Link: d.Rule.Link, Verified: d.Rule.Verified(),
		})
	}
	if s.Extra != nil {
		extra, err := s.Extra(b.ID, from, to)
		if err != nil {
			log.Printf("timeline extra: %v", err) // show deadlines even if invoices fail
		}
		items = append(items, extra...)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date.Before(items[j].Date) })

	views := make([]itemView, 0, len(items))
	balance := b.CashBalance
	for _, it := range items {
		v := itemView{
			Kind: string(it.Kind), Title: it.Title, Agency: it.Agency, Note: it.Note,
			Penalty: it.Penalty, Link: it.Link, Verified: it.Verified || it.Kind != timeline.KindDeadline,
			DateStr: it.Date.In(s.Loc).Format("Mon 2 Jan 2006"),
		}
		days := int(it.Date.Sub(from).Hours() / 24)
		switch {
		case days <= 0:
			v.DaysLabel = "today"
		case days == 1:
			v.DaysLabel = "tomorrow"
		default:
			v.DaysLabel = fmt.Sprintf("in %d days", days)
		}
		if it.Amount > 0 {
			v.Amount = formatKES(it.Amount)
		}
		if b.CashConfigured {
			if it.Kind == timeline.KindDeadline {
				balance -= it.Amount
			} else if it.Kind == timeline.KindInvoice {
				balance += it.Amount
			}
			v.Balance = formatKES(balance)
			v.Short = balance < 0
		}
		views = append(views, v)
	}

	s.render(w, 200, "calendar", map[string]any{"User": u, "Biz": b, "Items": views, "ForecastRules": forecastRules})
}

func (s *Server) forecastEstimates(businessID int64) (map[string]int64, error) {
	rows, err := s.DB.Query(`SELECT rule_id, estimated_amount FROM obligation_estimates WHERE business_id=?`, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]int64)
	for rows.Next() {
		var id string
		var amount int64
		if err := rows.Scan(&id, &amount); err != nil {
			return nil, err
		}
		values[id] = amount
	}
	return values, rows.Err()
}

func (s *Server) saveForecast(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if err != nil {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	cash, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("cash_balance")), 10, 64)
	if err != nil || cash < 0 {
		http.Error(w, "Enter a non-negative current cash amount.", http.StatusBadRequest)
		return
	}
	amounts := make(map[string]int64)
	for _, rule := range s.Rules {
		if !rule.Applies(b.Rules()) {
			continue
		}
		text := strings.TrimSpace(r.FormValue("estimate_" + rule.ID))
		if text == "" {
			text = "0"
		}
		amount, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr != nil || amount < 0 {
			http.Error(w, "Enter a non-negative amount for each applicable obligation.", http.StatusBadRequest)
			return
		}
		amounts[rule.ID] = amount
	}
	tx, err := s.DB.Begin()
	if err != nil {
		http.Error(w, "Could not save forecast", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE businesses SET cash_balance=?, cash_configured=1 WHERE id=?`, cash, b.ID); err != nil {
		http.Error(w, "Could not save forecast", http.StatusInternalServerError)
		return
	}
	for id, amount := range amounts {
		if _, err := tx.Exec(`INSERT INTO obligation_estimates(business_id,rule_id,estimated_amount) VALUES(?,?,?)
			ON CONFLICT(business_id,rule_id) DO UPDATE SET estimated_amount=excluded.estimated_amount`, b.ID, id, amount); err != nil {
			http.Error(w, "Could not save forecast", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "Could not save forecast", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func formatKES(n int64) string {
	s := strconv.FormatInt(n, 10)
	start := 0
	if strings.HasPrefix(s, "-") {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return "KES " + s
}

func (s *Server) invoiceList(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	items, err := s.Invoices.List(b.ID, time.Now().In(s.Loc))
	if err != nil {
		log.Printf("invoice list: %v", err)
		http.Error(w, "Could not load invoices", http.StatusInternalServerError)
		return
	}
	scores, err := s.Invoices.Scores(b.ID)
	if err != nil {
		log.Printf("client scores: %v", err)
		http.Error(w, "Could not load client history", http.StatusInternalServerError)
		return
	}
	_, smtpConfigured := reminders.SMTPFromEnv()
	s.render(w, http.StatusOK, "invoices", map[string]any{
		"User": u, "Biz": b, "Invoices": items, "Scores": scores,
		"SMTPConfigured": smtpConfigured, "ReminderSent": r.URL.Query().Get("reminder") == "sent",
	})
}

func (s *Server) invoiceAdd(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if err != nil {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	err = s.Invoices.Create(b.ID, r.FormValue("client"), r.FormValue("email"), r.FormValue("amount"), r.FormValue("issue_date"), r.FormValue("due_date"))
	if err != nil {
		if errors.Is(err, invoices.ErrInvalid) {
			http.Error(w, "Enter a client, valid optional email, positive KES amount, and valid dates with the due date on or after the issue date.", http.StatusBadRequest)
			return
		}
		log.Printf("invoice create: %v", err)
		http.Error(w, "Could not save invoice", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/invoices", http.StatusSeeOther)
}

func (s *Server) invoicePaid(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if err != nil {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid invoice", http.StatusBadRequest)
		return
	}
	date := time.Now().In(s.Loc).Format("2006-01-02")
	if err := s.Invoices.MarkPaid(b.ID, id, date); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Could not update invoice", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/invoices", http.StatusSeeOther)
}

func (s *Server) invoiceDelete(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if err != nil {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid invoice", http.StatusBadRequest)
		return
	}
	if err := s.Invoices.Delete(b.ID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Could not delete invoice", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/invoices", http.StatusSeeOther)
}

func (s *Server) invoiceRemind(w http.ResponseWriter, r *http.Request, u auth.User) {
	b, err := s.loadBusiness(u.ID)
	if err != nil {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid invoice", http.StatusBadRequest)
		return
	}
	now := time.Now().In(s.Loc)
	inv, err := s.Invoices.OverdueInvoice(b.ID, id, now)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Only overdue unpaid invoices can be reminded.", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Could not load invoice", http.StatusInternalServerError)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		email = inv.Email
	}
	today := now.Format("2006-01-02")
	var already int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM client_reminder_log WHERE invoice_id=? AND sent_on=?`, id, today).Scan(&already); err != nil {
		http.Error(w, "Could not check reminder history", http.StatusInternalServerError)
		return
	}
	if already > 0 {
		http.Error(w, "A reminder for this invoice was already sent today.", http.StatusConflict)
		return
	}
	due, _ := time.ParseInLocation("2006-01-02", inv.DueDate, s.Loc)
	daysLate := int(now.Sub(time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, s.Loc)).Hours() / 24)
	subject := fmt.Sprintf("Friendly reminder: invoice for KES %d", inv.Amount)
	body := fmt.Sprintf("Hello %s,\n\nThis is a friendly reminder that our invoice for KES %d, due on %s, is now %d days overdue.\n\nIf you have already paid, please ignore this message. Otherwise, we would appreciate payment at your earliest convenience.\n\nThank you,\n%s\n", inv.Client, inv.Amount, inv.DueDate, daysLate, b.Name)
	if err := reminders.SendEmail(r.Context(), email, subject, body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.DB.Exec(`UPDATE clients SET email=? WHERE id=(SELECT client_id FROM invoices WHERE id=? AND business_id=?)`, email, id, b.ID); err != nil {
		log.Printf("save client email after reminder: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO client_reminder_log(invoice_id, sent_on) VALUES(?, ?)`, id, today); err != nil {
		log.Printf("record invoice reminder: %v", err)
		http.Error(w, "Email was sent, but Kalenda could not record the reminder status.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/invoices?reminder=sent", http.StatusSeeOther)
}
