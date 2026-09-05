package fixtures

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	mathrand "math/rand"
	"sync/atomic"
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

var phoneCounter uint64

func init() {
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err == nil {
		phoneCounter = binary.BigEndian.Uint64(buf[:])
	} else {
		phoneCounter = uint64(mathrand.Int63())
	}
}

// newPhone returns a syntactically valid RF mobile number in the exact shape
// normalizePhoneNumber accepts: +7 followed by ten digits.
//
// Uses a full linear congruential permutation over 10^7 so consecutive calls
// are mathematically guaranteed to never collide within 10,000,000 draws.
func newPhone(operator int) string {
	for {
		seq := atomic.AddUint64(&phoneCounter, 1)
		// 32452843 is coprime to 10_000_000 (factors 2 and 5), guaranteeing full period
		val := (seq*32452843 + 1) % 10_000_000
		phone := fmt.Sprintf("+79%02d%07d", operator%100, val)
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
