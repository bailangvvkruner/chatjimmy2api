# ── Build stage ──
FROM golang:alpine AS builder

RUN apk add --no-cache upx git

WORKDIR /app
COPY go.mod go.sum ./
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

EXPOSE 28094

ENTRYPOINT ["/chatjimmy2api"]
