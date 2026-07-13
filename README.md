# digitaletude-api

Go API for the digitaletude portfolio. Spec lives in the frontend repo under `plans/` (00-foundations, 01-photography, 02-music, 03-blog); build progress in that repo's `CONTEXT.md`.

## Run

```sh
cp .env.example .env   # fill in Supabase values
go run ./cmd/api       # listens on :8080
```

The frontend dev server (`yarn dev` in the `digitaletude` repo) proxies `/api` here.

## Verify

```sh
go build ./... && go vet ./... && go test ./...
```

## Migrations

Plain SQL in `migrations/`, golang-migrate naming. Run with [golang-migrate](https://github.com/golang-migrate/migrate) against `DATABASE_URL`, or paste the `*.up.sql` files into the Supabase SQL editor in order.

## Deploy

Dockerfile builds a static binary on distroless. Health check: `GET /healthz`. Graceful shutdown on SIGTERM (k8s-ready per the eventual AWS plan).
