package testserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"locali-e2e-engine/pkg/client"
)

// MockBackend simulates the Locali microservice ecosystem with strict RBAC, idempotency and edge-case handling
type MockBackend struct {
	server       *httptest.Server
	mu           sync.RWMutex
	orders       map[string]*client.OrderResponse
	orderSeq     int
	clients      map[string]string // phone -> token
	rests        map[string]string // login -> token
	couriers     map[string]string // phone -> token
	dishes       map[int]*client.DishResponse
	dishSeq      int
	idempotency  map[string]*client.OrderResponse // key -> response

	// auth mirrors the backend's identity + OTP state so authentication
	// behaves the same here as on a real DEBUG stand.
	auth *authStore

	// cart holds each client's server-side cart. The restaurant order is
	// assembled from it, so an order placed against an empty cart must fail
	// here exactly as it does on a stand.
	cart      map[string][]cartItem
	addresses map[string]string // address_id -> client_id
}

// cartItem is one line of a client's cart.
type cartItem struct {
	CartItemID string
	EntityID   string
	EntityType string
	Quantity   int
	Price      float64
}

// NewMockBackend creates and starts an HTTP mock server
func NewMockBackend() *MockBackend {
	mb := &MockBackend{
		orders:      make(map[string]*client.OrderResponse),
		clients:     make(map[string]string),
		rests:       make(map[string]string),
		couriers:    make(map[string]string),
		dishes:      make(map[int]*client.DishResponse),
		idempotency: make(map[string]*client.OrderResponse),
		auth:        newAuthStore(),
		cart:        make(map[string][]cartItem),
		addresses:   make(map[string]string),
	}

	mux := http.NewServeMux()

	// Admin
	mux.HandleFunc("/api/admin/login", mb.handleAdminLogin)
	mux.HandleFunc("/api/admin/give-order-to-courier", mb.authGuard("admin", mb.handleAdminAssignCourier))
	mux.HandleFunc("/api/admin/cancel-order", mb.authGuard("admin", mb.handleAdminCancelOrder))
	mux.HandleFunc("/api/admin/admin-single", mb.authGuard("admin", mb.handleAdminGetOrder))
	mux.HandleFunc("/api/admin/clients", mb.authGuard("admin", mb.handleAdminListClients))

	// Client
	mux.HandleFunc("/api/clients/register", mb.handleClientRegister)
	mux.HandleFunc("/api/clients/login", mb.handleClientLogin)
	mux.HandleFunc("/api/clients/create-order", mb.authGuard("client", mb.handleClientCreateOrder))
	mux.HandleFunc("/api/clients/independent-order", mb.authGuard("client", mb.handleClientCreateIndependentOrder))
	mux.HandleFunc("/api/clients/orders/", mb.authGuard("client", mb.handleClientOrderOperations))
	mux.HandleFunc("/api/clients/active-orders/", mb.authGuard("client", mb.handleClientGetOrder))
	mux.HandleFunc("/api/clients/all-orders/", mb.authGuard("client", mb.handleClientGetFinalOrder))
	mux.HandleFunc("/api/clients/courier-types", mb.authGuard("client", mb.handleCourierTypes))

	mux.HandleFunc("/api/clients/addresses", mb.authGuard("client", mb.handleClientAddresses))
	mux.HandleFunc("/api/cart/items", mb.authGuard("client", mb.handleCartItems))
	mux.HandleFunc("/api/cart/groups/", mb.authGuard("client", mb.handleCartGroup))
	mux.HandleFunc("/api/clients/phone-change/start", mb.authGuard("client", mb.handlePhoneChangeStart))
	mux.HandleFunc("/api/clients/phone-change/verify-old", mb.authGuard("client", mb.handlePhoneChangeVerifyOld))
	mux.HandleFunc("/api/clients/phone-change/cancel", mb.authGuard("client", mb.handlePhoneChangeCancel))

	// Restaurant
	mux.HandleFunc("/api/rests/register", mb.handleRestRegister)
	mux.HandleFunc("/api/rests/login", mb.handleRestLogin)
	mux.HandleFunc("/api/rests/password-reset/request", mb.handleRestResetRequest)
	mux.HandleFunc("/api/rests/password-reset/verify", mb.handleRestResetVerify)
	mux.HandleFunc("/api/rests/password-reset/confirm", mb.handleRestResetConfirm)
	mux.HandleFunc("/api/rests/order-status", mb.authGuard("rest", mb.handleRestOrderStatus))
	mux.HandleFunc("/api/rests/dishes", mb.authGuard("rest", mb.handleRestDishes))
	mux.HandleFunc("/api/rests/categories", mb.authGuard("rest", mb.handleRestCategories))
	mux.HandleFunc("/api/rests/schedule", mb.authGuard("rest", mb.handleRestSchedule))

	// Courier
	mux.HandleFunc("/api/couriers/register", mb.handleCourierRegister)
	mux.HandleFunc("/api/couriers/login", mb.handleCourierLogin)
	mux.HandleFunc("/api/couriers/take-order", mb.authGuard("courier", mb.handleCourierTakeOrder))
	mux.HandleFunc("/api/couriers/change-status", mb.authGuard("courier", mb.handleCourierChangeStatus))

	mb.server = httptest.NewServer(mux)
	return mb
}

