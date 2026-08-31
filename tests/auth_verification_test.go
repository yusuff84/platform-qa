package tests

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/fixtures"
)

// These cover the authentication contract the backend enforces around the
// one-time code: who may request one, how often, how long it lives, and how
// many guesses it survives. Everything asserts on the typed status so a
// reworded Russian message never turns into a false failure.

func Test_Client_Verification_Flow(t *testing.T) {
	ctx := context.Background()

	t.Run("register returns debugCode on a DEBUG stand", func(t *testing.T) {
		phone := uniquePhone()
		resp, err := testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{
			PhoneNumber: phone,
			CityKey:     "Москва",
		})
		require.NoError(t, err)
		require.NotEmpty(t, resp.DebugCode,
			"стенд должен быть запущен с DEBUG=true — иначе автотесты не получат код")
		require.True(t, resp.FirstTime, "первый запрос кода на новый номер — firstTime")
	})

	t.Run("debugCode authenticates and yields a client token", func(t *testing.T) {
		phone := uniquePhone()
		resp, err := testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: phone})
		require.NoError(t, err)

		token, err := testEngine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      phone,
			VerificationCode: resp.DebugCode,
			FirstName:        "Иван",
			LastName:         "Тестов",
		})
		require.NoError(t, err)
		require.NoError(t, client.VerifyRole(token, "client"),
			"логин клиента обязан выдавать токен без ролевых флагов")
	})

	t.Run("login is rejected for a phone that never requested a code", func(t *testing.T) {
		_, err := testEngine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      uniquePhone(),
			VerificationCode: "1234",
		})
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusNotFound), "ожидался 404, получено: %v", err)
	})

	t.Run("wrong code is refused and the attempt budget runs out with 429", func(t *testing.T) {
		phone := uniquePhone()
		resp, err := testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: phone})
		require.NoError(t, err)

		wrong := "0000"
		if resp.DebugCode == wrong {
			wrong = "1111"
		}

		// Five wrong guesses are answered 400; the sixth is locked out.
		for i := 1; i <= 5; i++ {
			_, err := testEngine.ClientAPI.Login(ctx, client.ClientLoginRequest{
				PhoneNumber:      phone,
				VerificationCode: wrong,
			})
			require.Error(t, err)
			require.True(t, client.IsStatus(err, http.StatusBadRequest),
				"попытка %d: ожидался 400, получено: %v", i, err)
		}

		_, err = testEngine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      phone,
			VerificationCode: wrong,
		})
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusTooManyRequests),
			"после 5 неверных попыток ожидался 429, получено: %v", err)
	})

	t.Run("a spent code cannot be replayed", func(t *testing.T) {
		phone := uniquePhone()
		resp, err := testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: phone})
		require.NoError(t, err)

		_, err = testEngine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      phone,
			VerificationCode: resp.DebugCode,
			FirstName:        "Иван",
			LastName:         "Тестов",
		})
		require.NoError(t, err)

		_, err = testEngine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      phone,
			VerificationCode: resp.DebugCode,
		})
		require.Error(t, err, "повторное использование израсходованного кода должно отклоняться")
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "ожидался 400, получено: %v", err)
	})

	t.Run("resend inside the cooldown is rate limited with a retry hint", func(t *testing.T) {
		phone := uniquePhone()
		_, err := testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: phone})
		require.NoError(t, err)

		_, err = testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{PhoneNumber: phone})
		require.Error(t, err)
		require.True(t, client.IsRateLimited(err), "ожидался 429, получено: %v", err)
		require.Positive(t, client.RetryAfter(err),
			"429 обязан сообщать, через сколько можно повторить: %v", err)
	})

	t.Run("a non-RF number never reaches the OTP machinery", func(t *testing.T) {
		_, err := testEngine.ClientAPI.Register(ctx, client.ClientRegisterRequest{
			PhoneNumber: "+12025550199",
		})
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "ожидался 400, получено: %v", err)
	})
}

