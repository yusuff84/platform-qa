package testserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The mock mirrors the authentication contract of the real backend closely
// enough that a suite passing here is meaningful: same token shape, same OTP
// policy, same error envelopes. Anything looser turns the mock into a source
// of false confidence.

// OTP policy, mirroring ClientController.
const (
	otpTTL          = 5 * time.Minute
	otpResendWindow = 60 * time.Second
	otpMaxAttempts  = 5
)

// The DEBUG-only backdoor identity of the real backend. The mock always
// behaves as a DEBUG stand, so this number skips the resend cooldown and keeps
// its fixed code.
const (
	testPhone = "+79999999999"
	testCode  = "9999"
)

// otpRecord is the server-side state of one phone's pending verification.
type otpRecord struct {
	Code      string
	ExpiresAt time.Time
	SentAt    time.Time
	Attempts  int
}

// authStore holds identities and their credentials across the mock's lifetime.
type authStore struct {
	mu       sync.Mutex
	otps     map[string]*otpRecord // phone -> pending OTP
	clientID map[string]string     // phone -> stable client id
	restPwd  map[string]string     // login -> password
	restID   map[string]string     // login -> stable rest id
	restByPh map[string]string     // phone -> login
	courPwd  map[string]string     // login -> password
	courID   map[string]string     // login -> stable courier id
	seq      int
}

func newAuthStore() *authStore {
	return &authStore{
		otps:     make(map[string]*otpRecord),
		clientID: make(map[string]string),
		restPwd:  make(map[string]string),
		restID:   make(map[string]string),
		restByPh: make(map[string]string),
		courPwd:  make(map[string]string),
		courID:   make(map[string]string),
	}
}

func (s *authStore) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s_%d", prefix, s.seq)
}

// mockJWT builds a structurally valid, unsigned JWT. The engine never verifies
// the signature — it only reads the claims — so this exercises exactly the code
// path a real token takes.
func mockJWT(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(body)
	return fmt.Sprintf("%s.%s.%s", header, payload, "mock-signature")
}

// mockClaims decodes an unsigned mock token back into its claim set.
func mockClaims(token string) (map[string]interface{}, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func claimBool(claims map[string]interface{}, key string) bool {
	v, _ := claims[key].(bool)
	return v
}

// digitsOnly and normalizePhone mirror utilities/auth/authHelpers.js so the
// mock rejects exactly the numbers the backend rejects.
var digitsOnly = regexp.MustCompile(`\D`)

func normalizePhone(raw string) string {
	digits := digitsOnly.ReplaceAllString(raw, "")
	switch {
	case len(digits) == 11 && strings.HasPrefix(digits, "8"):
		return "+7" + digits[1:]
	case len(digits) == 11 && strings.HasPrefix(digits, "7"):
		return "+" + digits
	case len(digits) == 10 && strings.HasPrefix(digits, "9"):
		return "+7" + digits
	default:
		return ""
	}
}

// writeJSON emits a JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError emits the backend's error envelope: `description` carries the
// machine code, `message` the human text.
func writeError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]interface{}{}
	if code != "" {
		body["description"] = code
		body["error"] = strings.ToLower(code)
	}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}

// Value sets mirrored from the backend validators, so the mock rejects exactly
// what a stand rejects.
var (
	validReceiveMethods = map[string]bool{
		"delivery": true, "pickup": true, "takeout": true,
		"dinein": true, "table": true, "terminal": true,
	}
	validPaymentTypes = map[string]bool{"cash": true, "online": true}
	validSources      = map[string]bool{"lokali_eda": true, "crispy_app": true}
	validCargoTypes   = map[string]bool{
		"documents": true, "food": true, "clothes": true, "fragile": true,
		"goods": true, "personal_items": true, "other": true,
	}
)

// clientIDFromRequest resolves the caller's identity from the bearer token,
// the way the backend resolves req.user_id.
func (mb *MockBackend) clientIDFromRequest(r *http.Request) string {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, ok := mockClaims(token)
	if !ok {
		return ""
	}
	id, _ := claims["user_id"].(string)
	return id
}

// restIDFromRequest is clientIDFromRequest for restaurant tokens: the owning
// restaurant of a dish comes from the token, never from the request body.
func (mb *MockBackend) restIDFromRequest(r *http.Request) string {
	return mb.clientIDFromRequest(r)
}
