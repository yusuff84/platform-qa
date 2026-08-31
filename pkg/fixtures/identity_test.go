package fixtures

import (
	"regexp"
	"testing"
)

// rfMobile is the exact shape utilities/auth/authHelpers.js normalizePhoneNumber
// accepts without rewriting: a plus, then eleven digits starting with 7.
var rfMobile = regexp.MustCompile(`^\+7\d{10}$`)

func TestNewPhone_IsAcceptedByBackendNormalization(t *testing.T) {
	// Regression: fixtures used to build "+7922" + six digits, which is ten
	// digits in total. normalizePhoneNumber returns null for those, so courier
	// and restaurant registration was rejected before reaching the handler.
	for _, operator := range []int{operatorClient, operatorRest, operatorCourier} {
		for i := 0; i < 200; i++ {
			phone := newPhone(operator)
			if !rfMobile.MatchString(phone) {
				t.Fatalf("operator %d produced an unusable number: %q", operator, phone)
			}
			if phone == ReservedTestPhone {
				t.Fatalf("operator %d produced the reserved test number", operator)
			}
		}
	}
}

func TestNewPhone_KeepsOperatorPrefix(t *testing.T) {
	cases := map[int]string{
		operatorClient:  "+7999",
		operatorRest:    "+7911",
		operatorCourier: "+7922",
	}
	for operator, prefix := range cases {
		phone := newPhone(operator)
		if phone[:len(prefix)] != prefix {
			t.Errorf("operator %d: want prefix %s, got %s", operator, prefix, phone)
		}
	}
}

func TestNewPhone_IsUnique(t *testing.T) {
	// Parallel runs against one stand must not collide on an identity.
	seen := make(map[string]bool, 500)
	for i := 0; i < 500; i++ {
		phone := newPhone(operatorClient)
		if seen[phone] {
			t.Fatalf("duplicate phone after %d draws: %s", i, phone)
		}
		seen[phone] = true
	}
}

func TestNewSuffix_InRange(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := newSuffix()
		if s < 100_000 || s > 999_999 {
			t.Fatalf("suffix out of range: %d", s)
		}
	}
}
