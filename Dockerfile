
FROM golang:1.27.2-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o moneycalendar .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/moneycalendar ./moneycalendar
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/static ./static
COPY --from=builder /app/rules.json ./rules.json

RUN mkdir -p /app/data

ENV DB_PATH=/app/data/moneycalendar.db
ENV PORT=8080

EXPOSE 8080

CMD ["./moneycalendar"]
