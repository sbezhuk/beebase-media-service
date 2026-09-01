# beebase-media-service

Reusable file/media upload service for [BeeBase](https://github.com/sbezhuk/beebase-auth-service#trust-model),
an open-source backend for a beekeeper management application split into
microservices. See [CLAUDE.md](https://github.com/sbezhuk/beebase-auth-service/blob/main/CLAUDE.md)
for the architectural rules this service follows.

This service stores files (photos, PDFs, XML, and other documents)
attached to an entity owned by another service — currently an apiary or a
hive. It has no Apiary/Hive-specific logic baked in: `owner_type` +
`owner_id` are generic, so the same infrastructure can support more
entity types later without a schema change. It never trusts a client's
claimed file type or a client-supplied owner/user pairing — see
[Security](#security) and [Ownership](#ownership) below.

Related services: `beebase-auth-service` (users, refresh tokens, JWT
issuing), `beebase-apiary-service`, `beebase-hive-service`,
`beebase-gateway` (single entry point for clients).

This service is reachable through `beebase-gateway` at `/api/v1/media`,
same as every other backend service — see its docker-compose for the
full stack. **Not yet wired up** (deliberately, for now):
`beebase-apiary-service` and `beebase-hive-service` don't yet expose
their own "this apiary/hive has these photos" endpoints referencing this
service. This service is fully functional standalone or behind the
gateway either way; that integration is a follow-up.

## Requirements

- Go 1.27+
- PostgreSQL 16 (or Docker, to run it for you)
- [golang-migrate](https://github.com/golang-migrate/migrate) CLI, for applying
  migrations outside Docker: `make migrate-install`
- A running `beebase-auth-service` (or anything serving a compatible
  JWKS document) reachable at `AUTH_JWKS_URL`
- A running `beebase-apiary-service` reachable at `APIARY_SERVICE_URL`
- A running `beebase-hive-service` reachable at `HIVE_SERVICE_URL`

## Quick start

```bash
cp .env.example .env
# point AUTH_JWKS_URL, APIARY_SERVICE_URL, and HIVE_SERVICE_URL at
# running services, e.g. http://localhost:8081/.well-known/jwks.json,
# http://localhost:8082, and http://localhost:8083

# Option A: run Postgres in Docker, app on the host
docker compose up -d postgres
make migrate-up
make run

# Option B: run everything in Docker (migrations run once, automatically)
docker compose up --build
```

Verify it's up:

```bash
curl http://localhost:8080/health   # liveness — always 200 while the process is up
curl http://localhost:8080/ready    # readiness — 200 only if the database is reachable

TOKEN=...      # an access_token from auth-service's /api/v1/auth/register or /login
APIARY_ID=...  # an apiary that TOKEN's owner created via apiary-service

curl -X POST http://localhost:8080/api/v1/media \
  -H "Authorization: Bearer $TOKEN" \
  -F "owner_type=APIARY" -F "owner_id=$APIARY_ID" -F "file=@hive1.jpg"

curl "http://localhost:8080/api/v1/media?owner_type=APIARY&owner_id=$APIARY_ID" \
  -H "Authorization: Bearer $TOKEN"
```

The full API surface is documented in [api/openapi.yaml](api/openapi.yaml).

Note: this repo's `docker-compose.yml` is for standalone single-service
development only. To run the full BeeBase stack together, use
`beebase-gateway`'s docker-compose, which builds every service from
sibling checkouts and routes between them, including this one at
`/api/v1/media`.

## Configuration

All configuration is via environment variables (see
[.env.example](.env.example)):

| Variable                   | Default                    | Description                              |
| --------------------------- | --------------------------- | ----------------------------------------- |
| `APP_ENV`                  | `development`               | `development` or `production`             |
| `LOG_LEVEL`                 | `info`                       | `debug`, `info`, `warn`, `error`           |
| `HTTP_PORT`                 | `8080`                       | Port the HTTP server listens on           |
| `HTTP_READ_TIMEOUT`         | `5s`                         | Request read timeout                      |
| `HTTP_WRITE_TIMEOUT`        | `10s`                        | Response write timeout                    |
| `HTTP_IDLE_TIMEOUT`         | `60s`                        | Keep-alive idle timeout                   |
| `HTTP_SHUTDOWN_TIMEOUT`     | `15s`                        | Max time to wait for graceful shutdown    |
| `DATABASE_URL`              | *(required)*                 | PostgreSQL DSN                            |
| `DATABASE_CONNECT_TIMEOUT`  | `5s`                         | Timeout for the initial DB connection      |
| `AUTH_JWKS_URL`             | *(required)*                 | auth-service's public key endpoint, used to verify access tokens |
| `APIARY_SERVICE_URL`        | *(required)*                 | apiary-service's base URL, used to confirm apiary ownership on upload |
| `HIVE_SERVICE_URL`          | *(required)*                 | hive-service's base URL, used to confirm hive ownership on upload |
| `MAX_UPLOAD_SIZE_BYTES`     | `15728640` (15MB)            | Maximum size of a single uploaded file    |
| `TEST_DATABASE_URL`         | *(unset)*                    | Used only by `make test-integration`, never by the app |

## Project structure

```
cmd/server/                    entry point: wires config, logger, db, services, server
api/openapi.yaml                 API contract
migrations/                      SQL migrations (golang-migrate format)
internal/
  domain/media/                     Media entity, Repository port; no infrastructure dependency
  application/media/                 use cases: upload, get, download, list, delete;
                                     ApiaryVerifier/HiveVerifier ports (ownership checks)
  platform/apiaryclient/           ApiaryVerifier implemented by calling apiary-service over HTTP
  platform/hiveclient/             HiveVerifier implemented by calling hive-service over HTTP
  repository/postgres/             domain port implemented against PostgreSQL (pgx, explicit SQL);
                                     also owns file content storage (see Storage below)
  transport/http/                 chi router, health/ready handlers
    media/                            media HTTP handlers, request validation, responses
```

logger, JSON response/error helpers, the graceful-shutdown server wrapper,
and JWKS-based access-token verification (`RequireAuth` middleware) all
come from [beebase-common](https://github.com/sbezhuk/beebase-common),
shared by every BeeBase service.

## Ownership

A file belongs to exactly one owner (an apiary or a hive) and, through
it, one user. Ownership is enforced in two layers:

1. **On upload**, this service forwards the caller's own access token to
   apiary-service's or hive-service's `GET /api/v1/{apiaries,hives}/{id}`
   (whichever `owner_type` selects) and trusts the answer: a 200 means
   whoever holds that token owns that entity, a 404 means they don't (or
   it doesn't exist) — collapsed into the same `404 {apiary,hive}_not_found`
   response either way, so an entity's existence can't be probed. This
   service never queries apiary/hive ownership itself.
2. The verified owner's user ID is then denormalized onto the media row.
   Every later read/write (`Get`, `Download`, `List`, `Delete`) scopes its
   SQL by that `user_id` directly — no cross-service call is needed after
   upload, since `owner_id` is immutable and ownership can't change out
   from under a file. A request for another user's media returns the same
   `404 media_not_found` as one that doesn't exist, never a `403`.

Deletes are soft on the metadata row (`deleted_at` is set, the row is
retained) per the project's offline-sync plan — media is a synchronizable
entity — but the stored file content is removed immediately to reclaim
storage.

**Known limitation:** if an apiary or hive is deleted, its media here is
not cascade-deleted or notified — there's no event bus or outbox between
services yet (CLAUDE.md defers full synchronization). That media becomes
orphaned but remains independently accessible to its owner until this is
addressed.

## Storage

File bytes are stored directly in PostgreSQL — a `media_blobs` table,
kept separate from `media`'s own metadata columns so list/get queries
never touch blob data — gzip-compressed by the repository layer when that
actually shrinks the payload (skipped for already-compressed formats like
JPEG/PDF, which rarely benefit and would just pay gzip's framing
overhead). `GET /api/v1/media/{id}/download` is the stable, authenticated
URL a client fetches or displays a file from; content is proxied through
this service rather than a redirect to an object store, which also keeps
authorization uniform with every other endpoint (one ownership-scoped DB
lookup gates access).

This is a deliberate MVP choice: it avoids a paid S3/MinIO dependency and
any object-storage credentials to manage. The trade-off is that every
upload/download round-trips through PostgreSQL as a row read/write, so
it's worth revisiting if usage grows beyond MVP scale. Nothing outside
`internal/repository/postgres/media_repository.go` knows bytes are stored
this way — `domain/media.Repository`'s `Create`/`GetContent` signatures
just deal in `[]byte` — so swapping in an S3-backed implementation later
is a contained change, not a rewrite.

## Security

The service never trusts the client:

- **Authentication**: every `/api/v1/media/*` route requires a valid
  access token, verified against auth-service's JWKS.
- **Authorization**: see [Ownership](#ownership) above.
- **File type**: a file's extension must be on a hardcoded allowlist
  (jpg/jpeg/png/webp/heic, pdf, xml, txt, csv); where the format is
  reliably sniffable, the actual bytes are checked against
  `http.DetectContentType` and rejected on a mismatch (e.g. PNG magic
  bytes behind a `.pdf` filename). The stored `content_type` is always the
  canonical MIME the server derives, never the client's declared header.
- **File size**: capped by `MAX_UPLOAD_SIZE_BYTES`, enforced both at the
  HTTP layer (`http.MaxBytesReader`, so an oversized request is rejected
  without buffering it all) and again in the application layer (so the
  rule holds for any caller, not only ones going through HTTP).
- **Storage access**: there's no direct/unrestricted access to where
  bytes are stored — every read goes through the ownership-scoped
  `GET .../download` endpoint.

## Development

```bash
make run               # go run ./cmd/server
make fmt                # go fmt ./...
make vet                # go vet ./...
make test               # unit tests: go test ./...
make lint                # golangci-lint run

make migrate-up         # apply migrations to DATABASE_URL
make migrate-down       # roll back the last migration
make migrate-new name=add_something   # scaffold a new migration pair

make build              # build binary into bin/
```

### Integration tests

Integration tests exercise the PostgreSQL repository (including
compression round-trips) and the full HTTP upload/get/download/delete
flow — including a real JWKS round trip, fake apiary-service and
hive-service standing in for the real cross-service ownership checks, and
two independently authenticated users proving cross-user access is
impossible — against a real database. They're gated on
`TEST_DATABASE_URL` and skip themselves (not fail) if it's unset, and
every test runs inside a transaction that's rolled back afterward, so
they never leave rows behind or need manual cleanup.

```bash
docker compose up -d postgres
createdb -h localhost -p 5436 -U beebase beebase_media_test
migrate -path migrations -database "$TEST_DATABASE_URL" up

TEST_DATABASE_URL=postgres://beebase:beebase@localhost:5436/beebase_media_test?sslmode=disable \
  make test-integration
```
