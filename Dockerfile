FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kalenda ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache su-exec \
    && adduser -D -u 10001 kalenda
WORKDIR /app
COPY --from=build /out/kalenda /app/kalenda
ENV PORT=8080 DB_PATH=/data/kalenda.db
EXPOSE 8080
ENTRYPOINT ["/bin/sh", "-c", "mkdir -p /data && chown kalenda:kalenda /data && exec su-exec kalenda /app/kalenda"]
