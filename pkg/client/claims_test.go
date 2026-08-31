package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// token builds an unsigned JWT carrying the given claims — the same shape the
// backend issues, minus a valid signature the engine never checks.
func token(claims map[string]interface{}) string {
	body, _ := json.Marshal(claims)
	return fmt.Sprintf("%s.%s.%s",
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`)),
		base64.RawURLEncoding.EncodeToString(body),
		"sig")
}

func TestDecodeClaims_PerRoleShapes(t *testing.T) {
	cases := []struct {
		name     string
		claims   map[string]interface{}
		wantRole string
		wantUser string
	}{
		{
			name:     "client carries no role flag",
			claims:   map[string]interface{}{"user_id": "c-1"},
			wantRole: "client",
			wantUser: "c-1",
		},
		{
			name:     "courier",
			claims:   map[string]interface{}{"user_id": "k-1", "isCourier": true, "phoneNumber": "+79221234567"},
			wantRole: "courier",
			wantUser: "k-1",
		},
		{
			name:     "restaurant",
			claims:   map[string]interface{}{"user_id": "r-1", "isRestaurant": true},
			wantRole: "rest",
			wantUser: "r-1",
		},
		{
			name:     "admin",
			claims:   map[string]interface{}{"user_id": "a-1", "admin_id": "a-1", "isAdmin": true, "rights": []interface{}{"order1"}},
			wantRole: "admin",
			wantUser: "a-1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := DecodeClaims(token(tc.claims))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if claims.Role() != tc.wantRole {
				t.Errorf("role: want %q, got %q", tc.wantRole, claims.Role())
			}
			if claims.UserID != tc.wantUser {
				t.Errorf("user_id: want %q, got %q", tc.wantUser, claims.UserID)
			}
		})
	}
}

func TestDecodeClaims_NumericUserID(t *testing.T) {
	// Sequelize integer PKs arrive as JSON numbers, not strings.
	claims, err := DecodeClaims(token(map[string]interface{}{"user_id": 42}))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if claims.UserID != "42" {
		t.Fatalf("numeric user_id: want \"42\", got %q", claims.UserID)
	}
}

func TestDecodeClaims_Expiry(t *testing.T) {
	noExp, _ := DecodeClaims(token(map[string]interface{}{"user_id": "c-1"}))
	if noExp.HasExpiry() {
		t.Error("a token without exp must report no expiry")
	}

	exp := time.Now().Add(time.Hour).Unix()
	withExp, _ := DecodeClaims(token(map[string]interface{}{"user_id": "c-1", "exp": exp}))
	if !withExp.HasExpiry() || withExp.ExpiresAt.Unix() != exp {
		t.Errorf("exp not decoded: %+v", withExp.ExpiresAt)
	}
}

func TestDecodeClaims_NotAJWT(t *testing.T) {
	if _, err := DecodeClaims("jwt_mock_client_+79991234567"); err == nil {
		t.Fatal("an opaque string must not decode as a JWT")
	}
}

func TestVerifyRole(t *testing.T) {
	clientToken := token(map[string]interface{}{"user_id": "c-1"})
	courierToken := token(map[string]interface{}{"user_id": "k-1", "isCourier": true})
	rootToken := token(map[string]interface{}{"isAdmin": true, "user_id": nil, "admin_id": nil})

	if err := VerifyRole(clientToken, "client"); err != nil {
		t.Errorf("client token must verify as client: %v", err)
	}
	if err := VerifyRole(courierToken, "courier"); err != nil {
		t.Errorf("courier token must verify as courier: %v", err)
	}
	// The alias used by the session pools must resolve to the same role.
	if err := VerifyRole(token(map[string]interface{}{"user_id": "r-1", "isRestaurant": true}), "restaurant"); err != nil {
		t.Errorf("restaurant alias must verify: %v", err)
	}

	// This is the regression the check exists for: a login handler answering
	// 200 with the wrong claim set.
	if err := VerifyRole(courierToken, "client"); err == nil {
		t.Error("a courier token must not pass as a client token")
	}
	if err := VerifyRole(clientToken, "admin"); err == nil {
		t.Error("a client token must not pass as an admin token")
	}

	// The hardcoded super-root admin legitimately has no identity.
	if err := VerifyRole(rootToken, "admin"); err != nil {
		t.Errorf("super-root admin token must verify without user_id: %v", err)
	}
	// Every other role must carry one.
	if err := VerifyRole(token(map[string]interface{}{"isCourier": true}), "courier"); err == nil {
		t.Error("a courier token without user_id must be rejected")
	}
}
