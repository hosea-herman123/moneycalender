# Kalenda

**A calmer way for Kenyan small businesses to keep up with important dates.**

Kalenda brings compliance deadlines, invoice follow ups and a simple cash forecast into one business calendar. Create a profile, see the rules that apply to your business, and receive email reminders when delivery is configured.

## What it does

- Signup, login and business onboarding
- A 90 day calendar of applicable deadlines with plain language notes and official links
- Seven day and one day email reminders for deadlines
- Invoice tracking, overdue client email reminders and payment history
- A simple projected cash balance using owner supplied estimates
- SQLite storage and a Docker image suitable for a persistent volume

The calendar is a planning aid, not tax or legal advice. Rules remain visibly unverified until an independent reviewer checks their dates, applicability and penalties against official sources.

## Run locally

Requires Go 1.26 or later.

```sh
go test ./...
go run ./cmd/server
```

Open `http://localhost:8080`. The app creates its SQLite database at `kalenda.db` by default. Set `PORT` or `DB_PATH` to change those defaults.

To use SMTP, copy `.env.example` to `.env`, set the SMTP values, then run `bash run.sh`. Keep `.env` private; it is ignored by Git. SMTP settings are `SMTP_HOST`, `SMTP_PORT` (default `587`), `SMTP_USER`, `SMTP_PASS` and optional `SMTP_FROM` (defaults to `SMTP_USER`). Without SMTP configuration, queued deadline reminders are logged to the server output and email sends are unavailable.

## Run with Docker

Build and start the container with a persistent directory mounted at `/data`:

```sh
docker build -t kalenda .
docker run --rm -p 8080:8080 -v kalenda-data:/data \
  -e DB_PATH=/data/kalenda.db \
  -e SMTP_HOST -e SMTP_PORT -e SMTP_USER -e SMTP_PASS -e SMTP_FROM \
  kalenda
```

Set SMTP values in your hosting provider’s secret or environment settings. Back up the persistent volume regularly. The app is not deployed to a live host yet.

## Email reminders

The scheduler polls pending rows in `reminders`, sends using the configured SMTP sender, and records each result as sent or failed. Signup and profile updates schedule deadline reminders seven days and one day before each upcoming deadline. The scheduler runs inside the server process, so keep that process running. Failed reminders are recorded and are not retried automatically.

Overdue invoice reminders are sent directly from the invoice page and the send date is recorded in the invoice history.

## Project structure

```text
cmd/server/                 application startup and scheduler wiring
internal/auth/              password hashing and cookie sessions
internal/invoices/          business scoped invoices and timeline items
internal/reminders/         scheduling, SMTP delivery and reminder queue
internal/rules/             rule loading, applicability and recurrence
internal/rules/data/        curated obligation rules (YAML)
internal/store/             SQLite setup and migrations
internal/timeline/          shared calendar item model
internal/web/               HTTP handlers, templates and embedded assets
```

## Rule review before real use

Every rule needs a second reviewer to confirm the deadline, applicability, penalty and official source. Entries without a `last_verified` date are marked as unverified in the calendar. County permit dates vary. Some deadlines depend on working days, holidays, registration dates or accounting periods and are not yet modelled precisely. Do not rely on the calendar as the sole source for a filing or payment deadline.

## Current limitations

- No password reset, rate limiting or CSRF tokens beyond `SameSite=Lax` cookies
- Weekend and public holiday adjustments are not modelled
- Some anniversary or accounting period rules need additional business profile fields
- Failed reminder emails are not retried automatically
- SQLite requires a persistent volume and a backup plan when hosted
