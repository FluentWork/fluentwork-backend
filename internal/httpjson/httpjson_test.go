package httpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

const probePath = "/probe"

func newProbeEngine(t *testing.T, handlers ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET(probePath, handlers...)
	return engine
}

func serveProbe(t *testing.T, engine *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, probePath, nil))
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) apierr.Body {
	t.Helper()
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

func withLogger(buf *bytes.Buffer) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(LoggerContextKey, slog.New(slog.NewJSONHandler(buf, nil)))
	}
}

func TestError_HidesAPlainErrorFromTheClientAndShowsItInTheLog(t *testing.T) {
	const leak = `pq: duplicate key value violates unique constraint "users_email_key"`

	var buf bytes.Buffer
	engine := newProbeEngine(t, withLogger(&buf), func(c *gin.Context) {
		Error(c, errors.New(leak))
	})

	rec := serveProbe(t, engine)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := decodeEnvelope(t, rec)
	if body.Code != "INTERNAL" || body.Message != "internal error" {
		t.Fatalf("envelope = %+v, want the generic INTERNAL one", body)
	}
	if strings.Contains(rec.Body.String(), "users_email_key") {
		t.Fatalf("the response leaked the underlying error: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "users_email_key") {
		t.Fatalf("the log did not carry the underlying error: %s", buf.String())
	}
}

func TestError_KeepsAClientErrorAndDoesNotLogIt(t *testing.T) {
	var buf bytes.Buffer
	engine := newProbeEngine(t, withLogger(&buf), func(c *gin.Context) {
		Error(c, apierr.NotFound("no such material"))
	})

	rec := serveProbe(t, engine)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := decodeEnvelope(t, rec)
	if body.Code != "NOT_FOUND" || body.Message != "no such material" {
		t.Fatalf("envelope = %+v, want the caller's NOT_FOUND kept verbatim", body)
	}
	if buf.Len() != 0 {
		t.Fatalf("a 4xx was logged as a server failure: %s", buf.String())
	}
}

func TestError_LogsAServerErrorWithItsCode(t *testing.T) {
	var buf bytes.Buffer
	engine := newProbeEngine(t, withLogger(&buf), func(c *gin.Context) {
		Error(c, apierr.Unavailable("provider down"))
	})

	rec := serveProbe(t, engine)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := decodeEnvelope(t, rec); body.Code != "UNAVAILABLE" {
		t.Fatalf("code = %q, want UNAVAILABLE", body.Code)
	}
	if !strings.Contains(buf.String(), "request failed") {
		t.Fatalf("a 5xx was not logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "UNAVAILABLE") {
		t.Fatalf("the log did not carry the code: %s", buf.String())
	}
}

func TestError_ClampsAStatusThatIsNotAnHTTPError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"zero", 0},
		{"a success status", http.StatusOK},
		{"above the range", 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newProbeEngine(t, func(c *gin.Context) {
				Error(c, &apierr.Error{Code: "WEIRD", Message: "weird", HTTPStatus: tc.status})
			})

			rec := serveProbe(t, engine)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			body := decodeEnvelope(t, rec)
			if body.Code != "WEIRD" || body.Message != "weird" {
				t.Fatalf("envelope = %+v, want the caller's code and message kept", body)
			}
		})
	}
}

func TestError_SurvivesATypedNilApierr(t *testing.T) {
	var typedNil *apierr.Error

	engine := newProbeEngine(t, func(c *gin.Context) { Error(c, typedNil) })

	rec := serveProbe(t, engine)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := decodeEnvelope(t, rec); body.Code != "INTERNAL" {
		t.Fatalf("code = %q, want INTERNAL", body.Code)
	}
}

func TestError_WritesTheEnvelopeWhenNoLoggerWasWired(t *testing.T) {
	engine := newProbeEngine(t, func(c *gin.Context) { Error(c, apierr.Internal("boom")) })

	rec := serveProbe(t, engine)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestError_IgnoresAContextValueThatIsNotALogger(t *testing.T) {
	engine := newProbeEngine(t,
		func(c *gin.Context) { c.Set(LoggerContextKey, "not a logger") },
		func(c *gin.Context) { Error(c, apierr.Internal("boom")) },
	)

	rec := serveProbe(t, engine)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestError_AbortsSoNoLaterHandlerRuns(t *testing.T) {
	reached := false
	engine := newProbeEngine(t,
		func(c *gin.Context) { Error(c, apierr.InvalidArgument("bad")) },
		func(*gin.Context) { reached = true },
	)

	serveProbe(t, engine)

	if reached {
		t.Fatal("a handler registered after Error ran; the request was not aborted")
	}
}

func TestRequestID_ReadsOnlyAStringTheMiddlewareStored(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store bool
		value any
		want  string
	}{
		{"absent", false, nil, ""},
		{"a string", true, "req-7", "req-7"},
		{"not a string", true, 42, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			handlers := []gin.HandlerFunc{}
			if tc.store {
				value := tc.value
				handlers = append(handlers, func(c *gin.Context) { c.Set(RequestIDContextKey, value) })
			}
			handlers = append(handlers, func(c *gin.Context) { got = RequestID(c) })

			serveProbe(t, newProbeEngine(t, handlers...))

			if got != tc.want {
				t.Fatalf("RequestID = %q, want %q", got, tc.want)
			}
		})
	}
}
