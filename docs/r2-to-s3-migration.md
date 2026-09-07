# R2 → S3 media migration procedure

Exact, step-by-step procedure for migrating BeeBase's production media
objects from Cloudflare R2 to Amazon S3, using `cmd/migrate-r2-to-s3`
(see that command's package doc for how copy/verify work). This was
written and the tool built/tested without access to the real production
R2 bucket - run it for real by following this document once credentials
are available. **Nothing here deletes or modifies anything in R2.**

## Prerequisites

1. The S3 bucket exists (`terraform apply` in `terraform/` has run - see
   the S3 Terraform module in the deployment report) and the migration
   operator's AWS credentials/role can write to it.
2. R2 credentials for the existing production bucket: `SOURCE_R2_ENDPOINT`,
   `SOURCE_R2_BUCKET`, `SOURCE_R2_ACCESS_KEY_ID`, `SOURCE_R2_SECRET_ACCESS_KEY`
   (the same values already in production's `R2_*` configuration today).
3. Run this from a machine with network access to both R2 and S3 - a
   developer laptop is fine for the copy/verify steps below; it does not
   need to be the EC2 host.

## Step 1 — Copy (safe to run ahead of time, repeatable)

```bash
cd beebase-media-service

export SOURCE_R2_ENDPOINT="https://<account-hash>.r2.cloudflarestorage.com"
export SOURCE_R2_BUCKET="beebase-media"            # today's production R2 bucket name
export SOURCE_R2_ACCESS_KEY_ID="<production R2 access key>"
export SOURCE_R2_SECRET_ACCESS_KEY="<production R2 secret key>"

export DEST_STORAGE_BUCKET="beebase-media-prod"    # the new S3 bucket
export DEST_STORAGE_REGION="eu-central-1"          # match the EC2/Terraform region
# Leave DEST_STORAGE_ACCESS_KEY_ID/DEST_STORAGE_SECRET_ACCESS_KEY unset if
# your local AWS CLI is already authenticated as a principal with write
# access to the bucket (the tool falls back to the SDK's default
# credential chain, same as the service itself in production).

go run ./cmd/migrate-r2-to-s3 -mode=copy | tee copy-report.txt
```

This enumerates every object in the R2 bucket and copies it to S3 with
the exact same key and content type, using a conditional
("create if absent") write - so it's always safe to re-run: anything
already copied is reported as "already at destination" and skipped, not
overwritten. If it's interrupted partway (network blip, laptop sleeps),
just run the same command again.

Expect the final report to show `failed: 0`. Investigate anything in the
`FAILED` list before proceeding - do not move to Step 2 with unresolved
failures.

## Step 2 — Verify

```bash
go run ./cmd/migrate-r2-to-s3 -mode=verify | tee verify-report.txt
```

This re-lists every R2 object and compares it against its S3 counterpart
by size and ETag (`HeadObject` on both sides - no data is re-transferred).
It exits non-zero and lists every mismatch if:

- an object exists in R2 but is missing from S3 (copy step incomplete), or
- an object exists in both but its size or ETag differs (content corruption
  or an unexpected concurrent write to one side).

**Do not proceed to Step 3 unless `verify` reports zero mismatches.** If
it doesn't, re-run `-mode=copy` (idempotent - see above) and `-mode=verify`
again.

Additionally, spot-check a handful of representative objects by hand
(pick a few media IDs referenced by real `media` rows in production
PostgreSQL - `SELECT id, content_type, size_bytes FROM media ORDER BY
random() LIMIT 5;` - and compare against what `verify`'s per-object
detail shows) and confirm the object count matches PostgreSQL's own
count of active media rows:

```sql
SELECT count(*) FROM media WHERE deleted_at IS NULL;
```

against the `total source objects` line in the copy/verify report (they
should be very close - `media` rows can lag slightly behind the object
count for rows soft-deleted after the R2 object was already removed by
the service's own best-effort delete, which is expected and harmless).

## Step 3 — Cut production over to S3

Only after Step 2 reports zero mismatches:

1. Update production configuration: set `STORAGE_BUCKET`/`STORAGE_REGION`
   (and leave `STORAGE_ENDPOINT`/`STORAGE_ACCESS_KEY_ID`/
   `STORAGE_SECRET_ACCESS_KEY` unset, so the service authenticates via
   the EC2 instance's IAM role) and remove the old `R2_*` variables - see
   the deployment report's exact SSM Parameter Store changes.
2. Redeploy media-service so it picks up the new configuration.
3. Run through the [Backward Compatibility](#post-cutover-smoke-test)
   checklist below against production before considering the cutover
   complete.
4. **Do not delete the R2 bucket or its objects yet.** Keep it as a cold
   fallback for at least one full backup/verification cycle after
   cutover (see the deployment report). Deleting R2 data is a separate,
   explicit, manually-approved action for later - never part of this
   procedure.

## Post-cutover smoke test

Through the running gateway, using a real (test) account:

1. **Upload**: `POST /api/v1/media` with a small image → expect `201` and
   a `media` object with a working `image_url`.
2. **Download**: `GET` that `image_url` → expect the same bytes back,
   correct `Content-Type`.
3. **Delete**: `DELETE /api/v1/media/{id}` → expect `204`, then a
   follow-up `GET` on the same `image_url` → expect `404`.
4. **Attach to an apiary**: create/attach media via apiary-service's
   image endpoints → confirm it appears in the apiary's `images`.
5. **Attach to a hive**: same, via hive-service.
6. **Attach to an inspection**: same, via inspection-service.
7. **Pre-existing media**: pick a media id that existed *before* the
   migration (referenced by an apiary/hive/inspection created before
   cutover) and confirm its `image_url` still resolves correctly - this
   is the concrete proof that the object-key scheme carried over and no
   database migration was needed.
8. **Authorization**: as a second account, attempt to `GET`/`DELETE` the
   first account's media → expect `403`/`404` (whichever the existing
   ownership check already returns - unchanged by this migration).

## Rollback

If anything in the smoke test fails, revert production's `STORAGE_*`
configuration back to `R2_*` and redeploy - the code path for R2 access
still works identically (it's the same `blobstore.Store`, just configured
with `STORAGE_ENDPOINT`/`STORAGE_FORCE_PATH_STYLE` pointed at R2 again),
and no R2 data was ever touched, so the rollback is a pure configuration
revert with no data-recovery step needed.