// URL returns the mock server address
func (mb *MockBackend) URL() string {
	return mb.server.URL
}

// Close stops the server
func (mb *MockBackend) Close() {
	mb.server.Close()
}

// authGuard mirrors middlewares/authorization.js plus the per-area role gates
// (adminOnly, courierOnly): the same statuses and the same machine codes, so
// an RBAC assertion written against the mock holds on a real stand.
func (mb *MockBackend) authGuard(requiredRole string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, http.StatusUnauthorized, "NO_TOKEN_INCLUDED", "Токен не передан")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		claims, ok := mockClaims(token)
		if !ok {
			writeError(w, http.StatusUnauthorized, "INVALID_TOKEN", "Токен не разобран")
			return
		}

		isAdmin := claimBool(claims, "isAdmin")
		isCourier := claimBool(claims, "isCourier")
		isRestaurant := claimBool(claims, "isRestaurant")

		switch requiredRole {
		case "admin":
			if !isAdmin {
				writeError(w, http.StatusForbidden, "ADMIN_ONLY", "Доступ только для администратора")
				return
			}
		case "courier":
			// courierOnly also lets an admin/service token through — the
			// distribution service calls take-order with courier_id in body.
			if !isCourier && !isAdmin {
				writeError(w, http.StatusForbidden, "COURIER_ONLY", "Доступ только для курьера")
				return
			}
		case "rest":
			if !isRestaurant && !isAdmin {
				writeError(w, http.StatusForbidden, "REST_ONLY", "Доступ только для ресторана")
				return
			}
		case "client":
			if isCourier || isRestaurant {
				writeError(w, http.StatusForbidden, "CLIENT_ONLY", "Доступ только для клиента")
				return
			}
		}

		next(w, r)
	}
}

func (mb *MockBackend) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req client.AdminLoginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	// The real backend signs the super-root token with user_id:null, so the
	// mock does the same — the engine must tolerate an admin without identity.
	token := mockJWT(map[string]interface{}{
		"isAdmin":  true,
		"rights":   []string{"general1", "order1", "admin1"},
		"user_id":  nil,
		"admin_id": nil,
	})
	writeJSON(w, http.StatusOK, token)
}

// handleClientRegister mirrors ClientController.registerClient: phone
// normalization, the 60-second resend cooldown, and the debugCode the backend
// returns while DEBUG=true.
func (mb *MockBackend) handleClientRegister(w http.ResponseWriter, r *http.Request) {
	var req client.ClientRegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	phone := normalizePhone(req.PhoneNumber)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"message": "Поддерживаются только номера РФ формата +7XXXXXXXXXX",
		})
		return
	}

	mb.auth.mu.Lock()
	defer mb.auth.mu.Unlock()

	now := time.Now()
	isTestPhone := phone == testPhone
	prev := mb.auth.otps[phone]

	if !isTestPhone && prev != nil && now.Sub(prev.SentAt) < otpResendWindow {
		retryAfter := int((otpResendWindow - now.Sub(prev.SentAt)).Seconds()) + 1
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"message":    fmt.Sprintf("Код уже отправлен. Повторно можно через %d сек.", retryAfter),
			"retryAfter": retryAfter,
		})
		return
	}

	code := testCode
	if !isTestPhone {
		code = fmt.Sprintf("%04d", 1000+mb.auth.seq%9000)
		mb.auth.seq++
	}
	mb.auth.otps[phone] = &otpRecord{Code: code, ExpiresAt: now.Add(otpTTL), SentAt: now}

	_, known := mb.auth.clientID[phone]
	if !known {
		mb.auth.clientID[phone] = mb.auth.nextID("client")
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":   "success",
		"firstTime": !known,
		"debugCode": code,
	})
}

