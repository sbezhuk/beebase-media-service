## Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Build context is the repo root so the local beebase-common replace
# directive in go.mod (../beebase-common) resolves inside the image too.
COPY beebase-media-service/go.mod beebase-media-service/go.sum ./
COPY beebase-common /beebase-common
RUN go mod download

COPY beebase-media-service/. .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

## Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    adduser -D -H -u 10001 beebase

COPY --from=builder /out/server /usr/local/bin/server

USER beebase

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/server"]
