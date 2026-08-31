package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// APIError is the typed failure returned by HTTPClient for any response with
// status >= 400. It preserves the machine-readable parts of the backend error
// envelope so callers can assert on them instead of matching substrings.
//
// The Locali backend uses several envelopes, all covered here:
//
//	{"description":"NO_TOKEN_INCLUDED","error":"no_token"}   // authorization.js
//	{"description":"COURIER_ONLY","error":"forbidden"}       // courierOnly.js
//	{"code":"ORDER_NOT_ASSIGNED","message":"..."}            // CourierController
//	{"error":"bad_request","fields":[...],"message":"..."}   // RestController
//	{"message":"Код уже отправлен. Повторно можно через 42 сек."}
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string

	// Code is the machine-readable identifier: `code`, then `description`,
	// then `error` when it looks like a symbol rather than a sentence.
	Code string
	// Message is the human-readable text: `message`, then `error`.
	Message string
	// Fields lists the offending request fields when the backend reports them
	// (createRest missing-required-fields envelope).
	Fields []string
	// Details carries the per-field validation messages the newer validators
	// return: [{"field":"source","message":"source должен быть одним из: ..."}].
	// It is the difference between "Некорректные параметры заказа" and knowing
	// exactly which field the backend rejected.
	Details []FieldError
	// RetryAfterSec is the cooldown advertised by a 429: the Retry-After
	// header, the `retryAfter` body field, or the seconds embedded in the
	// Russian rate-limit message.
	RetryAfterSec int
}

// FieldError is one entry of the backend's `details` validation array.
type FieldError struct {
	Field   string
	Message string
}

func (f FieldError) String() string {
	if f.Field == "" {
		return f.Message
	}
	return f.Field + ": " + f.Message
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "api error %d", e.StatusCode)
	if e.Method != "" {
		fmt.Fprintf(&b, " [%s %s]", e.Method, e.Path)
	}
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	switch {
	case e.Message != "":
		fmt.Fprintf(&b, ": %s", e.Message)
	case e.Body != "":
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	if len(e.Details) > 0 {
		parts := make([]string, 0, len(e.Details))
		for _, d := range e.Details {
			parts = append(parts, d.String())
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, "; "))
	}
	if e.RetryAfterSec > 0 {
		fmt.Fprintf(&b, " (retry after %ds)", e.RetryAfterSec)
	}
	return b.String()
}

// AsAPIError unwraps err to the underlying *APIError, if any. Works through
// the fmt.Errorf("...: %w", err) wrapping used across the API layer.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// StatusOf returns the HTTP status carried by err, or 0 when err is not an
// API error (transport failure, marshalling, context cancellation).
func StatusOf(err error) int {
	if apiErr, ok := AsAPIError(err); ok {
		return apiErr.StatusCode
	}
	return 0
}

// IsStatus reports whether err is an API error with exactly this status.
func IsStatus(err error, status int) bool {
	return StatusOf(err) == status
}

// IsRateLimited reports whether the backend answered 429 Too Many Requests.
func IsRateLimited(err error) bool {
	return IsStatus(err, http.StatusTooManyRequests)
}

// IsUnauthorized reports a 401 (missing/invalid token).
func IsUnauthorized(err error) bool { return IsStatus(err, http.StatusUnauthorized) }

// IsForbidden reports a 403 (authenticated but wrong role).
func IsForbidden(err error) bool { return IsStatus(err, http.StatusForbidden) }

// RetryAfter returns the advertised cooldown in seconds, or 0 when unknown.
func RetryAfter(err error) int {
	if apiErr, ok := AsAPIError(err); ok {
		return apiErr.RetryAfterSec
	}
	return 0
}

// «Повторно можно через 42 сек.» — the only place some handlers expose the
// remaining cooldown (ClientController.registerClient).
var retryAfterInMessage = regexp.MustCompile(`через\s+(\d+)\s*сек`)

// looksLikeCode reports whether s reads as a machine symbol (NO_TOKEN_INCLUDED,
// bad_request) rather than a human sentence.
func looksLikeCode(s string) bool {
	if s == "" || strings.ContainsAny(s, " .,!?") {
		return false
	}
	return s == strings.ToUpper(s) || !strings.ContainsAny(s, "абвгдеёжзийклмнопрстуфхцчшщъыьэюя")
}

// newAPIError parses the backend error envelope into a typed error. Unparsable
// bodies (HTML error pages, proxy responses) still yield a usable error with
// the raw body attached.
func newAPIError(method, path string, resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}

	if h := resp.Header.Get("Retry-After"); h != "" {
		if sec, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && sec > 0 {
			e.RetryAfterSec = sec
		}
	}

	var envelope map[string]interface{}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return e
	}

	str := func(key string) string {
		if v, ok := envelope[key].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}

	code, description, errField := str("code"), str("description"), str("error")
	message := str("message")

	switch {
	case code != "":
		e.Code = code
	case description != "":
		e.Code = description
	case looksLikeCode(errField):
		e.Code = errField
	}

	switch {
	case message != "":
		e.Message = message
	case errField != "" && !looksLikeCode(errField):
		// `error` doubles as both slots: a symbol (no_token, forbidden) adds
		// nothing beyond Code, a sentence is the only human text there is.
		e.Message = errField
	}

	if raw, ok := envelope["details"].([]interface{}); ok {
		for _, item := range raw {
			entry, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			field, _ := entry["field"].(string)
			msg, _ := entry["message"].(string)
			if field == "" && msg == "" {
				continue
			}
			e.Details = append(e.Details, FieldError{Field: field, Message: msg})
		}
	}

	if raw, ok := envelope["fields"].([]interface{}); ok {
		for _, f := range raw {
			if s, ok := f.(string); ok {
				e.Fields = append(e.Fields, s)
			}
		}
	}

	if e.RetryAfterSec == 0 {
		if v, ok := envelope["retryAfter"].(float64); ok && v > 0 {
			e.RetryAfterSec = int(v)
		}
	}
	if e.RetryAfterSec == 0 {
		for _, candidate := range []string{message, errField} {
			if m := retryAfterInMessage.FindStringSubmatch(candidate); m != nil {
				if sec, err := strconv.Atoi(m[1]); err == nil {
					e.RetryAfterSec = sec
				}
				break
			}
		}
	}

	return e
}