// handleClientLogin mirrors ClientController.loginClient: TTL, attempt budget
// and the exact statuses each refusal carries.
func (mb *MockBackend) handleClientLogin(w http.ResponseWriter, r *http.Request) {
	var req client.ClientLoginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	phone := normalizePhone(req.PhoneNumber)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"message": "Поддерживаются только номера РФ формата +7XXXXXXXXXX",
		})
		return
	}
	if req.VerificationCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Не указан проверочный код."})
		return
	}

	mb.auth.mu.Lock()
	defer mb.auth.mu.Unlock()

	clientID, registered := mb.auth.clientID[phone]
	if !registered {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"message": "Клиент с таким номером не найден. Сначала запросите код.",
		})
		return
	}

	rec := mb.auth.otps[phone]
	if rec == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"message": "Код не запрошен или уже истёк. Запросите новый код.",
		})
		return
	}
	if time.Now().After(rec.ExpiresAt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"message": "Срок действия проверочного кода истёк. Запросите новый код.",
		})
		return
	}
	if rec.Attempts >= otpMaxAttempts {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"message": "Превышено количество попыток. Запросите новый код.",
		})
		return
	}
	if req.VerificationCode != rec.Code {
		rec.Attempts++
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Неверный код верификации."})
		return
	}

	// A consumed code is cleared, exactly as the backend clears it: replaying
	// the same code must not authenticate twice.
	delete(mb.auth.otps, phone)

	token := mockJWT(map[string]interface{}{"user_id": clientID})
	mb.mu.Lock()
	mb.clients[phone] = token
	mb.mu.Unlock()
	writeJSON(w, http.StatusOK, token)
}

// handleRestRegister mirrors the required-fields gate of
// RestController.createRest (LOCALIAPP-2387), including the `fields` list.
func (mb *MockBackend) handleRestRegister(w http.ResponseWriter, r *http.Request) {
	var req client.RestRegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	required := map[string]string{
		"restName": req.RestName,
		"login":    req.Login,
		"password": req.Password,
		"city":     req.City,
	}
	var missing []string
	for _, field := range []string{"city", "login", "password", "restName"} {
		if strings.TrimSpace(required[field]) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "bad_request",
			"fields":  missing,
			"message": "Отсутствуют обязательные поля: " + strings.Join(missing, ", "),
		})
		return
	}

	mb.auth.mu.Lock()
	mb.auth.restPwd[req.Login] = req.Password
	mb.auth.restID[req.Login] = mb.auth.nextID("rest")
	if phone := normalizePhone(req.PhoneNumber); phone != "" {
		mb.auth.restByPh[phone] = req.Login
	}
	mb.auth.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"message": "success"})
}

func (mb *MockBackend) handleRestLogin(w http.ResponseWriter, r *http.Request) {
	var req client.RestLoginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	mb.auth.mu.Lock()
	stored, known := mb.auth.restPwd[req.Login]
	restID := mb.auth.restID[req.Login]
	mb.auth.mu.Unlock()

	if !known || stored != req.Password {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid credentials"})
		return
	}

	token := mockJWT(map[string]interface{}{"user_id": restID, "isRestaurant": true})
	mb.mu.Lock()
	mb.rests[req.Login] = token
	mb.mu.Unlock()
	writeJSON(w, http.StatusOK, token)
}

