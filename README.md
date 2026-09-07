# beebase-media-service

Reusable file/media upload service for [BeeBase](https://github.com/sbezhuk/beebase-auth-service#trust-model),
an open-source backend for a beekeeper management application split into
microservices. See [CLAUDE.md](https://github.com/sbezhuk/beebase-auth-service/blob/main/CLAUDE.md)
for the architectural rules this service follows.

This service stores files (photos, PDFs, XML, and other documents),
uploaded independently of any owner and optionally attached afterward to
an entity owned by another service — currently an apiary or a hive. It
has no Apiary/Hive-specific logic baked in: `owner_type` + `owner_id` are
generic, so the same infrastructure can support more entity types later
without a schema change. It never trusts a client's claimed file type or
a client-supplied owner/user pairing — see [Security](#security) and
[Ownership](#ownership) below.

Related services: `beebase-auth-service` (users, refresh tokens, JWT
issuing), `beebase-apiary-service`, `beebase-hive-service`,
`beebase-gateway` (single entry point for clients).

This service is reachable through `beebase-gateway` at `/api/v1/media`,
same as every other backend service — see its docker-compose for the
full stack. `beebase-apiary-service` and `beebase-hive-service` each
expose an `images: []` field (on GET and PUT) that reflects and manages
which media is attached to a given apiary/hive, backed by this service's
own attach/list endpoints.

## Requirements

- Go 1.27+
- PostgreSQL 16 (or Docker, to run it for you)
- [golang-migrate](https://github.com/golang-migrate/migrate) CLI, for applying
  migrations outside Docker: `make migrate-install`
- A running `beebase-auth-service` (or anything serving a compatible
  JWKS document) reachable at `AUTH_JWKS_URL`
- A running `beebase-apiary-service` reachable at `APIARY_SERVICE_URL`
- A running `beebase-hive-service` reachable at `HIVE_SERVICE_URL`
- An Amazon S3 bucket (`STORAGE_BUCKET`, `STORAGE_REGION`) — see
  [Storage](#storage)

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

# Upload: no apiary/hive needed yet.
MEDIA_ID=$(curl -s -X POST http://localhost:8080/api/v1/media \
  -H "Authorization: Bearer $TOKEN" -F "file=@hive1.jpg" | jq -r .id)

# Attach: links it to an apiary the caller owns.
curl -X POST "http://localhost:8080/api/v1/media/$MEDIA_ID/attach" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"owner_type\":\"APIARY\",\"owner_id\":\"$APIARY_ID\"}"

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
[.env.example](.env.example) for the full list — it is a template only,
never read by the app, Docker Compose, or deployment tooling; copy it
once to create your real `.env`, which is what actually gets loaded).
Production configuration is generated at deploy time from AWS SSM
Parameter Store (see `beebase-gateway/deploy/deploy.sh`) — `.env.example`
is never used as a fallback, in development or in production.

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
| `PUBLIC_BASE_URL`           | *(required)*                 | Gateway's externally reachable base URL, used to build each item's `image_url` |
| `APIARY_SERVICE_URL`        | *(required)*                 | apiary-service's base URL, used to confirm apiary ownership on attach |
| `HIVE_SERVICE_URL`          | *(required)*                 | hive-service's base URL, used to confirm hive ownership on attach |
| `MAX_UPLOAD_SIZE_BYTES`     | `15728640` (15MB)            | Maximum size of a single uploaded file    |
| `STORAGE_BUCKET`            | *(required)*                 | S3 bucket file content is stored in       |
| `STORAGE_REGION`            | *(required)*                 | AWS region the bucket lives in            |
| `STORAGE_CONNECT_TIMEOUT`   | `5s`                         | Timeout for the initial storage connectivity check |
| `TEST_DATABASE_URL`         | *(unset)*                    | Used only by `make test-integration`, never by the app |

## Project structure

```
cmd/server/                    entry point: wires config, logger, db, blobstore, services, server
api/openapi.yaml                 API contract
migrations/                      SQL migrations (golang-migrate format)
internal/
  domain/media/                     Media entity, Repository + BlobStore ports; no infrastructure dependency
  application/media/                 use cases: upload, attach, get, download, list, delete;
                                     ApiaryVerifier/HiveVerifier ports (ownership checks)
  platform/blobstore/              BlobStore implemented against Amazon S3 (see Storage below)
  repository/postgres/             media metadata (media table) against PostgreSQL (pgx, explicit
                                      SQL)
  repository/media/                composes the postgres metadata store + blobstore.Store into the
                                      full domain/media.Repository port - see Storage below
  transport/http/                 chi router, health/ready handlers
    media/                            media HTTP handlers, request validation, responses
```

logger, JSON response/error helpers, the graceful-shutdown server wrapper,
and JWKS-based access-token verification (`RequireAuth` middleware) all
come from [beebase-common](https://github.com/sbezhuk/beebase-common),
shared by every BeeBase service.

## Ownership

A file always belongs to exactly one user (whoever uploaded it), and
optionally, once attached, to one owner (an apiary or a hive) as well.
Ownership is enforced in two layers:

1. **On upload**, the caller's user ID (from their verified access token)
   is set directly on the media row — no apiary or hive is involved yet,
   so no cross-service call happens here. `owner_type`/`owner_id` are both
   `null` until the file is attached.
2. **On attach** (`POST /api/v1/media/{id}/attach`), this service forwards
   the caller's own access token to apiary-service's or hive-service's
   `GET /api/v1/{apiaries,hives}/{id}` (whichever `owner_type` selects)
   and trusts the answer: a 200 means whoever holds that token owns that
   entity, a 404 means they don't (or it doesn't exist) — collapsed into
   the same `404 {apiary,hive}_not_found` response either way, so an
   entity's existence can't be probed. This service never queries
   apiary/hive ownership itself. Once set, `owner_type`/`owner_id` are
   immutable: attach is idempotent for the *same* owner (a safe retry),
   but attaching media already linked to a *different* owner fails with
   `409 already_attached` — there's no "move" operation.

Every read/write after attach (`Get`, `Download`, `List`, `Delete`) scopes
its SQL by `user_id` directly — no cross-service call is needed on those,
since ownership can't change out from under a file once attached. A
request for another user's media returns the same `404 media_not_found`
as one that doesn't exist, never a `403`.

Deletes are soft on the metadata row (`deleted_at` is set, the row is
retained) per the project's offline-sync plan — media is a synchronizable
entity — but the stored file content is removed immediately to reclaim
storage.

**Known limitations:**
- If an apiary or hive is deleted, its media here is cascade-deleted via
  `DELETE /api/v1/media?owner_type=&owner_id=`, called by
  apiary-service/hive-service as part of their own delete — but this
  is a best-effort HTTP call, not a distributed transaction, so a crash
  mid-cascade can still leave orphaned media (CLAUDE.md defers full
  synchronization; there's no event bus or outbox yet).
- Media that's uploaded but never attached to anything has no cleanup
  path yet — it stays independently accessible to its uploader
  indefinitely. A TTL-based sweep for long-unattached uploads is a
  reasonable follow-up if this becomes a real storage concern.
- Keeping a stored object and its metadata row in sync across two
  separate systems is best-effort, not transactional (see
  [Storage](#storage)): a failure at exactly the wrong moment (the
  storage delete fails right after its metadata row is gone, or the
  reverse during Create) can leave an orphaned, unreferenced object
  behind. It's logged when it happens, and it's harmless — nothing can
  ever reach it without a metadata row pointing at it — but there's no
  automated sweep for it yet.

## Storage

File content is stored in **Amazon S3** (see
[internal/platform/blobstore](internal/platform/blobstore)); media
metadata (filename, content type, size, timestamps) stays in PostgreSQL
exactly as before, in the `media` table.
`GET /api/v1/media/{id}/download` is the stable, authenticated URL a
client fetches or displays a file from; content is proxied through this
service rather than a redirect to the bucket, which keeps authorization
uniform with every other endpoint (one ownership-scoped DB lookup gates
access) and means no storage-specific detail (bucket, object key,
endpoint, a public URL) is ever exposed through the API. Every response
that includes a media item (this service's own `Response`, and apiary-
service's/hive-service's `images`) carries a ready-to-use `image_url`
pointing at this route, built by the shared `beebase-common/medialink`
package from `PUBLIC_BASE_URL` - a client never constructs this URL
itself from a raw id.

A media row's object key is derived deterministically from its id
(`internal/platform/blobstore`'s `ObjectKey`, currently `media/<id>`) —
Media ID → Object Key → Media Binary — so no extra database column is
needed to remember where a file lives, and retries are inherently
idempotent per id.

**Write ordering.** `internal/repository/media.Repository` composes the
PostgreSQL metadata store and the `BlobStore` into the
`domain/media.Repository` port every use case depends on:

- **Create** uploads to storage first, via a conditional ("create if
  absent") `PutObject`, and only persists the metadata row once that
  succeeds — so a media row can never exist without its content actually
  being stored, and a colliding client-supplied id can never silently
  overwrite someone else's bytes. If the metadata write then fails, the
  just-uploaded object is deleted best-effort so nothing is left
  orphaned without a reason.
- **Delete/DeleteByIDs** remove the metadata row(s) first, then
  best-effort delete the matching object(s) — so a media id is
  immediately gone from every caller's point of view even if the storage
  delete itself is briefly unreachable (logged for later cleanup rather
  than failing the request).

**Storage provider history.** File content previously stored in
PostgreSQL was migrated to object storage (BEEB-32); the legacy
`media_blobs` table was dropped via migration `000006_drop_media_blobs`.
All reads and writes go directly to the configured `BlobStore`; there
has never been an in-database blob fallback since that migration.

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

Integration tests exercise the PostgreSQL metadata repository and the full
HTTP upload/attach/get/download/delete flow — including a real JWKS round
trip, fake apiary-service and hive-service standing in for the real
cross-service ownership checks, and two independently authenticated users
proving cross-user access is impossible — against a real database. Object
storage itself is stood in for by an in-memory `BlobStore` fake in these
tests (see `internal/repository/media` for the unit tests that exercise
storage failure handling specifically, against fakes, and
`internal/platform/blobstore` for tests of the real S3 client against a
fake S3-compatible HTTP server); there's no test against a live S3
bucket. They're gated on `TEST_DATABASE_URL` and
skip themselves (not fail) if it's unset, and every test runs inside a
transaction that's rolled back afterward, so they never leave rows behind
or need manual cleanup.

```bash
docker compose up -d postgres
createdb -h localhost -p 5436 -U beebase beebase_media_test
migrate -path migrations -database "$TEST_DATABASE_URL" up

TEST_DATABASE_URL=postgres://beebase:beebase@localhost:5436/beebase_media_test?sslmode=disable \
  make test-integration
```
