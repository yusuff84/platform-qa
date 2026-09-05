package fixtures

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"locali-e2e-engine/pkg/client"
)

// Register cooldown handling. The backend refuses a second OTP for the same
// phone within 60 seconds (ClientController.registerClient) and reports the
// remaining time in the message. Fresh random phones never hit this; a pinned
// phone (FIXTURE_CLIENT_PHONE / USE_TEST_PHONE) does, and waiting it out beats
// failing the whole suite.
const (
	registerMaxAttempts = 3
	registerMaxWait     = 75 * time.Second
)

// OTP source labels, reported through the fixture log so a failed login can be
// traced back to where the code came from.
const (
	otpSourceDebug    = "debugCode (ответ register, стенд в DEBUG)"
	otpSourceConfig   = "код стенда (VERIFICATION_CODE / verifyCode)"
	otpSourceTestUser = "фиксированный код тестового номера"
	otpSourceOperator = "ручной ввод оператора"
	otpSourceMock     = "встроенный мок"
)

// presetIdentity is a ready-to-use account bound to a role: either a token from
// the engine config or an account the operator picked in the vault. No
// register/login happens for that role — isolation between runs is knowingly
// traded for a fixed, known identity.
//
// EntityID is the id the backend knows the account by (client_id, rest_id,
// courier_id), read from the token's user_id claim. It is not decorative: the
// admin API assigns orders by courier_id, and an order is placed against a
// rest_id, so a preset without it cannot participate in a flow.
type presetIdentity struct {
	Identifier string
	EntityID   string
	Token      string
	Label      string
}

// resolve fills EntityID from the token claims and returns an error when the
// token cannot serve the role it was bound to.
func newPresetIdentity(role, label, identifier, token string) (*presetIdentity, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("для роли %s не передан токен", role)
	}
	if err := client.VerifyRole(token, role); err != nil {
		return nil, fmt.Errorf("аккаунт для роли %s: %w", role, err)
	}
	claims, err := client.DecodeClaims(token)
	if err != nil {
		return nil, fmt.Errorf("аккаунт для роли %s: %w", role, err)
	}
	if identifier == "" {
		identifier = claims.PhoneNumber
	}
	if label == "" {
		label = identifier
	}
	return &presetIdentity{
		Identifier: identifier,
		EntityID:   claims.UserID,
		Token:      token,
		Label:      label,
	}, nil
}

// FixtureManager generates and registers isolated test entities with teardown capability
type FixtureManager struct {
	mu         sync.Mutex
	clientAPI  *client.ClientAPI
	restAPI    *client.RestAPI
	courierAPI *client.CourierAPI
	adminAPI   *client.AdminAPI
	cleanups   []func(ctx context.Context) error

	verificationCode string
	city             string
	latitude         float64
	longitude        float64
	pinnedPhone      string
	courierGroupID   string
	onInputRequest   func(runID, phone string)
	logf             func(format string, args ...interface{})
	presetClient     *presetIdentity
	presetRest       *presetIdentity
	presetCourier    *presetIdentity
	presetAdmin      *presetIdentity
}

// InputCtxKey is the context key under which the orchestrator exposes the
// interactive verification-code requester for the duration of a run.
type InputCtxKey struct{}

// InputRequester pauses the run and asks the operator for the one-time code
// delivered to the client's Telegram group.
type InputRequester interface {
	RequestVerificationCode(ctx context.Context, notify func(runID, phone string), phone string) (string, error)
}

func inputRequesterFromCtx(ctx context.Context) InputRequester {
	if v, ok := ctx.Value(InputCtxKey{}).(InputRequester); ok && v != nil {
		return v
	}
	return nil
}

// SetOnInputRequest installs the callback invoked right before the manager
// starts waiting for an operator-entered code (the server wires it to UI events)
func (fm *FixtureManager) SetOnInputRequest(fn func(runID, phone string)) {
	fm.mu.Lock()
	fm.onInputRequest = fn
	fm.mu.Unlock()
}

// SetLogger replaces the destination of fixture progress notes (which OTP
// source was used, how long a rate limit was waited out).
func (fm *FixtureManager) SetLogger(fn func(format string, args ...interface{})) {
	fm.mu.Lock()
	fm.logf = fn
	fm.mu.Unlock()
}

