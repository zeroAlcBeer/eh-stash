# Build context is the repo root (needs both scraper-go/ and migrations/).
# ── builder ────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY scraper-go/go.mod scraper-go/go.sum ./
RUN go mod download

COPY scraper-go/ .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /scraper .

# ── runtime ────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /scraper /usr/local/bin/scraper
COPY migrations /app/migrations

CMD ["scraper"]
