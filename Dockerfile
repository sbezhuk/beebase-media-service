## Build stage
FROM golang:1.27-alpine AS builder

WORKDIR /src
RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN --mount=type=secret,id=github_token,required=true \
    GOPRIVATE=github.com/sbezhuk/beebase-common \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/github_token)@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download

COPY . .

# TARGETOS/TARGETARCH are populated automatically by BuildKit to match
# the requested --platform (e.g. `docker buildx build --platform
# linux/arm64`); with no --platform given they default to the host's own
# platform, so a plain local `docker build`/`docker compose build` is
# unaffected and keeps building for the machine it runs on.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

## Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    adduser -D -H -u 10001 beebase

COPY --from=builder /out/server /usr/local/bin/server

USER beebase

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/server"]
