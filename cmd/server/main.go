package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database so Africa/Nairobi works anywhere

	"kalenda/internal/invoices"
	"kalenda/internal/reminders"
	"kalenda/internal/rules"
	"kalenda/internal/store"
	"kalenda/internal/web"
)

func main() {
	addr := ":" + env("PORT", "8080")
	dbPath := env("DB_PATH", "kalenda.db")

	loc, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		loc = time.FixedZone("EAT", 3*3600)
	}

	rs, err := rules.Load()
	if err != nil {
		log.Fatalf("rules: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	if err := rules.SyncToDB(db, rs); err != nil {
		log.Fatalf("sync rules: %v", err)
	}

	srv, err := web.New(db, rs, loc)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	srv.Extra = invoices.New(db).Timeline

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sender reminders.Sender = reminders.LogSender{}
	if smtpSender, ok := reminders.SMTPFromEnv(); ok {
		sender = smtpSender
	}
	go reminders.Run(ctx, db, sender, time.Minute)
	go func() {
		for {
			if err := reminders.TopUp(ctx, db, rs, loc); err != nil {
				log.Printf("top-up: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx)
	}()

	log.Printf("Kalenda listening on %s (db: %s)", addr, dbPath)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
