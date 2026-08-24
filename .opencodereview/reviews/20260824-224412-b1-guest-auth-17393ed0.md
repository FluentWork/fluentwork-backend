# OpenCodeReview export — B1 guest auth / app-server skeleton

- Session: `17393ed0-96b9-4702-8f52-d3cd086805ab`
- Branch: `feature/b1-guest-auth-and-merge`
- Exported for local visibility under `.opencodereview/reviews/`
- Author disposition after review:
  - Fixed: HTTP timeouts / shutdown Close / store closer logging
  - Fixed: Bearer parsing, merge actor checks, merge token revoke order
  - Fixed: MarkMerged target RowsAffected, device_id duplicate mapping, parseTime DSN
  - Fixed: memory device rebinding, JWT secret outside development, readiness timeout
  - Fixed: request-id length cap, ErrAbortHandler re-panic, access-log path/route keys, HTTP status normalize
  - Deferred: mongo-driver indirect via gin (cannot remove via tidy), pool/config knobs, typed UserStatus, golangci major-version pin in script

---


─── go.mod:36-36 ───
[maintainability · medium] The MongoDB driver (`go.mongodb.org/mongo-driver/v2 v2.5.0`) is listed as
an indirect dependency, but no code in this repository imports or uses MongoDB — the project
persists data via MySQL (`github.com/go-sql-driver/mysql`), and none of the direct dependencies
(gin, mysql driver, jwt, uuid) transitively require mongo-driver. This looks like a leftover from
`go get` experimentation. Removing this line and running `go mod tidy` will keep the module graph
minimal and avoid bloating builds/go.sum with an unused database driver.



─── cmd/app-server/main.go:49-53 ───
[security · medium] The http.Server only sets ReadHeaderTimeout. ReadTimeout, WriteTimeout and
IdleTimeout stay at zero, so a client can send headers quickly and then stall the request body (or
never read a response / hold keep-alive connections open) indefinitely, tying up handler goroutines
and connections. Set explicit deadlines to bound resource usage; tune the values to the API's
expected latency.

  	httpServer := &http.Server{
  		Addr:              cfg.HTTPAddr,
  		Handler:           server.Handler(),
  		ReadHeaderTimeout: 5 * time.Second,
+ 		ReadTimeout:       15 * time.Second,
+ 		WriteTimeout:      15 * time.Second,
+ 		IdleTimeout:       60 * time.Second,
  	}


─── cmd/app-server/main.go:69-72 ───
[bug · low] If Shutdown exceeds the 10s deadline it returns context.DeadlineExceeded, and this error
is returned directly without calling httpServer.Close(). That turns a graceful-shutdown timeout into
a non-zero exit while leaving connections to be torn down only by process termination. Add a
forced-close fallback so shutdown completes deterministically.

  	case <-ctx.Done():
  		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
  		defer cancel()
- 		return httpServer.Shutdown(shutdownCtx)
+ 		if err := httpServer.Shutdown(shutdownCtx); err != nil {
+ 			_ = httpServer.Close()
+ 			return fmt.Errorf("graceful shutdown: %w", err)
+ 		}
+ 		return nil


─── cmd/app-server/main.go:44-44 ───
[maintainability · low] The closer returned by account.OpenStore (db.Close for MySQL) has its error
silently discarded. A close failure can hide leaked connections and remove diagnostic information
during shutdown. Log the error instead of ignoring it.

- 	defer func() { _ = closer() }()
+ 	defer func() {
+ 		if err := closer(); err != nil {
+ 			logger.Error("closing account store", "err", err)
+ 		}
+ 	}()


─── internal/account/http.go:72-73 ───
[maintainability · low] The `ok` values from both `c.Get` and the type assertion are discarded. If
the `RequireRegistered` middleware is ever not applied to this route, or the value stored under
`ContextUserIDKey` is not a string, `actorID` silently becomes `""` and the request still proceeds
to `Merge` with an empty actor id (only to be rejected later as "invalid access token"). Relying on
the implicit middleware side effect makes the failure mode non-obvious. Consider checking the `ok`
values and returning an explicit unauthenticated error instead of silently continuing.