// handleRestResetRequest mirrors RestController.resetPasswordRestRequest: the
// OTP is bound to the phone and repeats inside 60 seconds are refused.
func (mb *MockBackend) handleRestResetRequest(w http.ResponseWriter, r *http.Request) {
	var req client.RestPasswordResetRequestRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	phone := normalizePhone(req.PhoneNumber)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid phone number"})
		return
	}

	mb.auth.mu.Lock()
	defer mb.auth.mu.Unlock()

	if _, ok := mb.auth.restByPh[phone]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Restaurant not found"})
		return
	}

	key := "rest:" + phone
	now := time.Now()
	if prev := mb.auth.otps[key]; prev != nil && now.Sub(prev.SentAt) < otpResendWindow {
		retryAfter := int((otpResendWindow - now.Sub(prev.SentAt)).Seconds()) + 1
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"error":      fmt.Sprintf("Код уже отправлен. Повторно можно через %d сек.", retryAfter),
			"retryAfter": retryAfter,
		})
		return
	}

	code := fmt.Sprintf("%04d", 1000+mb.auth.seq%9000)
	mb.auth.seq++
	mb.auth.otps[key] = &otpRecord{Code: code, ExpiresAt: now.Add(otpTTL), SentAt: now}

	// The real backend delivers this by SMS only; the mock echoes it so the
	// reset flow is testable end to end.
	writeJSON(w, http.StatusOK, map[string]string{"message": "Code sent to phone", "debugCode": code})
}

func (mb *MockBackend) handleRestResetVerify(w http.ResponseWriter, r *http.Request) {
	var req client.RestPasswordResetVerifyRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	phone := normalizePhone(req.PhoneNumber)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid phone number"})
		return
	}

	mb.auth.mu.Lock()
	defer mb.auth.mu.Unlock()

	login, known := mb.auth.restByPh[phone]
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Restaurant not found"})
		return
	}

	key := "rest:" + phone
	rec := mb.auth.otps[key]
	if rec == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No reset code requested"})
		return
	}
	if time.Now().After(rec.ExpiresAt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Code expired"})
		return
	}

	rec.Attempts++
	if rec.Attempts > otpMaxAttempts {
		delete(mb.auth.otps, key)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "Слишком много неверных попыток. Запросите код заново.",
		})
		return
	}
	if rec.Code != req.Code {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"attemptsLeft": otpMaxAttempts - rec.Attempts,
			"error":        "Invalid code",
		})
		return
	}

	delete(mb.auth.otps, key)
	resetToken := mockJWT(map[string]interface{}{
		"purpose": "password-reset",
		"rest_id": mb.auth.restID[login],
		"login":   login,
		"exp":     time.Now().Add(5 * time.Minute).Unix(),
	})
	writeJSON(w, http.StatusOK, map[string]string{"resetToken": resetToken})
}

func (mb *MockBackend) handleRestResetConfirm(w http.ResponseWriter, r *http.Request) {
	var req client.RestPasswordResetConfirmRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.ResetToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Reset token is required"})
		return
	}
	if req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "New password is required"})
		return
	}
	if msg := validateMockPassword(req.NewPassword); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	claims, ok := mockClaims(req.ResetToken)
	if !ok || claims["purpose"] != "password-reset" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid reset token"})
		return
	}
	if exp, ok := claims["exp"].(float64); ok && time.Now().After(time.Unix(int64(exp), 0)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Reset token has expired"})
		return
	}

	login, _ := claims["login"].(string)
	mb.auth.mu.Lock()
	_, known := mb.auth.restPwd[login]
	if known {
		mb.auth.restPwd[login] = req.NewPassword
	}
	mb.auth.mu.Unlock()

	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Restaurant not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Password successfully updated"})
}

// validateMockPassword mirrors utilities/auth/authHelpers.js validatePassword.
func validateMockPassword(password string) string {
	if len(password) < 8 {
		return "Password must be at least 8 characters long"
	}
	hasUpper, hasDigit := false, false
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasUpper || !hasDigit {
		return "Password must contain uppercase and numbers"
	}
	return ""
}

func (mb *MockBackend) handleCourierRegister(w http.ResponseWriter, r *http.Request) {
	var req client.CourierRegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if normalizePhone(req.PhoneNumber) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"message": "Поддерживаются только номера РФ формата +7XXXXXXXXXX",
		})
		return
	}

	login := req.Login
	if login == "" {
		login = req.PhoneNumber
	}

	mb.auth.mu.Lock()
	mb.auth.courPwd[login] = req.Password
	courierID := mb.auth.nextID("courier")
	mb.auth.courID[login] = courierID
	mb.auth.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"courier_id": courierID,
		"login":      login,
		"message":    "success",
	})
}

