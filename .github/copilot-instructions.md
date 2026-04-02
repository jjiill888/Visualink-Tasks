# Project Guidelines

## Architecture
- This is a Go 1.22 server-rendered web app with chi routing, SQLite storage, and HTMX/SSE-driven partial updates.
- Keep responsibilities aligned with the current package boundaries: `internal/handler` for HTTP flow, `internal/db` for persistence, `internal/model` for domain structs and display helpers, `internal/session` for auth/session helpers, and `internal/hub` for SSE broadcasts.
- Prefer extending the existing handler + db patterns over adding new layers or abstractions.

## Build And Test
- Use `go run .` for local development and `go build .` to verify the app still compiles.
- Docker workflow is documented in `README.md` and uses `docker compose up -d --build`.
- There is no automated test suite in this repo today. After behavior changes, at minimum run `go build .` and describe any manual validation you performed.
- The app defaults to `DB_PATH=./data/app.db`; Docker uses `/app/data/app.db`.

## Conventions
- Follow the existing chi routing and handler patterns in `main.go` and `internal/handler/*`.
- When adding or renaming templates, update the template registration in `buildTmplMap()` or `buildPartialTmpl()` in `main.go`. Missing registrations fail at runtime.
- Reuse the existing auth and rendering flow (`RequireAuth`, `UserFromContext`, `render`, `redirect`) instead of introducing parallel patterns.
- SQLite is intentionally opened with a single connection. Avoid long-running transactions and unnecessary write amplification.
- Time display is centered on Asia/Shanghai helpers in `internal/model/models.go`; preserve that behavior unless the task explicitly changes timezone handling.
- Static assets under `static/` are served with long-lived immutable cache headers. Frontend asset changes should assume a rebuild/redeploy path.