// NewFixtureManager creates a new fixture manager
func NewFixtureManager(c *client.ClientAPI, r *client.RestAPI, cr *client.CourierAPI, a *client.AdminAPI) *FixtureManager {
	return &FixtureManager{
		clientAPI:        c,
		restAPI:          r,
		courierAPI:       cr,
		adminAPI:         a,
		cleanups:         make([]func(ctx context.Context) error, 0),
		verificationCode: "",
		city:             "Москва",
		latitude:         55.7649,
		longitude:        37.6055,
		logf:             log.Printf,
	}
}

// SetVerificationCode injects the backend verification code from the engine config.
// An empty value clears the universal code: the manager then relies on the
// debugCode returned by register, falling back to the operator prompt.
func (fm *FixtureManager) SetVerificationCode(code string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.verificationCode = code
}

// SetCity overrides the city sent when registering fixture restaurants. The
// backend only accepts cities present in the platform directory, so a stand
// seeded elsewhere needs this.
func (fm *FixtureManager) SetCity(city string) {
	city = strings.TrimSpace(city)
	if city == "" {
		return
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.city = city
}

// SetCourierGroupID sets the courier group assigned to fixture couriers.
//
// regCourier writes the request's deliveryType directly into the group_id
// foreign key, so only an existing group UUID is accepted. Anything else — the
// plain "car" this engine used to send — fails the constraint and the courier
// is never created. Empty leaves the field out entirely: the backend accepts a
// courier without a group, and that needs no stand-specific data.
func (fm *FixtureManager) SetCourierGroupID(id string) {
	id = strings.TrimSpace(id)
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.courierGroupID = id
}

// SetCoordinates sets the point used for fixture restaurants and client
// addresses. It must lie inside the configured city: Locali delivery is priced
// by distance and refuses an address outside the point's reach.
func (fm *FixtureManager) SetCoordinates(lat, lon float64) {
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.latitude, fm.longitude = lat, lon
}

// PinClientPhone forces every fixture client onto one phone number instead of
// a freshly generated one. Isolation is traded for a stable identity.
func (fm *FixtureManager) PinClientPhone(phone string) {
	phone = strings.TrimSpace(phone)
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.pinnedPhone = phone
}

// SetPresetToken binds a ready-made token to a role. The matching Create*
// method then returns that account instead of registering a new one.
//
// An empty token clears the binding, putting the role back to generating a
// fresh identity per run. A token whose claims do not match the role is
// rejected: silently testing as the wrong role is worse than failing here.
func (fm *FixtureManager) SetPresetToken(role, token string) error {
	return fm.BindAccount(role, "", "", token)
}

// BindAccount binds a named account to a role, labelled for the run log so an
// operator reading it can tell which identity a suite actually used.
func (fm *FixtureManager) BindAccount(role, label, identifier, token string) error {
	canonical := canonicalRole(role)
	if canonical == "" {
		return fmt.Errorf("неизвестная роль: %s", role)
	}

	if strings.TrimSpace(token) == "" {
		fm.mu.Lock()
		defer fm.mu.Unlock()
		fm.setPresetLocked(canonical, nil)
		return nil
	}

	preset, err := newPresetIdentity(canonical, label, identifier, token)
	if err != nil {
		return err
	}
	if canonical != "admin" && preset.EntityID == "" {
		return fmt.Errorf("аккаунт для роли %s: в токене нет user_id, назначение заказов работать не будет", canonical)
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.setPresetLocked(canonical, preset)
	return nil
}

// BoundAccount reports the account currently bound to a role, if any.
func (fm *FixtureManager) BoundAccount(role string) (label, identifier, entityID string, bound bool) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	p := fm.presetLocked(canonicalRole(role))
	if p == nil {
		return "", "", "", false
	}
	return p.Label, p.Identifier, p.EntityID, true
}

func (fm *FixtureManager) setPresetLocked(canonical string, p *presetIdentity) {
	switch canonical {
	case "client":
		fm.presetClient = p
	case "rest":
		fm.presetRest = p
	case "courier":
		fm.presetCourier = p
	case "admin":
		fm.presetAdmin = p
	}
}

func (fm *FixtureManager) presetLocked(canonical string) *presetIdentity {
	switch canonical {
	case "client":
		return fm.presetClient
	case "rest":
		return fm.presetRest
	case "courier":
		return fm.presetCourier
	case "admin":
		return fm.presetAdmin
	}
	return nil
}

// canonicalRole normalizes the role aliases used across the engine and the UI.
func canonicalRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "client":
		return "client"
	case "rest", "restaurant":
		return "rest"
	case "courier":
		return "courier"
	case "admin", "director":
		return "admin"
	}
	return ""
}

