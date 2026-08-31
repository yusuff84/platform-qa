package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func responseWith(status int, headers map[string]string) *http.Response {
	rec := httptest.NewRecorder()
	for k, v := range headers {
		rec.Header().Set(k, v)
	}
	rec.WriteHeader(status)
	return rec.Result()
}

func TestNewAPIError_BackendEnvelopes(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantCode    string
		wantMessage string
		wantRetry   int
		wantFields  []string
	}{
		{
			name:     "authorization middleware: missing token",
			status:   http.StatusUnauthorized,
			body:     `{"description":"NO_TOKEN_INCLUDED","error":"no_token"}`,
			wantCode: "NO_TOKEN_INCLUDED",
		},
		{
			name:     "courierOnly gate",
			status:   http.StatusForbidden,
			body:     `{"description":"COURIER_ONLY","error":"forbidden"}`,
			wantCode: "COURIER_ONLY",
		},
		{
			name:        "controller code + message",
			status:      http.StatusConflict,
			body:        `{"code":"ORDER_NOT_ASSIGNED","message":"Заказ ещё не назначен курьеру"}`,
			wantCode:    "ORDER_NOT_ASSIGNED",
			wantMessage: "Заказ ещё не назначен курьеру",
		},
		{
			name:        "createRest missing required fields",
			status:      http.StatusBadRequest,
			body:        `{"error":"bad_request","fields":["city","login"],"message":"Отсутствуют обязательные поля: city, login"}`,
			wantCode:    "bad_request",
			wantMessage: "Отсутствуют обязательные поля: city, login",
			wantFields:  []string{"city", "login"},
		},
		{
			name:        "rate limit with seconds embedded in the message",
			status:      http.StatusTooManyRequests,
			body:        `{"message":"Код уже отправлен. Повторно можно через 42 сек."}`,
			wantMessage: "Код уже отправлен. Повторно можно через 42 сек.",
			wantRetry:   42,
		},
		{
			name:        "rate limit with explicit retryAfter field",
			status:      http.StatusTooManyRequests,
			body:        `{"error":"Код уже отправлен.","retryAfter":17}`,
			wantMessage: "Код уже отправлен.",
			wantRetry:   17,
		},
		{
			name:        "error field carries the only human text",
			status:      http.StatusUnauthorized,
			body:        `{"error":"Invalid credentials"}`,
			wantMessage: "Invalid credentials",
		},
		{
			name:        "plain message envelope",
			status:      http.StatusBadRequest,
			body:        `{"message":"Неверный код верификации."}`,
			wantMessage: "Неверный код верификации.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := newAPIError("POST", "/api/test", responseWith(tc.status, nil), []byte(tc.body))

			if err.StatusCode != tc.status {
				t.Fatalf("status: want %d, got %d", tc.status, err.StatusCode)
			}
			if err.Code != tc.wantCode {
				t.Errorf("code: want %q, got %q", tc.wantCode, err.Code)
			}
			if err.Message != tc.wantMessage {
				t.Errorf("message: want %q, got %q", tc.wantMessage, err.Message)
			}
			if err.RetryAfterSec != tc.wantRetry {
				t.Errorf("retryAfter: want %d, got %d", tc.wantRetry, err.RetryAfterSec)
			}
			if len(err.Fields) != len(tc.wantFields) {
				t.Fatalf("fields: want %v, got %v", tc.wantFields, err.Fields)
			}
			for i, f := range tc.wantFields {
				if err.Fields[i] != f {
					t.Errorf("fields[%d]: want %q, got %q", i, f, err.Fields[i])
				}
			}
		})
	}
}

func TestNewAPIError_RetryAfterHeaderWins(t *testing.T) {
	resp := responseWith(http.StatusTooManyRequests, map[string]string{"Retry-After": "5"})
	err := newAPIError("POST", "/api/clients/register", resp, []byte(`{"retryAfter":99}`))
	if err.RetryAfterSec != 5 {
		t.Fatalf("header must win: want 5, got %d", err.RetryAfterSec)
	}
}

func TestNewAPIError_NonJSONBodyStaysReadable(t *testing.T) {
	err := newAPIError("GET", "/api/health", responseWith(502, nil), []byte("<html>Bad Gateway</html>"))
	if err.Body != "<html>Bad Gateway</html>" {
		t.Fatalf("raw body lost: %q", err.Body)
	}
	if err.Code != "" || err.Message != "" {
		t.Fatalf("unparsable body must not invent code/message: %+v", err)
	}
}

func TestHelpersUnwrapThroughFmtErrorf(t *testing.T) {
	base := newAPIError("POST", "/api/clients/login", responseWith(429, nil), []byte(`{"retryAfter":30}`))
	wrapped := fmt.Errorf("client login failed: %w", fmt.Errorf("outer: %w", base))

	if !IsRateLimited(wrapped) {
		t.Error("IsRateLimited must see through wrapping")
	}
	if got := StatusOf(wrapped); got != 429 {
		t.Errorf("StatusOf: want 429, got %d", got)
	}
	if got := RetryAfter(wrapped); got != 30 {
		t.Errorf("RetryAfter: want 30, got %d", got)
	}
	if IsRateLimited(fmt.Errorf("connection refused")) {
		t.Error("a transport error is not a rate limit")
	}
	if got := StatusOf(fmt.Errorf("connection refused")); got != 0 {
		t.Errorf("StatusOf of a non-API error: want 0, got %d", got)
	}
}

func TestStatusPredicates(t *testing.T) {
	unauthorized := newAPIError("GET", "/x", responseWith(401, nil), []byte(`{}`))
	forbidden := newAPIError("GET", "/x", responseWith(403, nil), []byte(`{}`))

	if !IsUnauthorized(unauthorized) || IsForbidden(unauthorized) {
		t.Error("401 must classify as unauthorized only")
	}
	if !IsForbidden(forbidden) || IsUnauthorized(forbidden) {
		t.Error("403 must classify as forbidden only")
	}
}

func TestNewAPIError_LegacyThreeOhOneErrorConvention(t *testing.T) {
	// loginAdmin / loginRest / regCourier answer 301 with an error body
	// instead of a 4xx. Treating that as success handed the caller an error
	// message in place of a token.
	err := newAPIError("POST", "/api/admin/login", responseWith(301, nil),
		[]byte(`{"message":"Неправильный пароль."}`))

	if err.StatusCode != 301 {
		t.Fatalf("status: want 301, got %d", err.StatusCode)
	}
	if err.Message != "Неправильный пароль." {
		t.Errorf("message: %q", err.Message)
	}
	if IsUnauthorized(err) || IsForbidden(err) {
		t.Error("301 must not classify as 401/403")
	}
}
