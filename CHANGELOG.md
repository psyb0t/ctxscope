# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

## v1.0.0 — 2026-08-08

First release. This package was `github.com/psyb0t/common-go/scope`; it now
lives on its own so it can ship on its own schedule.

The starting version is `v1.0.0` rather than a continuation of common-go's
`v0.3.x` because a changed module path is a different module, not a new major
of the old one — the version sequence starts fresh.

- **Extracted from `common-go`.** The API is unchanged apart from the package
  name: `scope.Set` → `ctxscope.Set`, and so on for the whole surface. The
  motivation is release coupling, not dependencies — Go's module-graph pruning
  already kept common-go's gorm/echo/NATS/Temporal requirements out of a
  scope-only build. What it did not do was let a change to the scope package
  ship without also shipping whatever else in common-go happened to be in
  flight, or spare five importing repos from version bumps driven entirely by
  code they never compile.
- **New: `NewHandler`.** A `slog.Handler` that reads the scope off the context
  passed to `Handle` and applies it to every record, so plain
  `slog.InfoContext(ctx, ...)` carries the attributes — including from library
  code that has never heard of this package, which `GetLogger` cannot reach.
  Install it once at startup with `slog.SetDefault`.

  It replays `WithAttrs`/`WithGroup` calls *after* applying the scope, so scope
  attributes always land at the record's top level. Adding them to the record
  instead would nest them inside any open group, and a `request_id` under a
  group is not the one log queries match on.

  `NewHandler` and `GetLogger` are alternatives, not layers — `GetLogger`
  applies the scope itself, so using both emits every attribute twice.
- Stdlib-only apart from `ctxerrors`, and that is enforced by the module
  boundary now rather than by a note in a document. Transport adapters (Temporal
  `ContextPropagator`, NATS header injector, HTTP middleware) depend on this
  package, never the reverse.