// RegisterCleanup adds a cleanup function to be executed during Teardown
func (fm *FixtureManager) RegisterCleanup(fn func(ctx context.Context) error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.cleanups = append(fm.cleanups, fn)
}

// Teardown executes all cleanup hooks in reverse LIFO order
func (fm *FixtureManager) Teardown(ctx context.Context) []error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	var errs []error
	for i := len(fm.cleanups) - 1; i >= 0; i-- {
		if err := fm.cleanups[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	fm.cleanups = nil
	return errs
}

// Logf writes a line into the fixture log. Exported so the orchestrator can
// report API-suite setup through the same channel as fixture progress.
func (fm *FixtureManager) Logf(format string, args ...interface{}) {
	fm.note(format, args...)
}

// note reports fixture progress without holding the manager lock.
func (fm *FixtureManager) note(format string, args ...interface{}) {
	fm.mu.Lock()
	logf := fm.logf
	fm.mu.Unlock()
	if logf == nil {
		return
	}
	logf(format, args...)
}

// CreateUniqueClient registers and logs in a unique client.
// If a preset CLIENT_TOKEN is configured, returns it without register/login (isolation sacrificed).
func (fm *FixtureManager) CreateUniqueClient(ctx context.Context) (phone, token string, err error) {
	fm.mu.Lock()
	preset := fm.presetClient
	pinned := fm.pinnedPhone
	fm.mu.Unlock()

	if preset != nil {
		fm.note("[FIXTURE] клиент: используется привязанный аккаунт %s", preset.Label)
		return preset.Identifier, preset.Token, nil
	}

	phone = pinned
	if phone == "" {
		phone = newPhone(operatorClient)
	}

	regResp, err := fm.registerClient(ctx, phone)
	if err != nil {
		return "", "", err
	}

	code, source, err := fm.resolveOTP(ctx, phone, regResp)
	if err != nil {
		return "", "", err
	}
	fm.note("[FIXTURE] клиент %s: код верификации получен — %s", phone, source)

	loginReq := client.ClientLoginRequest{
		PhoneNumber:      phone,
		VerificationCode: code,
		FirstName:        "TestClient",
		LastName:         "Auto",
	}
	// A wrong code is never retried: the backend counts attempts and locks the
	// phone out after five (429), so a blind retry would burn the budget of the
	// next run on this number.
	token, err = fm.clientAPI.Login(ctx, loginReq)
	if err != nil {
		return "", "", fmt.Errorf("вход клиента %s не выполнен (код из источника «%s»): %w%s", phone, source, err, otpHint(err))
	}

	if verr := client.VerifyRole(token, "client"); verr != nil {
		return "", "", fmt.Errorf("клиент %s: %w", phone, verr)
	}

	return phone, token, nil
}

// CreateUniqueClientIsolated creates and authenticates a unique client without mutating the shared SessionMgr.
func (fm *FixtureManager) CreateUniqueClientIsolated(ctx context.Context) (phone, token string, err error) {
	fm.mu.Lock()
	pinned := fm.pinnedPhone
	fm.mu.Unlock()

	phone = pinned
	if phone == "" {
		phone = newPhone(operatorClient)
	}

	regResp, err := fm.registerClient(ctx, phone)
	if err != nil {
		return "", "", err
	}

	code, source, err := fm.resolveOTP(ctx, phone, regResp)
	if err != nil {
		return "", "", err
	}

	loginReq := client.ClientLoginRequest{
		PhoneNumber:      phone,
		VerificationCode: code,
		FirstName:        "TestClient",
		LastName:         "Auto",
	}
	token, err = fm.clientAPI.LoginWithoutSession(ctx, loginReq)
	if err != nil {
		return "", "", fmt.Errorf("вход клиента %s не выполнен (код из источника «%s»): %w%s", phone, source, err, otpHint(err))
	}

	if verr := client.VerifyRole(token, "client"); verr != nil {
		return "", "", fmt.Errorf("клиент %s: %w", phone, verr)
	}

	return phone, token, nil
}

// registerClient requests an OTP, waiting out the 60-second resend cooldown
// when the phone is pinned and a previous run just used it.
func (fm *FixtureManager) registerClient(ctx context.Context, phone string) (*client.ClientRegisterResponse, error) {
	fm.mu.Lock()
	city := fm.city
	fm.mu.Unlock()

	regReq := client.ClientRegisterRequest{
		PhoneNumber: phone,
		Address:     "ул. Тестовая, д. 10, кв. 42",
		CityKey:     city,
	}

	var lastErr error
	for attempt := 1; attempt <= registerMaxAttempts; attempt++ {
		resp, err := fm.clientAPI.Register(ctx, regReq)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !client.IsRateLimited(err) {
			return nil, fmt.Errorf("регистрация клиента %s: %w", phone, err)
		}

		wait := time.Duration(client.RetryAfter(err)) * time.Second
		if wait <= 0 || wait > registerMaxWait {
			return nil, fmt.Errorf("регистрация клиента %s упёрлась в лимит отправки кода (%w). "+
				"Уберите FIXTURE_CLIENT_PHONE/USE_TEST_PHONE, чтобы каждый прогон брал новый номер", phone, err)
		}
		if attempt == registerMaxAttempts {
			break
		}

		fm.note("[FIXTURE] клиент %s: лимит отправки кода, ждём %s (попытка %d/%d)", phone, wait, attempt, registerMaxAttempts)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("регистрация клиента %s прервана во время ожидания лимита: %w", phone, ctx.Err())
		case <-time.After(wait + time.Second):
		}
	}

	return nil, fmt.Errorf("регистрация клиента %s: лимит отправки кода не снялся за %d попытки: %w", phone, registerMaxAttempts, lastErr)
}