func (mb *MockBackend) handleCourierLogin(w http.ResponseWriter, r *http.Request) {
	var req client.CourierLoginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	// The backend looks the courier up by the `login` column using the
	// phoneNumber field of the body — the mock keeps that quirk.
	mb.auth.mu.Lock()
	stored, known := mb.auth.courPwd[req.PhoneNumber]
	courierID := mb.auth.courID[req.PhoneNumber]
	mb.auth.mu.Unlock()

	if !known || stored != req.Password {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Неверный логин или пароль"})
		return
	}

	token := mockJWT(map[string]interface{}{
		"user_id":     courierID,
		"phoneNumber": req.PhoneNumber,
		"isCourier":   true,
	})
	mb.mu.Lock()
	mb.couriers[req.PhoneNumber] = token
	mb.mu.Unlock()
	writeJSON(w, http.StatusOK, token)
}

// Client phone-change guards. Codes travel by SMS on the real backend, so only
// the session guards are reproducible here: without an active session every
// step is refused.
func (mb *MockBackend) handlePhoneChangeStart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"message": "success"})
}

func (mb *MockBackend) handlePhoneChangeVerifyOld(w http.ResponseWriter, r *http.Request) {
	var req client.PhoneChangeVerifyOldRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.OldCode) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Не указан код со старого номера."})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Неверный код со старого номера."})
}

// Cancel is idempotent on the backend: it drops the session and answers 200
// whether or not one existed.
func (mb *MockBackend) handlePhoneChangeCancel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"message": "success"})
}

