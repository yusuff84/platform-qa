package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains runtime settings for E2E Engine
type Config struct {
	BaseURL       string
	NatsURL       string
	AdminLogin    string
	AdminPassword string
	ClientToken   string
	RestToken     string
	CourierToken  string
	AdminToken    string
	RequestTimeout time.Duration
	PollInterval  time.Duration

	// VerificationCode is the default SMS verification code accepted by the real backend
	VerificationCode string
	// DataDir is the working directory for persisted artifacts (user scenarios etc.)
	DataDir string

	// FixtureCity is the city sent when registering fixture restaurants. The
	// backend geocodes it and rejects anything outside the platform directory
	// (supportedCities of the active Locali tariff, LOCALIAPP-2387), so a stand
	// seeded with a different city needs this override.
	FixtureCity string
	// FixtureClientPhone pins the phone used for fixture clients instead of
	// generating a fresh one. Pinning trades isolation for a stable identity
	// and makes the register rate limit (60s per phone) observable.
	FixtureClientPhone string
	// UseTestPhone pins the DEBUG-only backdoor identity (+79999999999 / 9999)
	// as the fixture client. Works only while the backend runs with DEBUG=true.
	UseTestPhone bool
	// FixtureLatitude / FixtureLongitude are the coordinates used for fixture
	// restaurants and client addresses. They must fall inside FixtureCity —
	// Locali delivery is priced by distance and refuses an address outside the
	// point's reach.
	FixtureLatitude  float64
	FixtureLongitude float64
	// FixtureCourierGroupID is the courier group assigned to fixture couriers.
	// regCourier writes deliveryType straight into the group_id foreign key, so
	// only an existing group UUID is accepted (GET /api/admin/groups lists
	// them). Empty leaves the courier without a group, which the backend
	// accepts — that is the default because it needs no stand-specific data.
	FixtureCourierGroupID string
	// HTTPDebug mirrors every request/response into the engine log with secrets
	// masked.
	HTTPDebug bool

	// TelegramBotToken / TelegramChatID enable failure alerts via the
	// Telegram Bot API. Both empty (default) keeps alerting disabled.
	TelegramBotToken string `json:"-"`
	TelegramChatID   string `json:"-"`
}

// LoadFromEnv loads configuration with sensible defaults and environment overrides
func LoadFromEnv() *Config {
	baseURL := getEnv("BASE_URL", "http://localhost:3000")
	natsURL := getEnv("NATS_URL", "nats://localhost:4222")
	adminLogin := getEnv("ADMIN_LOGIN", "")
	adminPassword := getEnv("ADMIN_PASSWORD", "")

	timeoutSec, _ := strconv.Atoi(getEnv("REQUEST_TIMEOUT_SEC", "15"))
	if timeoutSec <= 0 {
		timeoutSec = 15
	}

	return &Config{
		BaseURL:        baseURL,
		NatsURL:        natsURL,
		AdminLogin:     adminLogin,
		AdminPassword:  adminPassword,
		ClientToken:    os.Getenv("CLIENT_TOKEN"),
		RestToken:      os.Getenv("REST_TOKEN"),
		CourierToken:   os.Getenv("COURIER_TOKEN"),
		AdminToken:     os.Getenv("ADMIN_TOKEN"),
		RequestTimeout: time.Duration(timeoutSec) * time.Second,
		PollInterval:   500 * time.Millisecond,

		VerificationCode: os.Getenv("VERIFICATION_CODE"),
		DataDir:          getEnv("DATA_DIR", "./data"),

		FixtureCity:        getEnv("FIXTURE_CITY", "Москва"),
		FixtureClientPhone: os.Getenv("FIXTURE_CLIENT_PHONE"),
		UseTestPhone:       isTruthy(os.Getenv("USE_TEST_PHONE")),

		FixtureLatitude:       getEnvFloat("FIXTURE_LATITUDE", 55.7649),
		FixtureLongitude:      getEnvFloat("FIXTURE_LONGITUDE", 37.6055),
		FixtureCourierGroupID: os.Getenv("FIXTURE_COURIER_GROUP_ID"),
		HTTPDebug:          isTruthy(os.Getenv("HTTP_DEBUG")),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// isTruthy accepts the usual on/off spellings so a stand can be flipped from
// a shell, a compose file or a CI variable without surprises.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// getEnvFloat reads a float setting, falling back to the default when the
// variable is unset or unparsable.
func getEnvFloat(key string, defaultVal float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultVal
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return defaultVal
	}
	return v
}