// resolveOTP picks the one-time code, preferring fully automated sources.
//
// Order matters:
//  1. debugCode from this very register response — authoritative for this
//     phone, present on any stand running with DEBUG=true;
//  2. the code configured for the stand (a stand that accepts a universal code);
//  3. the fixed code of the reserved test number;
//  4. the operator prompt (real stand, one-shot codes arriving in Telegram);
//  5. the embedded mock, which accepts anything.
func (fm *FixtureManager) resolveOTP(ctx context.Context, phone string, reg *client.ClientRegisterResponse) (code, source string, err error) {
	fm.mu.Lock()
	configured := fm.verificationCode
	requestInput := fm.onInputRequest
	fm.mu.Unlock()

	if reg != nil && reg.DebugCode != "" {
		return reg.DebugCode, otpSourceDebug, nil
	}
	if configured != "" {
		return configured, otpSourceConfig, nil
	}
	if phone == ReservedTestPhone {
		return ReservedTestCode, otpSourceTestUser, nil
	}

	if ref := inputRequesterFromCtx(ctx); ref != nil {
		entered, ierr := ref.RequestVerificationCode(ctx, requestInput, phone)
		if ierr != nil {
			return "", "", fmt.Errorf("клиент %s: %w", phone, ierr)
		}
		if strings.TrimSpace(entered) == "" {
			return "", "", fmt.Errorf("клиент %s: получен пустой код верификации", phone)
		}
		return strings.TrimSpace(entered), otpSourceOperator, nil
	}

	// No run context (go test / dsl path): the embedded mock accepts any
	// non-empty code.
	return "1234", otpSourceMock, nil
}

// otpHint turns the backend's OTP refusals into an actionable next step.
func otpHint(err error) string {
	apiErr, ok := client.AsAPIError(err)
	if !ok {
		return ""
	}
	switch apiErr.StatusCode {
	case 404:
		return "\nПодсказка: номер не зарегистрирован — register не дошёл до бэкенда."
	case 429:
		return "\nПодсказка: исчерпаны попытки ввода кода (лимит 5). Нужен новый код: перезапустите прогон."
	case 400:
		return "\nПодсказка: код неверен или истёк (TTL 5 минут). Если стенд запущен без DEBUG=true, " +
			"debugCode в ответе register отсутствует — задайте VERIFICATION_CODE стенда или включите ручной ввод."
	}
	return ""
}