- 	userID, _ := c.Get(ContextUserIDKey)
- 	actorID, _ := userID.(string)
+ 	userID, ok := c.Get(ContextUserIDKey)
+ 	if !ok {
+ 		httpjson.Error(c, apierr.Unauthenticated("missing authenticated user"))
+ 		return
+ 	}
+ 	actorID, ok := userID.(string)
+ 	if !ok || actorID == "" {
+ 		httpjson.Error(c, apierr.Unauthenticated("invalid authenticated user"))
+ 		return
+ 	}


─── internal/account/http.go:35-36 ───
[bug · low] `strings.CutPrefix(header, "Bearer ")` is case-sensitive, but RFC 7235 defines the
auth-scheme token as case-insensitive. Clients that send `bearer` or `BEARER` (which are valid per
spec) will be rejected as unauthenticated. Consider parsing the scheme case-insensitively (e.g.,
split on the first space and compare with `strings.EqualFold`).

- 		token, ok := strings.CutPrefix(header, "Bearer ")
- 		token = strings.TrimSpace(token)
+ 		parts := strings.SplitN(header, " ", 2)
+ 		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
+ 			httpjson.Error(c, apierr.Unauthenticated("missing bearer token"))
+ 			return
+ 		}
+ 		token := strings.TrimSpace(parts[1])
+ 		if token == "" {
+ 			httpjson.Error(c, apierr.Unauthenticated("missing bearer token"))
+ 			return
+ 		}


─── internal/account/mysql_store.go:84-88 ───
[bug · high] The second UPDATE ignores RowsAffected and unconditionally overwrites the target's
device_id. If targetID is missing or no longer UserStatusActive (e.g. a concurrent status change
after the service-level check), the guest row is still archived and its device_id cleared, yet the
device binding is never created — and the transaction commits this partial merge. It also silently
drops any device_id already bound to the target. Check RowsAffected and abort (return ErrNotFound)
when it is not 1, and consider guarding the target row's existing device_id state before
overwriting.

- 	if _, err := tx.ExecContext(ctx, `
+ 	result, err := tx.ExecContext(ctx, `
  		UPDATE users SET device_id = ?, updated_at = ? WHERE id = ? AND status = ?
- 	`, deviceID, at, targetID, UserStatusActive); err != nil {
+ 	`, deviceID, at, targetID, UserStatusActive)
+ 	if err != nil {
+ 		return err
+ 	}
+ 	n, err := result.RowsAffected()
+ 	if err != nil {
  		return err
+ 	}
+ 	if n != 1 {
+ 		return ErrNotFound
  	}


─── internal/account/mysql_store.go:37-42 ───
[bug · medium] isMySQLDuplicate maps every 1062 duplicate-key error to ErrDuplicateDeviceID. The
users table also has UNIQUE keys on email and phone (uk_users_email, uk_users_phone), so once
email/phone registration is added, a duplicate email/phone will be misreported as a duplicate
device_id and hide the real conflict. Narrow the mapping to the device_id unique key, e.g. by
inspecting the key name in the MySQL error message.



─── internal/account/mysql_store.go:142-146 ───
[bug · medium] scanUser scans DATETIME(3) columns directly into time.Time. go-sql-driver/mysql only
returns time.Time when parseTime=true is set in the DSN; otherwise it returns []byte and every read
fails at Scan with an unsupported-conversion error. The DSN is passed through unchanged from
MYSQL_DSN and nothing validates parseTime, so a misconfigured DSN makes all user queries fail.
Validate/enforce parseTime=true (and a fixed loc) when opening the connection, or parse the columns
explicitly.



─── internal/account/mysql_store.go:110-112 ───
[bug · medium] ReplaceRefreshToken deletes and re-inserts without locking the user row, and
auth_tokens has no unique constraint on user_id (only on refresh_token_hash). Two concurrent token
issues for the same user can interleave DELETE/INSERT and both commit, leaving multiple valid
refresh tokens and violating the documented 'drops existing tokens and stores one' behavior.
Serialize via SELECT ... FOR UPDATE on the user row, or add a UNIQUE KEY on user_id.



