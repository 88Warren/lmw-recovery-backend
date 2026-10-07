# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /app/bin/lmw-server \
    ./cmd/server/main.go

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates for outbound HTTPS (Brevo, Stripe)
# tzdata for Europe/London timezone in scheduler
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/bin/lmw-server .

# DB file lives on a mounted PersistentVolume at /data
# APP_ENV and all other config come from Kubernetes ConfigMap / Secrets
ENV DB_PATH=/data/lmw.db
ENV APP_ENV=production

EXPOSE 8080

ENTRYPOINT ["/app/lmw-server"]