// CreateUniqueRestaurant registers and logs in a unique restaurant.
// If a preset REST_TOKEN is configured, returns it without register/login (isolation sacrificed).
func (fm *FixtureManager) CreateUniqueRestaurant(ctx context.Context) (restID, token string, err error) {
	fm.mu.Lock()
	preset := fm.presetRest
	city := fm.city
	lat, lon := fm.latitude, fm.longitude
	fm.mu.Unlock()

	if preset != nil {
		fm.note("[FIXTURE] ресторан: используется привязанный аккаунт %s", preset.Label)
		return preset.Identifier, preset.Token, nil
	}

	suffix := newSuffix()
	login := fmt.Sprintf("rest_e2e_%d", suffix)
	password := "SecretPass123!"
	name := fmt.Sprintf("Restaurant E2E #%d", suffix)

	regReq := client.RestRegisterRequest{
		RestName:       name,
		Login:          login,
		Password:       password,
		Email:          fmt.Sprintf("%s@test.locali", login),
		PhoneNumber:    newPhone(operatorRest),
		City:           city,
		Street:         "Тестовая",
		House:          "10",
		DeliveryMethod: "locali",
		// Without these flags every receive method is disabled and the
		// restaurant refuses orders with RECEIVE_METHOD_DISABLED.
		DeliveryEnabled: true,
		PickupEnabled:   true,
		Latitude:        &lat,
		Longitude:       &lon,
	}

	if err := fm.restAPI.Register(ctx, regReq); err != nil {
		return "", "", fmt.Errorf("регистрация ресторана %s: %w%s", login, err, restHint(err, city))
	}

	loginReq := client.RestLoginRequest{
		Login:    login,
		Password: password,
	}
	token, err = fm.restAPI.Login(ctx, loginReq)
	if err != nil {
		return "", "", fmt.Errorf("вход ресторана %s: %w", login, err)
	}

	if verr := client.VerifyRole(token, "rest"); verr != nil {
		return "", "", fmt.Errorf("ресторан %s: %w", login, verr)
	}

	return login, token, nil
}

// restHint explains the two rejections that depend on stand data rather than
// on the request being malformed.
func restHint(err error, city string) string {
	apiErr, ok := client.AsAPIError(err)
	if !ok || apiErr.StatusCode != 400 {
		return ""
	}
	if len(apiErr.Fields) > 0 {
		return fmt.Sprintf("\nПодсказка: бэкенд считает обязательными поля %s.", strings.Join(apiErr.Fields, ", "))
	}
	return fmt.Sprintf("\nПодсказка: город «%s» должен быть в справочнике платформы "+
		"(supportedCities активного тарифа Locali). Задайте FIXTURE_CITY под свой стенд.", city)
}

// CreateUniqueCourier registers and logs in a unique courier and returns its
// courier_id — the identifier the admin API assigns orders by. The login phone
// is not that identifier: give-order-to-courier looks the courier up by its
// UUID and answers 404 for anything else.
//
// If a preset COURIER_TOKEN is configured, returns it without register/login
// (isolation sacrificed).
func (fm *FixtureManager) CreateUniqueCourier(ctx context.Context) (courierID, token string, err error) {
	fm.mu.Lock()
	preset := fm.presetCourier
	city := fm.city
	groupID := fm.courierGroupID
	fm.mu.Unlock()

	if preset != nil {
		// The courier_id is what the admin API assigns orders by, so a bound
		// courier must hand back its id, not its phone.
		fm.note("[FIXTURE] курьер: используется привязанный аккаунт %s", preset.Label)
		return preset.EntityID, preset.Token, nil
	}

	suffix := newSuffix()
	phone := newPhone(operatorCourier)
	login := phone
	password := "CourierPass123!"
	surname := fmt.Sprintf("Num%d", suffix)

	regReq := client.CourierRegisterRequest{
		FirstName:    "FastCourier",
		Surname:      surname,
		LastName:     surname,
		Patronymic:   "Тестович",
		Gender:       "М",
		Email:        fmt.Sprintf("courier_%d@test.locali", suffix),
		PhoneNumber:  phone,
		Login:        login,
		Password:     password,
		DeliveryType: groupID, // omitted when empty — see SetCourierGroupID
		CityKey:      city,
	}

	if err := fm.courierAPI.Register(ctx, regReq); err != nil {
		return "", "", fmt.Errorf("регистрация курьера %s: %w", phone, err)
	}

	loginReq := client.CourierLoginRequest{
		PhoneNumber: login,
		Password:    password,
	}
	token, err = fm.courierAPI.Login(ctx, loginReq)
	if err != nil {
		return "", "", fmt.Errorf("вход курьера %s: %w", phone, err)
	}

	if verr := client.VerifyRole(token, "courier"); verr != nil {
		return "", "", fmt.Errorf("курьер %s: %w", phone, verr)
	}

	claims, cerr := client.DecodeClaims(token)
	if cerr != nil || claims.UserID == "" {
		return "", "", fmt.Errorf("курьер %s: в токене нет courier_id", phone)
	}

	return claims.UserID, token, nil
}
