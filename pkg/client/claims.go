package client

import (
	"fmt"
	"strings"
	"time"
)

// TokenClaims is the decoded, role-relevant part of a backend JWT.
//
// The Locali backend issues one flat claim set per role (see
// ClientController.loginClient, CourierController.loginCourier,
// RestController.loginRest, AdminController.loginAdmin):
//
//	client  -> {user_id}
//	courier -> {user_id, phoneNumber, isCourier:true}
//	rest    -> {user_id, isRestaurant:true}
//	admin   -> {user_id, admin_id, rights[], isAdmin:true}
type TokenClaims struct {
	UserID       string
	AdminID      string
	PhoneNumber  string
	IsAdmin      bool
	IsCourier    bool
	IsRestaurant bool
	Rights       []string
	ExpiresAt    time.Time // zero when the token carries no exp claim
	Raw          map[string]interface{}
}

// HasExpiry reports whether the token limits its own lifetime.
func (c TokenClaims) HasExpiry() bool { return !c.ExpiresAt.IsZero() }

// Role derives the canonical role name from the claim flags. A token with no
// role flag is a client token — that is how the backend encodes clients.
func (c TokenClaims) Role() string {
	switch {
	case c.IsAdmin:
		return "admin"
	case c.IsCourier:
		return "courier"
	case c.IsRestaurant:
		return "rest"
	default:
		return "client"
	}
}

// DecodeClaims parses the JWT payload without verifying the signature — the
// engine only asserts on what the backend put there, it never trusts it.
func DecodeClaims(token string) (TokenClaims, error) {
	payload := parseJWTPayload(token)
	if payload == nil {
		return TokenClaims{}, fmt.Errorf("token is not a decodable JWT")
	}

	claims := TokenClaims{Raw: payload}

	boolClaim := func(key string) bool {
		v, _ := payload[key].(bool)
		return v
	}
	strClaim := func(key string) string {
		switch v := payload[key].(type) {
		case string:
			return v
		case float64:
			return fmt.Sprintf("%.0f", v)
		default:
			return ""
		}
	}

	claims.IsAdmin = boolClaim("isAdmin")
	claims.IsCourier = boolClaim("isCourier")
	claims.IsRestaurant = boolClaim("isRestaurant")
	claims.UserID = strClaim("user_id")
	claims.AdminID = strClaim("admin_id")
	claims.PhoneNumber = strClaim("phoneNumber")

	if raw, ok := payload["rights"].([]interface{}); ok {
		for _, r := range raw {
			if s, ok := r.(string); ok {
				claims.Rights = append(claims.Rights, s)
			}
		}
	}
	if exp, ok := payload["exp"].(float64); ok && exp > 0 {
		claims.ExpiresAt = time.Unix(int64(exp), 0)
	}

	return claims, nil
}

// VerifyRole checks that a freshly issued token actually carries the role it
// was requested for. It catches the class of regression where a login handler
// keeps returning 200 but signs the wrong claim set — every downstream RBAC
// assertion would silently pass against such a token.
//
// The admin super-root token is the one legitimate case of an empty user_id
// (AdminController signs user_id:null for the hardcoded root login), so an
// identity is only required for the other roles.
func VerifyRole(token, expectedRole string) error {
	claims, err := DecodeClaims(token)
	if err != nil {
		return fmt.Errorf("token for role %q: %w", expectedRole, err)
	}

	want := canonicalRole(expectedRole)
	if got := claims.Role(); got != want {
		return fmt.Errorf("token role mismatch: expected %q, token carries %q (isAdmin=%t isCourier=%t isRestaurant=%t)",
			want, got, claims.IsAdmin, claims.IsCourier, claims.IsRestaurant)
	}
	if want != "admin" && claims.UserID == "" {
		return fmt.Errorf("token for role %q carries no user_id claim", want)
	}
	return nil
}

// canonicalRole normalizes the role aliases used across the engine.
func canonicalRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "restaurant", "rest":
		return "rest"
	case "director", "admin":
		return "admin"
	case "courier":
		return "courier"
	default:
		return "client"
	}
}