func Test_Role_Login_Issues_Correct_Claims(t *testing.T) {
	ctx := context.Background()

	// A login handler that keeps answering 200 while signing the wrong claim
	// set would silently disable every RBAC assertion downstream.
	t.Run("courier login carries isCourier", func(t *testing.T) {
		_, token, err := testEngine.Fixtures.CreateUniqueCourier(ctx)
		require.NoError(t, err)
		require.NoError(t, client.VerifyRole(token, "courier"))
	})

	t.Run("restaurant login carries isRestaurant", func(t *testing.T) {
		_, token, err := testEngine.Fixtures.CreateUniqueRestaurant(ctx)
		require.NoError(t, err)
		require.NoError(t, client.VerifyRole(token, "rest"))
	})

	t.Run("admin login carries isAdmin", func(t *testing.T) {
		token, err := testEngine.SessionMgr.LoginAdmin(ctx,
			testEngine.Config.AdminLogin, testEngine.Config.AdminPassword)
		require.NoError(t, err)
		require.NoError(t, client.VerifyRole(token, "admin"))

		claims, err := client.DecodeClaims(token)
		require.NoError(t, err)
		require.NotEmpty(t, claims.Rights, "админский токен обязан нести список прав")
	})
}

func Test_Restaurant_Password_Reset_Flow(t *testing.T) {
	ctx := context.Background()

	t.Run("reset is refused for an unknown phone", func(t *testing.T) {
		err := testEngine.RestAPI.RequestPasswordReset(ctx, uniquePhone())
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusNotFound), "ожидался 404, получено: %v", err)
	})

	t.Run("verify without a requested code is refused", func(t *testing.T) {
		_, err := testEngine.RestAPI.VerifyPasswordResetCode(ctx, uniquePhone(), "1234")
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusNotFound), "ожидался 404, получено: %v", err)
	})

	t.Run("confirm requires a reset token", func(t *testing.T) {
		err := testEngine.RestAPI.ConfirmPasswordReset(ctx, "", "NewPass123")
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "ожидался 400, получено: %v", err)
	})

	t.Run("confirm rejects a forged reset token", func(t *testing.T) {
		err := testEngine.RestAPI.ConfirmPasswordReset(ctx, "not.a.token", "NewPass123")
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusUnauthorized), "ожидался 401, получено: %v", err)
	})

	t.Run("confirm enforces the password policy", func(t *testing.T) {
		err := testEngine.RestAPI.ConfirmPasswordReset(ctx, "not.a.token", "weak")
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest),
			"слабый пароль отклоняется до проверки токена: %v", err)
	})
}

func Test_Client_Phone_Change_Guards(t *testing.T) {
	ctx := context.Background()

	_, token, err := testEngine.Fixtures.CreateUniqueClient(ctx)
	require.NoError(t, err)
	testEngine.SessionMgr.AddToken("client", "Phone change", "", token, true)

	t.Run("verify-old without a code is refused", func(t *testing.T) {
		err := testEngine.ClientAPI.VerifyOldPhoneCode(ctx, "")
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "ожидался 400, получено: %v", err)
	})

	t.Run("verify-old without an active session is refused", func(t *testing.T) {
		err := testEngine.ClientAPI.VerifyOldPhoneCode(ctx, "1234")
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "ожидался 400, получено: %v", err)
	})

	t.Run("cancel is idempotent", func(t *testing.T) {
		require.NoError(t, testEngine.ClientAPI.CancelPhoneChange(ctx))
		require.NoError(t, testEngine.ClientAPI.CancelPhoneChange(ctx),
			"отмена без активной сессии тоже должна проходить")
	})
}

// uniquePhone returns a fresh RF number that no other test in this package has
// used, so the per-phone OTP state never leaks between subtests.
func uniquePhone() string {
	return fixtures.NewClientPhone()
}
