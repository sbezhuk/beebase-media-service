## Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

## Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    adduser -D -H -u 10001 beebase

COPY --from=builder /out/server /usr/local/bin/server

USER beebase

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/server"]
