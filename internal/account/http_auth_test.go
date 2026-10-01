package account_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// postAuthJSON 自成一体地起一台服务器：这几条判据关心的是「一次调用拿到什么」，
// 名字避开 `refresh_http_test.go` 里那个带 server 参数的 `postJSON`。
func postAuthJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	server, _, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeToken(t *testing.T, rec *httptest.ResponseRecorder) account.TokenResponse {
	t.Helper()
	var out account.TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode token: %v body=%s", err, rec.Body.String())
	}
	return out
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) apierr.Body {
	t.Helper()
	var out apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error: %v body=%s", err, rec.Body.String())
	}
	return out
}

func TestRegisterHTTPReturnsRegisteredTokens(t *testing.T) {
	rec := postAuthJSON(t, "/api/v1/auth/register", `{"email":"tango@example.com","password":"a good password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	body := decodeToken(t, rec)
	if body.IsGuest {
		t.Fatalf("注册应当给出注册身份：%+v", body)
	}
	if body.UserID == "" || body.AccessToken == "" || body.RefreshToken == "" || body.TokenType != "Bearer" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestRegisterHTTPRejectsDuplicateEmail(t *testing.T) {
	server, _, _ := setupServer(t)
	handler := server.Handler()

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		bytes.NewReader([]byte(`{"email":"tango@example.com","password":"a good password"}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first register: %d %s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		bytes.NewReader([]byte(`{"email":"Tango@Example.com","password":"another password"}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(second, req)

	if second.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d body = %s", second.Code, second.Body.String())
	}
	if got := decodeError(t, second).Code; got != "ALREADY_EXISTS" && got != "CONFLICT" {
		t.Fatalf("意外的错误码 %q（契约里 409 的码要固定）", got)
	}
}

func TestLoginHTTPReturnsTokens(t *testing.T) {
	server, _, _ := setupServer(t)
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		bytes.NewReader([]byte(`{"email":"tango@example.com","password":"a good password"}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		bytes.NewReader([]byte(`{"email":"  TANGO@example.com ","password":"a good password"}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if body := decodeToken(t, rec); body.IsGuest || body.AccessToken == "" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

// **这一组里最要紧的一条**：HTTP 层的响应体也必须分不出「邮箱不存在」与「口令不对」。
// 服务层那条判据守的是错误值；这一条守的是**真的发出去的那几行 JSON**——
// 只要 code / message 有一个不同，登录接口就是一个免费的账号枚举器。
func TestLoginHTTPDoesNotRevealWhetherTheAccountExists(t *testing.T) {
	server, _, _ := setupServer(t)
	handler := server.Handler()

	register := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		bytes.NewReader([]byte(`{"email":"tango@example.com","password":"a good password"}`)))
	register.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), register)

	call := func(body string) (int, apierr.Body) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		var out apierr.Body
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode error body: %v body=%s", err, rec.Body.String())
		}
		return rec.Code, out
	}

	wrongStatus, wrong := call(`{"email":"tango@example.com","password":"not the password"}`)
	missingStatus, missing := call(`{"email":"nobody@example.com","password":"not the password"}`)

	if wrongStatus != http.StatusUnauthorized || missingStatus != http.StatusUnauthorized {
		t.Fatalf("两种失败都该是 401：%d / %d", wrongStatus, missingStatus)
	}
	if wrong.Code != missing.Code || wrong.Message != missing.Message {
		t.Fatalf("两种失败暴露了账号是否存在：%+v vs %+v", wrong, missing)
	}
}

// 报文坏了不是凭据错误：混成一句会让排查的人从错的方向找。
func TestLoginHTTPMalformedBodyIsNotACredentialError(t *testing.T) {
	rec := postAuthJSON(t, "/api/v1/auth/login", `{"email":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body = %s", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec).Code; got != "INVALID_ARGUMENT" {
		t.Fatalf("want INVALID_ARGUMENT, got %q", got)
	}
}

func TestRegisterHTTPRejectsWeakPassword(t *testing.T) {
	rec := postAuthJSON(t, "/api/v1/auth/register", `{"email":"tango@example.com","password":"short"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body = %s", rec.Code, rec.Body.String())
	}
}

// 注册拿到的令牌**本身就是注册身份**：它要能过 `RequireRegistered`。
//
// 这条挡的是「注册返回了一个游客身份」那种最隐蔽的错 —— 服务层的判据看的是
// `IsGuest` 字段，而这里看的是**这个令牌在别处到底算不算注册用户**。
func TestRegisteredTokenPassesRequireRegistered(t *testing.T) {
	server, _, _ := setupServer(t)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		bytes.NewReader([]byte(`{"email":"tango@example.com","password":"a good password"}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	tokens := decodeToken(t, rec)

	// /account/merge 由 RequireRegistered 把守。缺 device_id 会得到 400，
	// 但**绝不能**得到 403（那是「你不是注册用户」）。
	merge := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	handler.ServeHTTP(merge, req)

	if merge.Code == http.StatusForbidden {
		t.Fatalf("注册出来的令牌被 RequireRegistered 拒了：%s", merge.Body.String())
	}
}

// 对照组：游客令牌走同一条路**必须**被拒（403）。没有这一条，
// 上面那条判据在「RequireRegistered 整个失效」时也会通过。
func TestGuestTokenIsRejectedByRequireRegistered(t *testing.T) {
	server, _, _ := setupServer(t)
	handler := server.Handler()

	guest := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(guest, req)
	tokens := decodeToken(t, guest)

	merge := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	handler.ServeHTTP(merge, req)

	if merge.Code != http.StatusForbidden {
		t.Fatalf("游客令牌应当被 RequireRegistered 拒掉，得到 %d %s", merge.Code, merge.Body.String())
	}
}