func (mb *MockBackend) handleClientCreateOrder(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get("Idempotency-Key")

	mb.mu.Lock()
	defer mb.mu.Unlock()

	// Check Idempotency
	if idemKey != "" {
		if cached, exists := mb.idempotency[idemKey]; exists {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cached)
			return
		}
	}

	var req client.CreateRestaurantOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_request"}`, http.StatusBadRequest)
		return
	}

	// Mirrors validators/order/createOrder.validator.js, including the
	// per-field `details` array the engine surfaces.
	var details []map[string]string
	if req.RestID == "" && req.SettingID == "" {
		details = append(details, map[string]string{"field": "rest_id", "message": "rest_id обязателен"})
	}
	if !validReceiveMethods[req.ReceiveMethod] {
		details = append(details, map[string]string{
			"field":   "receiveMethod",
			"message": "receiveMethod должен быть delivery, pickup, takeout, dinein, table или terminal",
		})
	}
	if req.Payment.Type == "" {
		details = append(details, map[string]string{"field": "payment", "message": "payment обязателен"})
	} else if !validPaymentTypes[req.Payment.Type] {
		details = append(details, map[string]string{"field": "payment", "message": "Неверный тип оплаты"})
	}
	if req.Source != "" && !validSources[req.Source] {
		details = append(details, map[string]string{
			"field":   "source",
			"message": "source должен быть одним из: lokali_eda, crispy_app",
		})
	}
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"details": details,
			"error":   "VALIDATION_ERROR",
			"message": "Некорректные параметры заказа",
			"status":  http.StatusBadRequest,
		})
		return
	}

	// The order is assembled from the cart: an empty cart is not an order.
	clientID := mb.clientIDFromRequest(r)
	items := mb.cart[clientID]
	if len(items) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "CART_EMPTY",
			"message": "Корзина пуста",
			"status":  http.StatusBadRequest,
		})
		return
	}
	if req.ReceiveMethod == client.ReceiveDelivery && req.AddressID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "DELIVERY_ADDRESS_REQUIRED",
			"message": "Необходимо выбрать адрес доставки",
			"status":  http.StatusBadRequest,
		})
		return
	}

	total := 0.0
	for _, it := range items {
		total += it.Price * float64(it.Quantity)
	}
	delete(mb.cart, clientID)

	mb.orderSeq++
	orderID := uuid.New().String()
	order := &client.OrderResponse{
		OrderID:         orderID,
		OrderNumber:     1000 + mb.orderSeq,
		Status:          "new",
		CourierStatus:   "",
		ClientID:        clientID,
		RestID:          req.RestID,
		Price:           total,
		DeliveryAddress: req.AddressID,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	mb.orders[orderID] = order

	if idemKey != "" {
		mb.idempotency[idemKey] = order
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleClientCreateIndependentOrder(w http.ResponseWriter, r *http.Request) {
	var req client.CreateIndependentOrderRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Mirrors validators/courier/createCourierOrder.validator.js.
	var details []map[string]string
	coord := func(field string, v float64, limit float64) {
		if v == 0 {
			details = append(details, map[string]string{"field": field, "message": field + " обязателен"})
			return
		}
		if v < -limit || v > limit {
			details = append(details, map[string]string{
				"field":   field,
				"message": fmt.Sprintf("%s вне диапазона -%g..%g", field, limit, limit),
			})
		}
	}
	coord("fromLat", req.FromLat, 90)
	coord("toLat", req.ToLat, 90)
	coord("fromLong", req.FromLong, 180)
	coord("toLong", req.ToLong, 180)

	if !validPaymentTypes[req.PaymentType] {
		details = append(details, map[string]string{
			"field": "paymentType", "message": "paymentType должен быть 'cash' или 'online'",
		})
	}
	if !validCargoTypes[req.CargoType] {
		details = append(details, map[string]string{
			"field": "cargoType", "message": "cargoType должен быть одним из: documents, food, clothes, fragile, goods, personal_items, other",
		})
	}
	if strings.TrimSpace(req.RecipientName) == "" {
		details = append(details, map[string]string{"field": "recipientName", "message": "recipientName обязателен"})
	}
	if normalizePhone(req.RecipientPhone) == "" {
		details = append(details, map[string]string{"field": "recipientPhone", "message": "recipientPhone обязателен"})
	}
	if req.PaymentType == client.PaymentCash && req.ChangeAmount < 0 {
		details = append(details, map[string]string{"field": "change_amount", "message": "change_amount должен быть числом больше 0"})
	}
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"details": details,
			"error":   "VALIDATION_ERROR",
			"message": "Некорректные параметры доставки",
			"status":  http.StatusBadRequest,
		})
		return
	}

	mb.mu.Lock()
	defer mb.mu.Unlock()

	mb.orderSeq++
	orderID := uuid.New().String()
	order := &client.OrderResponse{
		OrderID:       orderID,
		OrderNumber:   2000 + mb.orderSeq,
		Status:        "new",
		CourierStatus: "new",
		ClientID:      mb.clientIDFromRequest(r),
		AddressA:      fmt.Sprintf("%.5f,%.5f", req.FromLat, req.FromLong),
		AddressB:      fmt.Sprintf("%.5f,%.5f", req.ToLat, req.ToLong),
		Price:         150,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	mb.orders[orderID] = order

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleClientOrderOperations(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /api/clients/orders/{order_id}/cancel
	if len(parts) >= 5 && parts[4] == "cancel" {
		orderID := parts[3]
		mb.mu.Lock()
		defer mb.mu.Unlock()

		order, exists := mb.orders[orderID]
		if !exists {
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
			return
		}

		// A client may cancel only while the order is still new. The refusal is
		// a state conflict (409 CANCEL_NOT_ALLOWED), not an authorization one:
		// the client owns the order, its status is what forbids cancelling.
		if order.Status != "new" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "CANCEL_NOT_ALLOWED",
				"message": "Отмена заказа недоступна на текущем статусе",
			})
			return
		}

		order.Status = "cancelled"
		order.UpdatedAt = time.Now()

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "order cancelled"})
		return
	}

	http.NotFound(w, r)
}

