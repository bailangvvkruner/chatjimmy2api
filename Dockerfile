# ── Build stage ──
FROM golang:alpine AS builder

RUN apk add --no-cache upx git ca-certificates

WORKDIR /app
COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w" \
    -trimpath \
    -o chatjimmy2api .

RUN upx --best --lzma chatjimmy2api

# ── Runtime stage ──
FROM scratch

COPY --from=builder /app/chatjimmy2api /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

EXPOSE 28094

ENTRYPOINT ["/chatjimmy2api"]
