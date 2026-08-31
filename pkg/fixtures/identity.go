package fixtures

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	mathrand "math/rand"
)

// ReservedTestPhone / ReservedTestCode mirror the DEBUG-only backdoor identity
// in ClientController: while the backend runs with DEBUG=true this number is
// exempt from the resend cooldown and always receives the fixed code. It is a
// shared identity — using it sacrifices isolation between runs.
const (
	ReservedTestPhone = "+79999999999"
	ReservedTestCode  = "9999"
)

// Operator prefixes ("9NN" block) used to keep fixture roles distinguishable
// in the backend's data when reading rows by hand.
const (
	operatorClient  = 99
	operatorRest    = 11
	operatorCourier = 22
)

// newPhone returns a syntactically valid RF mobile number in the exact shape
// normalizePhoneNumber accepts: +7 followed by ten digits.
//
// The previous fixtures built numbers like "+7922" + six digits, which is ten
// digits in total — normalizePhoneNumber returns null for those and courier /
// restaurant registration was rejected before it ever reached the handler.
//
// The reserved test number is never produced: it is a shared identity with a
// fixed OTP and would silently break isolation.
func newPhone(operator int) string {
	for {
		phone := fmt.Sprintf("+79%02d%07d", operator%100, randBelow(10_000_000))
		if phone != ReservedTestPhone {
			return phone
		}
	}
}

// randBelow returns a uniform value in [0, n) using the system CSPRNG, falling
// back to math/rand if the OS source is unavailable. Crypto-grade randomness
// is not a security requirement here — it is what keeps parallel runs on the
// same stand from colliding on a phone number.
func randBelow(n int) int {
	if n <= 0 {
		return 0
	}
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return mathrand.Intn(n)
	}
	return int(binary.BigEndian.Uint64(buf[:]) % uint64(n))
}

// newSuffix returns a short numeric suffix for logins and display names.
func newSuffix() int { return 100_000 + randBelow(900_000) }

// NewClientPhone returns a fresh, backend-valid client phone number. Exported
// for suites that drive the auth endpoints directly instead of going through
// the fixture manager, so they never collide on a phone's OTP state.
func NewClientPhone() string { return newPhone(operatorClient) }