─── internal/account/types.go:55-55 ───
[style · low] `MergedFromUserID` is a nullable pointer without `omitempty`, so every response will
include `"merged_from_user_id": null` when no guest was merged (e.g. the already-merged path where
`mergedFrom` returns nil). If the intent is to omit the field when absent, add `omitempty`;
otherwise clients must treat the field as always-present-and-nullable, which should be a deliberate
API contract decision.

- 	MergedFromUserID *string `json:"merged_from_user_id"`
+ 	MergedFromUserID *string `json:"merged_from_user_id,omitempty"`


─── internal/account/types.go:12-15 ───
[maintainability · low] `UserStatusActive`/`UserStatusMerged` are untyped string constants, while
`User.Status` and `TokenResponse.Status` are plain `string`. A defined `UserStatus` type would let
the compiler catch invalid status assignments and make the allowed status values part of the public
contract, rather than relying on convention.

+ type UserStatus string
+ 
  const (
- 	UserStatusActive = "active"
- 	UserStatusMerged = "merged"
+ 	UserStatusActive UserStatus = "active"
+ 	UserStatusMerged UserStatus = "merged"
  )


─── internal/account/memory_store.go:98-101 ───
[bug · high] MarkMerged binds `deviceID` to the target without first removing any previous device_id
the target already owns, and it silently overwrites `byDevice[deviceID]` if that device is currently
bound to a different user. After a second merge on the same target, `byDevice` still maps the
target's old device to the target while `target.DeviceID` points at the new device, so
`GetActiveByDeviceID` returns a stale binding. This diverges from the MySQL store, where `device_id`
is a single column with `uk_users_device_id` (the old device is simply overwritten/unbound). Remove
the target's prior mapping and reject a deviceID owned by another user before mutating state.

+ if target.DeviceID != nil {
+ 		delete(s.byDevice, *target.DeviceID)
+ 	}
+ 	if existing, ok := s.byDevice[deviceID]; ok && existing != targetID {
+ 		return ErrDuplicateDeviceID
+ 	}
  target.DeviceID = cloneStringPtr(&deviceID)
  	target.UpdatedAt = at
  	s.users[targetID] = target
  	s.byDevice[deviceID] = targetID


─── internal/account/memory_store.go:37-37 ───
[maintainability · low] A duplicate user id is reported with an ad-hoc `fmt.Errorf` rather than a
sentinel error, unlike `ErrNotFound` and `ErrDuplicateDeviceID`. Callers cannot distinguish this
failure via `errors.Is`, and it would be mapped to a generic INTERNAL error in the unified envelope.
Define a dedicated sentinel (e.g., `ErrDuplicateUserID`) for consistency.



─── internal/account/store.go:44-48 ───
[maintainability · low] Connection pool settings and the startup ping timeout are hardcoded with no
configuration hook. In orchestrations where the app and MySQL start concurrently (or during a cold
DB warm-up), the single 3-second ping with no retry causes a hard startup failure, and fixed pool
sizes (20/10/30m) limit production tuning. Consider externalizing these via config (alongside
MYSQL_DSN) or adding a bounded retry for the initial ping.



─── internal/account/service.go:127-136 ───
[bug · high] The merge sequence is not atomic: ReassignFromGuest, MarkMerged, and
DeleteRefreshTokensForUser are three independent store calls. If MarkMerged commits but
DeleteRefreshTokensForUser fails, Merge returns an error even though the merge already completed,
and the retry path (holder.ID == actor.ID) short-circuits to AlreadyMerged without ever revoking the
guest's refresh token, leaving a live refresh token for an archived account. Conversely, if
ReassignFromGuest succeeds but MarkMerged fails (e.g. a concurrent merge makes the guest no longer
active, so MarkMerged returns ErrNotFound), business data has been reassigned without the merge
being recorded and the client receives a 500 instead of an idempotent result. Consider making the
guest archive + device transfer + token revocation atomic, or treating post-MarkMerged failures as
success and also revoking tokens on the AlreadyMerged branch.



