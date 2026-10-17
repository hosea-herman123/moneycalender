// Package auth handles signup, login and cookie sessions.
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const SessionTTL = 14 * 24 * time.Hour

type User struct {
	ID    int64
	Email string
}

// UserError is safe to show to the person using the site.
type UserError string

func (e UserError) Error() string { return string(e) }

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) CreateUser(email, password string) (int64, error) {
	if len(email) > 254 || !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n") {
		return 0, UserError("Please enter a valid email address.")
	}
	if len(password) < 8 {
		return 0, UserError("Password must be at least 8 characters.")
	}
	if len(password) > 72 { // bcrypt silently truncates beyond 72 bytes
		return 0, UserError("Password must be at most 72 characters.")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO users(email, password_hash) VALUES(?, ?)`, email, string(hash))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, UserError("That email is already registered. Try logging in.")
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Authenticate(email, password string) (int64, error) {
	bad := UserError("Wrong email or password.")
	var id int64
	var hash string
	err := s.db.QueryRow(`SELECT id, password_hash FROM users WHERE email = ?`, email).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, bad
	}
	if err != nil {
		return 0, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return 0, bad
	}
	return id, nil
}

func (s *Store) NewSession(userID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	exp := time.Now().Add(SessionTTL).UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO sessions(token, user_id, expires_at) VALUES(?, ?, ?)`, token, userID, exp)
	return token, err
}

// UserFor returns the user for a session token, or ok=false if missing/expired.
func (s *Store) UserFor(token string) (User, bool) {
	var u User
	err := s.db.QueryRow(`
		SELECT u.id, u.email FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ?`,
		token, time.Now().UTC().Format(time.RFC3339)).Scan(&u.ID, &u.Email)
	return u, err == nil
}

func (s *Store) DeleteSession(token string) {
	s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
}
