package runner

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/fixtures"
)

// runAuthOTP exercises the authentication contract end to end: how a one-time
// code is issued, what it costs to guess it wrong, which claims each role's
// token carries, and which doors stay shut for the wrong token.
//
// Unlike the flow suites this one asserts on HTTP statuses and the machine
// codes in the error envelope, never on message text — the backend's messages
// are Russian prose and get reworded.
func (o *TestOrchestrator) runAuthOTP(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "Auth: вход, авторизация и верификация"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Проверяем выдачу кода, лимиты OTP, ролевые claims токенов и гейты доступа...",
		Timestamp: time.Now(),
	})

	say := func(stepType string, level LogLevel, format string, args ...interface{}) {
		o.Emit(&ExecutionEvent{
			RunID:     run.ID,
			SuiteName: suiteName,
			SuiteKey:  suiteKey,
			StepType:  stepType,
			Level:     level,
			Message:   fmt.Sprintf(format, args...),
			Timestamp: time.Now(),
		})
	}

	// 1. The code is issued at all, and (on a DEBUG stand) is readable from
	//    the response — this is what makes the whole suite unattended.
	issueStart := time.Now()
	o.checkStart(run, suiteKey, "otp_issued")
	phone := fixtures.NewClientPhone()
	say("GIVEN", LogInfo, "Запрашиваем код верификации для нового номера %s", phone)

	reg, err := o.engine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: phone})
	if err != nil {
		msg := fmt.Sprintf("register не выдал код: %v", err)
		o.checkDone(run, suiteKey, "otp_issued", false, msg, issueStart)
		return fmt.Errorf("%s", msg)
	}
	if reg.DebugCode == "" {
		msg := "в ответе register нет debugCode — стенд запущен без DEBUG=true, автоматический вход невозможен"
		o.checkDone(run, suiteKey, "otp_issued", false, msg, issueStart)
		return fmt.Errorf("%s", msg)
	}
	o.checkDone(run, suiteKey, "otp_issued", true, "Код выдан и доступен в ответе register (debugCode)", issueStart)

	// 2. The code authenticates, and the token it yields is a client token.
	loginStart := time.Now()
	o.checkStart(run, suiteKey, "otp_login")
	clientToken, err := o.engine.ClientAPI.Login(ctx, client.ClientLoginRequest{
		PhoneNumber:      phone,
		VerificationCode: reg.DebugCode,
		FirstName:        "Авто",
		LastName:         "Тестов",
	})
	if err != nil {
		msg := fmt.Sprintf("вход по выданному коду не прошёл: %v", err)
		o.checkDone(run, suiteKey, "otp_login", false, msg, loginStart)
		return fmt.Errorf("%s", msg)
	}
	if verr := client.VerifyRole(clientToken, "client"); verr != nil {
		msg := fmt.Sprintf("токен клиента несёт чужие claims: %v", verr)
		o.checkDone(run, suiteKey, "otp_login", false, msg, loginStart)
		return fmt.Errorf("%s", msg)
	}
	say("THEN", LogSuccess, "✓ Вход выполнен, токен клиента не несёт ролевых флагов")
	o.checkDone(run, suiteKey, "otp_login", true, "Вход по коду выдал корректный токен клиента", loginStart)

	// 3. A spent code must not authenticate a second time.
	replayStart := time.Now()
	o.checkStart(run, suiteKey, "otp_replay")
	_, err = o.engine.ClientAPI.Login(ctx, client.ClientLoginRequest{
		PhoneNumber:      phone,
		VerificationCode: reg.DebugCode,
	})
	if err == nil {
		msg := "израсходованный код авторизовал повторно — код не сбрасывается после успешного входа"
		o.checkDone(run, suiteKey, "otp_replay", false, msg, replayStart)
		return fmt.Errorf("%s", msg)
	}
	say("THEN", LogSuccess, "✓ Повторный вход по тому же коду отклонён (%d)", client.StatusOf(err))
	o.checkDone(run, suiteKey, "otp_replay", true, "Израсходованный код повторно не принимается", replayStart)

	// 4. The attempt budget: five wrong guesses, then the phone is locked out.
	attemptsStart := time.Now()
	o.checkStart(run, suiteKey, "otp_attempts")
	victim := fixtures.NewClientPhone()
	victimReg, err := o.engine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: victim})
	if err != nil {
		msg := fmt.Sprintf("не удалось запросить код для проверки лимита попыток: %v", err)
		o.checkDone(run, suiteKey, "otp_attempts", false, msg, attemptsStart)
		return fmt.Errorf("%s", msg)
	}
	wrong := "0000"
	if victimReg.DebugCode == wrong {
		wrong = "1111"
	}

	var lastErr error
	for i := 1; i <= 6; i++ {
		_, lastErr = o.engine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      victim,
			VerificationCode: wrong,
		})
		if lastErr == nil {
			msg := fmt.Sprintf("неверный код был принят на попытке %d", i)
			o.checkDone(run, suiteKey, "otp_attempts", false, msg, attemptsStart)
			return fmt.Errorf("%s", msg)
		}
		if client.IsRateLimited(lastErr) {
			break
		}
		if !client.IsStatus(lastErr, http.StatusBadRequest) {
			msg := fmt.Sprintf("попытка %d: ожидался 400, получено: %v", i, lastErr)
			o.checkDone(run, suiteKey, "otp_attempts", false, msg, attemptsStart)
			return fmt.Errorf("%s", msg)
		}
	}
	if !client.IsRateLimited(lastErr) {
		msg := fmt.Sprintf("бюджет попыток не исчерпался за 6 неверных вводов, последний ответ: %v", lastErr)
		o.checkDone(run, suiteKey, "otp_attempts", false, msg, attemptsStart)
		return fmt.Errorf("%s", msg)
	}
	say("THEN", LogSuccess, "✓ После серии неверных кодов номер заблокирован (429)")
	o.checkDone(run, suiteKey, "otp_attempts", true, "Бюджет попыток ограничен, перебор упирается в 429", attemptsStart)

	// 5. Resend cooldown: a second code for the same phone inside the window
	//    is refused, and the refusal says when to come back.
	resendStart := time.Now()
	o.checkStart(run, suiteKey, "otp_resend_limit")
	_, err = o.engine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: victim})
	if err == nil {
		msg := "повторный код на тот же номер выдан сразу — лимит отправки не работает"
		o.checkDone(run, suiteKey, "otp_resend_limit", false, msg, resendStart)
		return fmt.Errorf("%s", msg)
	}
	if !client.IsRateLimited(err) {
		msg := fmt.Sprintf("ожидался 429 на повторную отправку, получено: %v", err)
		o.checkDone(run, suiteKey, "otp_resend_limit", false, msg, resendStart)
		return fmt.Errorf("%s", msg)
	}
	retryAfter := client.RetryAfter(err)
	if retryAfter <= 0 {
		msg := fmt.Sprintf("429 не сообщает, через сколько можно повторить: %v", err)
		o.checkDone(run, suiteKey, "otp_resend_limit", false, msg, resendStart)
		return fmt.Errorf("%s", msg)
	}
	say("THEN", LogSuccess, "✓ Повторная отправка отклонена, можно повторить через %d сек", retryAfter)
	o.checkDone(run, suiteKey, "otp_resend_limit", true,
		fmt.Sprintf("Повторная отправка внутри окна → 429 (retryAfter=%d)", retryAfter), resendStart)

	// 6. Each role's login must sign its own claim set.
	claimsStart := time.Now()
	o.checkStart(run, suiteKey, "role_claims")
	_, courierToken, err := o.engine.Fixtures.CreateUniqueCourier(ctx)
	if err != nil {
		msg := fmt.Sprintf("фикстура курьера: %v", err)
		o.checkDone(run, suiteKey, "role_claims", false, msg, claimsStart)
		return fmt.Errorf("%s", msg)
	}
	_, restToken, err := o.engine.Fixtures.CreateUniqueRestaurant(ctx)
	if err != nil {
		msg := fmt.Sprintf("фикстура ресторана: %v", err)
		o.checkDone(run, suiteKey, "role_claims", false, msg, claimsStart)
		return fmt.Errorf("%s", msg)
	}
	adminToken, err := o.engine.SessionMgr.LoginAdmin(ctx, o.engine.Config.AdminLogin, o.engine.Config.AdminPassword)
	if err != nil {
		msg := fmt.Sprintf("вход администратора: %v", err)
		o.checkDone(run, suiteKey, "role_claims", false, msg, claimsStart)
		return fmt.Errorf("%s", msg)
	}
	for role, token := range map[string]string{"courier": courierToken, "rest": restToken, "admin": adminToken} {
		if verr := client.VerifyRole(token, role); verr != nil {
			msg := fmt.Sprintf("роль %s: %v", role, verr)
			o.checkDone(run, suiteKey, "role_claims", false, msg, claimsStart)
			return fmt.Errorf("%s", msg)
		}
	}
	say("THEN", LogSuccess, "✓ Курьер, ресторан и админ получают собственные ролевые claims")
	o.checkDone(run, suiteKey, "role_claims", true, "Каждая роль получает токен со своими claims", claimsStart)

	// 7. The gates themselves: no token, broken token, wrong role.
	gatesStart := time.Now()
	o.checkStart(run, suiteKey, "auth_gates")
	type gate struct {
		name   string
		token  string
		path   string
		body   interface{}
		status int
		code   string
	}
	gates := []gate{
		{
			name:   "без токена",
			path:   "/api/couriers/take-order",
			body:   client.CourierTakeOrderRequest{OrderID: "order-test-id"},
			status: http.StatusUnauthorized,
			code:   "NO_TOKEN_INCLUDED",
		},
		{
			name:   "битый токен",
			token:  "definitely.not.a.jwt",
			path:   "/api/couriers/take-order",
			body:   client.CourierTakeOrderRequest{OrderID: "order-test-id"},
			status: http.StatusUnauthorized,
			code:   "INVALID_TOKEN",
		},
		{
			name:   "клиентский токен в курьерской зоне",
			token:  clientToken,
			path:   "/api/couriers/take-order",
			body:   client.CourierTakeOrderRequest{OrderID: "order-test-id"},
			status: http.StatusForbidden,
			code:   "COURIER_ONLY",
		},
		{
			name:   "клиентский токен в админской зоне",
			token:  clientToken,
			path:   "/api/admin/give-order-to-courier",
			body:   client.AssignCourierRequest{OrderID: "order-test-id"},
			status: http.StatusForbidden,
			code:   "ADMIN_ONLY",
		},
		{
			name:   "курьерский токен в админской зоне",
			token:  courierToken,
			path:   "/api/admin/give-order-to-courier",
			body:   client.AssignCourierRequest{OrderID: "order-test-id"},
			status: http.StatusForbidden,
			code:   "ADMIN_ONLY",
		},
	}

	for _, g := range gates {
		_, gerr := o.engine.HTTPClient.Request(ctx, "POST", g.path, g.token, g.body, nil)
		if gerr == nil {
			msg := fmt.Sprintf("%s: запрос к %s прошёл, ожидался %d", g.name, g.path, g.status)
			o.checkDone(run, suiteKey, "auth_gates", false, msg, gatesStart)
			return fmt.Errorf("%s", msg)
		}
		if !client.IsStatus(gerr, g.status) {
			msg := fmt.Sprintf("%s: ожидался %d, получено: %v", g.name, g.status, gerr)
			o.checkDone(run, suiteKey, "auth_gates", false, msg, gatesStart)
			return fmt.Errorf("%s", msg)
		}
		if apiErr, ok := client.AsAPIError(gerr); ok && apiErr.Code != g.code {
			// The status is right but the machine code drifted: the client
			// apps branch on it, so it is part of the contract.
			say("AND", LogWarn, "⚠ %s: статус %d верный, но код в теле «%s» вместо «%s»",
				g.name, g.status, apiErr.Code, g.code)
		}
		say("THEN", LogSuccess, "✓ %s → %d", g.name, g.status)
	}
	o.checkDone(run, suiteKey, "auth_gates", true,
		fmt.Sprintf("Все %d гейта доступа отработали ожидаемо", len(gates)), gatesStart)

	return nil
}
