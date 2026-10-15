package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "session"
const sessionDays = 14

type User struct {
	ID    int
	Email string
}

type AuthPage struct {
	Error string
	Email string
}

func setupAuth() {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			expires_at TEXT NOT NULL
		)`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			log.Fatal(err)
		}
	}
}

// newToken returns a random 64-character string that cannot be guessed.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func startSession(w http.ResponseWriter, userID int) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(sessionDays * 24 * time.Hour)

	_, err = db.Exec(
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		token, userID, expires.Format(time.RFC3339),
	)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   os.Getenv("COOKIE_SECURE") == "1",
	})
	return nil
}

// currentUser returns the logged-in user, or nil if nobody is logged in.
func currentUser(r *http.Request) *User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}

	var u User
	err = db.QueryRow(
		`SELECT u.id, u.email FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.token = ? AND s.expires_at > ?`,
		c.Value, time.Now().UTC().Format(time.RFC3339),
	).Scan(&u.ID, &u.Email)
	if err != nil {
		return nil
	}
	return &u
}

func signupPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		render(w, "signup.html", AuthPage{})
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")
	page := AuthPage{Email: email}

	if _, err := mail.ParseAddress(email); err != nil {
		page.Error = "Please enter a valid email address."
		render(w, "signup.html", page)
		return
	}
	if len(password) < 8 {
		page.Error = "Your password must be at least 8 characters."
		render(w, "signup.html", page)
		return
	}
	if password != confirm {
		page.Error = "The two passwords do not match."
		render(w, "signup.html", page)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Could not create account", http.StatusInternalServerError)
		return
	}

	res, err := db.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)",
		email, string(hash), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			page.Error = "An account with that email already exists."
			render(w, "signup.html", page)
			return
		}
		log.Println("signup error:", err)
		http.Error(w, "Could not create account", http.StatusInternalServerError)
		return
	}

	id, _ := res.LastInsertId()
	if err := startSession(w, int(id)); err != nil {
		http.Error(w, "Could not start session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func loginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		render(w, "login.html", AuthPage{})
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	page := AuthPage{Email: email, Error: "Wrong email or password."}

	var id int
	var hash string
	err := db.QueryRow("SELECT id, password_hash FROM users WHERE email = ?", email).Scan(&id, &hash)
	if err != nil {
		render(w, "login.html", page)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		render(w, "login.html", page)
		return
	}

	if err := startSession(w, id); err != nil {
		http.Error(w, "Could not start session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if c, err := r.Cookie(sessionCookie); err == nil {
			db.Exec("DELETE FROM sessions WHERE token = ?", c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
		})
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// userWriter carries the logged-in user alongside the response, so any page
// can show the right menu without changing every handler.
type userWriter struct {
	http.ResponseWriter
	user *User
}

func withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&userWriter{ResponseWriter: w, user: currentUser(r)}, r)
	})
}

func loggedInEmail(w http.ResponseWriter) string {
	if uw, ok := w.(*userWriter); ok && uw.user != nil {
		return uw.user.Email
	}
	return ""
}

// requireLogin sends anyone who is not logged in to the login page.
// Only the login page, sign-up page and static files stay public.
func requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isAuthPage := path == "/login" || path == "/signup"
		isPublic := isAuthPage || strings.HasPrefix(path, "/static/")
		loggedIn := loggedInEmail(w) != ""

		if !isPublic && !loggedIn {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if isAuthPage && loggedIn {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}