func (mb *MockBackend) handleAdminAssignCourier(w http.ResponseWriter, r *http.Request) {
	var req client.AssignCourierRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	mb.mu.Lock()
	defer mb.mu.Unlock()

	order, exists := mb.orders[req.OrderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	if order.Status == "cancelled" || order.Status == "complete" {
		http.Error(w, `{"error":"bad_request","message":"Cannot assign courier to completed or cancelled order"}`, http.StatusBadRequest)
		return
	}

	order.CourierID = req.CourierID
	order.CourierName = fmt.Sprintf("Courier %s", req.CourierID)
	order.CourierStatus = "new"
	order.UpdatedAt = time.Now()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleAdminCancelOrder(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	_ = json.NewDecoder(r.Body).Decode(&req)
	orderID := req["order_id"]

	mb.mu.Lock()
	defer mb.mu.Unlock()

	order, exists := mb.orders[orderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	order.Status = "cancelled"
	order.CourierStatus = "cancelled"
	order.UpdatedAt = time.Now()

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "order cancelled by admin"})
}

func (mb *MockBackend) handleRestOrderStatus(w http.ResponseWriter, r *http.Request) {
	var req client.ChangeRestOrderStatusRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	mb.mu.Lock()
	defer mb.mu.Unlock()

	order, exists := mb.orders[req.OrderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	if order.Status == "complete" || order.Status == "cancelled" {
		http.Error(w, `{"error":"bad_request","message":"Cannot change status of terminal order"}`, http.StatusBadRequest)
		return
	}

	order.Status = req.Status
	order.UpdatedAt = time.Now()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleCourierTakeOrder(w http.ResponseWriter, r *http.Request) {
	var req client.CourierTakeOrderRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	mb.mu.Lock()
	defer mb.mu.Unlock()

	order, exists := mb.orders[req.OrderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	order.CourierID = req.CourierID
	order.CourierStatus = "going"
	order.UpdatedAt = time.Now()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleCourierChangeStatus(w http.ResponseWriter, r *http.Request) {
	var req client.CourierChangeStatusRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	mb.mu.Lock()
	defer mb.mu.Unlock()

	order, exists := mb.orders[req.OrderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	if order.Status == "complete" || order.Status == "cancelled" {
		http.Error(w, `{"error":"bad_request","message":"Cannot change status of terminal order"}`, http.StatusBadRequest)
		return
	}

	order.CourierStatus = req.Status
	if req.Status == "complete" {
		order.Status = "complete"
	} else if req.Status == "shipping" {
		order.Status = "shipping"
	}
	order.UpdatedAt = time.Now()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleClientGetOrder(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	orderID := parts[len(parts)-1]

	mb.mu.RLock()
	defer mb.mu.RUnlock()

	order, exists := mb.orders[orderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleAdminGetOrder(w http.ResponseWriter, r *http.Request) {
	orderID := r.URL.Query().Get("order_id")

	mb.mu.RLock()
	defer mb.mu.RUnlock()

	order, exists := mb.orders[orderID]
	if !exists {
		http.Error(w, `{"error":"order not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(order)
}

func (mb *MockBackend) handleAdminListClients(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (mb *MockBackend) handleRestDishes(w http.ResponseWriter, r *http.Request) {
	var req client.CreateDishRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Mirrors validators/dish: title, price and category_id are required, and
	// the owning restaurant comes from the token — never from the body.
	if strings.TrimSpace(req.Title) == "" || req.Price < 0 || strings.TrimSpace(req.CategoryID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "VALIDATION_ERROR",
			"message": "Ошибка валидации",
			"status":  http.StatusBadRequest,
		})
		return
	}

	mb.mu.Lock()
	defer mb.mu.Unlock()

	mb.dishSeq++
	dish := &client.DishResponse{
		DishID: uuid.New().String(),
		Title:  req.Title,
		Price:  req.Price,
		RestID: mb.restIDFromRequest(r),
		OnSell: true,
		Categories: []client.CategoryResponse{
			{CategoryID: req.CategoryID, Title: "Категория", OnSell: true},
		},
	}
	mb.dishes[mb.dishSeq] = dish

	writeJSON(w, http.StatusOK, dish)
}

// handleClientAddresses saves a delivery address and hands back its id, which
// is what a delivery order refers to.
func (mb *MockBackend) handleClientAddresses(w http.ResponseWriter, r *http.Request) {
	var req client.CreateAddressRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	full := strings.TrimSpace(req.FullAddress)
	if len(full) < 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "fullAddress is required"})
		return
	}

	clientID := mb.clientIDFromRequest(r)
	addressID := uuid.New().String()

	mb.mu.Lock()
	mb.addresses[addressID] = clientID
	mb.mu.Unlock()

	resp := client.AddressResponse{
		AddressID:   addressID,
		ClientID:    clientID,
		FullAddress: full,
		IsDefault:   true,
	}
	if req.Latitude != nil {
		resp.Latitude = *req.Latitude
	}
	if req.Longitude != nil {
		resp.Longitude = *req.Longitude
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCartItems adds a dish to the caller's cart.
func (mb *MockBackend) handleCartItems(w http.ResponseWriter, r *http.Request) {
	var req client.AddCartItemRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if strings.TrimSpace(req.EntityID) == "" || strings.TrimSpace(req.EntityType) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "MISSING_FIELDS", "message": "entity_id и entity_type обязательны",
		})
		return
	}
	if req.Quantity <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "INVALID_QUANTITY", "message": "quantity должен быть больше 0",
		})
		return
	}

	mb.mu.Lock()
	defer mb.mu.Unlock()

	price := 0.0
	for _, d := range mb.dishes {
		if d.DishID == req.EntityID {
			price = d.Price
			break
		}
	}
	if price == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "ENTITY_NOT_FOUND", "message": "Блюдо не найдено",
		})
		return
	}

	clientID := mb.clientIDFromRequest(r)
	item := cartItem{
		CartItemID: uuid.New().String(),
		EntityID:   req.EntityID,
		EntityType: req.EntityType,
		Quantity:   req.Quantity,
		Price:      price,
	}
	mb.cart[clientID] = append(mb.cart[clientID], item)

	var out client.AddCartItemResponse
	out.Message = "success"
	out.Item.CartItemID = item.CartItemID
	out.Item.EntityID = item.EntityID
	out.Item.EntityType = item.EntityType
	out.Item.ItemPrice = item.Price
	out.Item.Quantity = item.Quantity
	writeJSON(w, http.StatusCreated, out)
}

// handleCartGroup clears one restaurant group of the caller's cart.
func (mb *MockBackend) handleCartGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusOK, map[string]string{"message": "success"})
		return
	}
	mb.mu.Lock()
	delete(mb.cart, mb.clientIDFromRequest(r))
	mb.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"message": "success"})
}

// handleRestCategories creates a menu category. The real endpoint answers with
// an array because it accepts batches — the mock keeps that shape.
func (mb *MockBackend) handleRestCategories(w http.ResponseWriter, r *http.Request) {
	var req client.CreateCategoryRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if strings.TrimSpace(req.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": "VALIDATION_ERROR", "message": "Невалидные данные", "status": http.StatusBadRequest,
		})
		return
	}

	writeJSON(w, http.StatusCreated, []client.CategoryResponse{{
		CategoryID: uuid.New().String(),
		RestID:     mb.restIDFromRequest(r),
		Title:      req.Title,
		OnSell:     true,
	}})
}

// handleRestSchedule stores working hours. The validator wants exactly seven
// days, and a restaurant without a schedule counts as closed.
func (mb *MockBackend) handleRestSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusOK, map[string]interface{}{"schedule": []client.ScheduleDay{}})
		return
	}

	var req client.UpdateScheduleRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if len(req.Days) != 7 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"errors":  []map[string]string{{"field": "days", "message": "Необходимо указать ровно 7 дней"}},
			"message": "Ошибка валидации расписания",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"schedule": req.Days})
}

// handleClientGetFinalOrder reads an order regardless of whether it is still
// active. The real endpoint runs the row through toCamelCase, so the mock
// answers in camelCase too — that is what keeps the engine's dual-naming
// decoding honest instead of accidentally correct.
func (mb *MockBackend) handleClientGetFinalOrder(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	orderID := parts[len(parts)-1]

	mb.mu.RLock()
	order, exists := mb.orders[orderID]
	mb.mu.RUnlock()

	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "ORDER_NOT_FOUND", "message": "Заказ не найден",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"orderId":       order.OrderID,
		"orderNumber":   order.OrderNumber,
		"status":        order.Status,
		"courierStatus": order.CourierStatus,
		"clientId":      order.ClientID,
		"restId":        order.RestID,
		"courierId":     order.CourierID,
		"price":         order.Price,
	})
}

// handleCourierTypes reports the parcel working window. The mock is always
// open: a suite must not depend on the wall clock of the machine running it.
func (mb *MockBackend) handleCourierTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []client.CourierTypeAvailability{
		{IsAvailable: true, StartTime: "10:00", EndTime: "23:00"},
	})
}
