package client

import (
	"encoding/json"
	"strings"
)

// redactedMark replaces a secret value in logs and persisted run history.
const redactedMark = "***"

// secretKeys are JSON fields whose values must never reach logs, run history
// or Telegram digests. One-time verification codes are deliberately NOT here:
// on a DEBUG stand they are throwaway values, and seeing them is what makes a
// failed OTP step diagnosable.
var secretKeys = map[string]bool{
	"password":      true,
	"newpassword":   true,
	"oldpassword":   true,
	"token":         true,
	"accesstoken":   true,
	"refreshtoken":  true,
	"resettoken":    true,
	"clienttoken":   true,
	"resttoken":     true,
	"couriertoken":  true,
	"admintoken":    true,
	"authorization": true,
	"apilogin":      true,
	"apikey":        true,
	"secret":        true,
	"jwt_secret":    true,
}

// Redact returns a log-safe rendering of a JSON payload with every secret
// field masked. Non-JSON input is returned unchanged (nothing to walk), which
// keeps HTML error pages and plain-text proxy replies readable.
func Redact(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var decoded interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return string(payload)
	}
	cleaned, err := json.Marshal(redactValue(decoded))
	if err != nil {
		return string(payload)
	}
	return string(cleaned)
}

// RedactValue masks secrets in an already-decoded structure (map, slice or
// scalar) and returns a copy. The input is never mutated, so callers can pass
// values they still intend to use.
func RedactValue(v interface{}) interface{} {
	return redactValue(v)
}

func redactValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if secretKeys[strings.ToLower(k)] {
				out[k] = redactedMark
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, item := range t {
			out[i] = redactValue(item)
		}
		return out
	default:
		return v
	}
}

// MaskToken shortens a bearer token to a non-reversible fingerprint suitable
// for correlating log lines without exposing the credential.
func MaskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 12 {
		return redactedMark
	}
	return token[:6] + "…" + token[len(token)-4:]
}