─── internal/account/service.go:96-99 ───
[bug · medium] Every GetUser error here — database outage, context cancellation, or scan failure —
is collapsed to 401 'invalid access token'. Only ErrNotFound should map to 401; other errors should
surface as 5xx (e.g. apierr.Internal/Unavailable) so infrastructure failures are not masked as auth
failures.

  	actor, err := s.store.GetUser(ctx, actorUserID)
- 	if err != nil {
+ 	if errors.Is(err, ErrNotFound) {
  		return MergeResponse{}, apierr.Unauthenticated("invalid access token")
+ 	}
+ 	if err != nil {
+ 		return MergeResponse{}, err
  	}


─── internal/account/service.go:154-157 ───
[bug · medium] Same issue as in Merge: any GetUser error (DB outage, context cancellation) is
returned as 401 'invalid access token'. Distinguish ErrNotFound from real failures so infrastructure
errors produce a 5xx instead of misleading the client and hiding operational problems.

  	user, err := s.store.GetUser(ctx, claims.Subject)
- 	if err != nil {
+ 	if errors.Is(err, ErrNotFound) {
  		return User{}, apierr.Unauthenticated("invalid access token")
+ 	}
+ 	if err != nil {
+ 		return User{}, err
  	}


─── internal/account/service.go:185-192 ───
[bug · low] mergedFrom swallows every error from FindGuestMergedInto and returns nil. A transient DB
failure in the AlreadyMerged branch then yields AlreadyMerged=true with MergedFromUserID=nil,
silently dropping the linkage. Distinguish ErrNotFound (→ nil) from real errors and at least log
them.

  func (s *Service) mergedFrom(ctx context.Context, targetID string) *string {
  	guest, err := s.store.FindGuestMergedInto(ctx, targetID)
  	if err != nil {
+ 		if !errors.Is(err, ErrNotFound) {
+ 			s.logger.Warn("find merged guest failed", "target_id", targetID, "err", err)
+ 		}
  		return nil
  	}
  	id := guest.ID
  	return &id
  }


─── internal/account/service.go:165-167 ───
[maintainability · low] IssueSession mints access+refresh tokens for any User value without
verifying existence or UserStatusActive. Unlike Authenticate and Merge, an inactive or merged
account passed here still receives valid tokens. Since this is exported for later login callers,
consider enforcing the Active-status check here (or documenting that callers must validate) to
preserve the account lifecycle invariant.

  func (s *Service) IssueSession(ctx context.Context, user User) (TokenResponse, error) {
+ 	if user.Status != UserStatusActive {
+ 		return TokenResponse{}, apierr.Unauthenticated("account is not active")
+ 	}
  	return s.issueSession(ctx, user)
  }


─── internal/apierr/apierr.go:60-60 ───
[maintainability · low] The comment claims Internal "does not leak internals", but the function does
not enforce this — it forwards the caller-supplied message directly to the client-facing Message
field. Today's callers pass a fixed "internal error" string, but nothing prevents a future call like
apierr.Internal(err.Error()) from exposing DB/internal details to API clients. Consider making
Internal() return a fixed generic client message (and logging the real error server-side) so the
contract is explicit rather than relying on caller discipline.



─── internal/apierr/apierr.go:30-30 ───
[maintainability · low] The stable machine-readable codes are part of the public API contract, but
each is repeated as a string literal across the constructors, risking typos or drift between this
package and any client-side mapping. Consider defining named constants (e.g. CodeInvalidArgument =
"INVALID_ARGUMENT") and referencing them from each constructor.



─── internal/config/config.go:56-58 ───
[security · high] The production safeguard only rejects the publicly known DevJWTSecret when AppEnv
is exactly "production". Any other deployment value (e.g. "prod", "staging", "uat") — or an unset
APP_ENV, which Load() silently defaults to "development" — runs with the hardcoded signing secret,
so anyone who reads the source can forge JWT tokens. Dockerfile/docker-compose.yml don't set APP_ENV
at all, so a container deployed as-is uses the dev secret. Prefer an allowlist of development
environment names and require a real secret for everything else.

- 	if c.IsProduction() && c.AuthJWTSecret == DevJWTSecret {
- 		return fmt.Errorf("AUTH_JWT_SECRET must be set in production")
+ 	if !isDevEnvironment(c.AppEnv) && c.AuthJWTSecret == DevJWTSecret {
+ 		return fmt.Errorf("AUTH_JWT_SECRET must not use the development default outside development")
  	}


─── internal/config/config.go:84-87 ───
[maintainability · medium] durationOr silently returns the fallback when the TTL env var is
malformed because the time.ParseDuration error is discarded. A typo such as AUTH_ACCESS_TTL=2hurs
produces no startup error or warning, so the server runs with an unintended token lifetime and the
misconfiguration stays hidden. Consider returning the parse error and propagating it through Load()
so invalid values fail fast.

  	parsed, err := time.ParseDuration(raw)
  	if err != nil {
- 		return fallback
+ 		return 0, fmt.Errorf("%s: %w", key, err)
  	}


─── internal/config/config.go:50-52 ───
[maintainability · low] Validate() only checks that HTTPAddr is non-empty, so malformed values such
as "8080" or "localhost" pass configuration validation and only fail later inside
http.Server.ListenAndServe. Validating the host:port format here makes startup errors actionable and
keeps configuration failures deterministic.

- 	if strings.TrimSpace(c.HTTPAddr) == "" {
- 		return fmt.Errorf("HTTP_ADDR is required")
+ 	if _, _, err := net.SplitHostPort(strings.TrimSpace(c.HTTPAddr)); err != nil {
+ 		return fmt.Errorf("HTTP_ADDR must be a valid host:port: %w", err)
  	}


─── scripts/dev-down.sh:7-9 ───
[maintainability · low] `command -v docker` only verifies that the Docker CLI exists. `docker
compose down` will still fail when the Compose plugin is missing or the Docker daemon is not
running, and because `set -euo pipefail` is active the script then exits non-zero before printing
the final status message. For an idempotent teardown script, consider checking `docker compose
version` and/or handling a failed `down` explicitly so the user gets the intended message instead of
an abrupt failure.



─── internal/httpserver/server.go:55-56 ───
[bug · medium] The readiness handler calls `ready(c.Request.Context())` without any deadline. When
wired to `MySQLStore.Ping` (i.e. `sql.DB.PingContext`), a check against an unreachable DB can block
until the client disconnects rather than failing fast, so orchestrator readiness probes will time
out and keep the pod unready. Apply a short timeout around the dependency check.

  		if ready != nil {
- 			if err := ready(c.Request.Context()); err != nil {
+ 			ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
+ 			defer cancel()
+ 			if err := ready(ctx); err != nil {


─── internal/httpserver/server.go:18-18 ───
[maintainability · low] A package-level `sync.Once` permanently switches Gin into ReleaseMode on the
first `New` call and can never be undone. This is an implicit, cross-cutting global side effect: any
test that constructs the server can no longer switch Gin to TestMode/DebugMode for output, and
`main` already sets ReleaseMode before calling `New`, making this redundant in production. Prefer
letting the caller control gin mode explicitly.



─── internal/httpjson/httpjson.go:38-38 ───
[bug · medium] `Error` writes `ae.HTTPStatus` directly without validation. `apierr.Error` has
exported fields and no constructor-time validation, so a zero or out-of-range status (e.g.
`HTTPStatus == 0`) would cause `AbortWithStatusJSON`/`net/http` to panic (`invalid WriteHeader
code`). Today all `apierr` constructors set a valid status, but since this is the central error
handler, normalize invalid statuses defensively to avoid a panic that the Recover middleware then
turns into a misleading 500.

- 	c.AbortWithStatusJSON(ae.HTTPStatus, apierr.Body{
+ 	status := ae.HTTPStatus
+ 	if status < http.StatusBadRequest || status > 599 {
+ 		status = http.StatusInternalServerError
+ 	}
+ 	c.AbortWithStatusJSON(status, apierr.Body{


─── scripts/dev-check.sh:21-21 ───
[maintainability · low] When `gofumpt -l .` finds unformatted files, the file list is captured and
discarded, so the script aborts with no indication of which files failed the check. Consider
printing the offending files before failing. The same applies to the `goimports -l` check below.

- test -z "$(gofumpt -l .)"
+ unformatted="$(gofumpt -l .)"
+ test -z "$unformatted" || { printf '%s\n' "$unformatted" >&2; exit 1; }


─── scripts/dev-check.sh:18-18 ───
[maintainability · low] `need` only checks that the binary exists, not its version. An
already-installed golangci-lint v1 would pass this check even though the install command pins v2,
whose CLI and configuration format differ and may then fail or behave differently in `golangci-lint
run ./...`. Consider verifying the major version for tools where compatibility matters.



─── internal/httpserver/middleware.go:19-24 ───
[security · low] The client-supplied X-Request-ID is accepted verbatim and both echoed on the
response and stored for logs/error envelopes, with no length or character validation. A malicious or
buggy client can send an extremely long or control-character-laden value, bloating logs and spoofing
trace IDs. Consider capping the length (and falling back to a generated UUID when
empty/oversized/invalid).

  		id := c.GetHeader(requestIDHeader)
- 		if id == "" {
+ 		if id == "" || len(id) > 64 {
  			id = uuid.NewString()
  		}
  		c.Set(httpjson.RequestIDContextKey, id)
  		c.Header(requestIDHeader, id)


─── internal/httpserver/middleware.go:33-38 ───
[bug · low] The Recover middleware also swallows http.ErrAbortHandler, the sentinel panic net/http
uses to abort a handler (e.g. client disconnect). The docs state such panics should not be recovered
by the handler; converting it to a logged 500 response misclassifies client aborts as server errors.
Re-panic it so the server can handle the abort.

  			if rec := recover(); rec != nil {
+ 				if rec == http.ErrAbortHandler {
+ 					panic(rec)
+ 				}
  				if logger != nil {
  					logger.Error("panic recovered", "panic", rec, "request_id", httpjson.RequestID(c))
  				}
  				httpjson.Error(c, apierr.Internal("internal error"))
  			}


─── internal/httpserver/middleware.go:54-55 ───
[maintainability · low] The field names are semantically reversed: c.FullPath() returns the
registered route pattern, while c.Request.URL.Path is the actual requested path. Logging the route
pattern under "path" and the real path under "route" will mislead log consumers and dashboards. Swap
the values (or rename the keys) so "path" reflects the requested path.

- 			"path", c.FullPath(),
- 			"route", c.Request.URL.Path,
+ 			"path", c.Request.URL.Path,
+ 			"route", c.FullPath(),


─── scripts/dev-up.sh:77-85 ───
[bug · medium] wait_for_health only checks that /healthz responds, but never verifies that the
just-launched `go run` process is still alive or that it is the process answering the request. If
another server is already listening on PORT, `go run` exits immediately with "bind: address already
in use" while curl still succeeds against the stale server, so the script falsely reports a healthy
app-server and smoke-tests the wrong process. Also note /healthz is liveness (always 200 once the
HTTP server is up); /readyz is the endpoint that reflects store readiness. Add a `kill -0
"$SERVER_PID"` check inside the loop so the wait fails when the newly started process dies.

  wait_for_health() {
    local url="http://127.0.0.1:${PORT}/healthz"
    local i
    for i in $(seq 1 60); do
+     if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
+       echo "app-server exited before becoming healthy" >&2
+       return 1
+     fi
      if curl -sf "$url" >/dev/null 2>&1; then
        return 0
      fi
      sleep 0.5
    done

