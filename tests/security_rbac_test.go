package tests

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/client"
)

func Test_Security_RBAC_And_Token_Isolation(t *testing.T) {
	ctx := context.Background()

	t.Run("Unauthenticated request with missing token returns 401", func(t *testing.T) {
		req := client.CreateRestaurantOrderRequest{
			RestID:        "rest_123",
			ReceiveMethod: client.ReceiveDelivery,
			Payment:       client.OrderPayment{Type: client.PaymentCash},
		}
		// Send request without Bearer token
		_, err := testEngine.HTTPClient.Request(ctx, "POST", "/api/clients/create-order", "", req, nil)
		require.Error(t, err)
		require.True(t, client.IsUnauthorized(err), "ожидался 401, получено: %v", err)

		apiErr, ok := client.AsAPIError(err)
		require.True(t, ok)
		require.Equal(t, "NO_TOKEN_INCLUDED", apiErr.Code,
			"authorization.js обязан различать «токена нет» и «токен битый»")
		t.Logf("    ✓ Correctly rejected unauthenticated request: %v", err)
	})

	t.Run("Client token cannot access Director/Admin APIs (403 Forbidden)", func(t *testing.T) {
		clientPhone, clientToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, clientToken)

		// Client attempts to call admin give-order-to-courier
		assignReq := client.AssignCourierRequest{
			OrderID:   "order-test-id",
			CourierID: "courier-123",
		}
		_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/admin/give-order-to-courier", clientToken, assignReq, nil)
		require.Error(t, err)
		require.True(t, client.IsForbidden(err), "ожидался 403, получено: %v", err)
		requireErrorCode(t, err, "ADMIN_ONLY")
		t.Logf("    ✓ Correctly blocked client (%s) from accessing Admin API: %v", clientPhone, err)
	})

	t.Run("Courier token cannot access Restaurant Dish Management APIs (403 Forbidden)", func(t *testing.T) {
		_, courierToken, err := testEngine.Fixtures.CreateUniqueCourier(ctx)
		require.NoError(t, err)

		dishReq := client.CreateDishRequest{
			Title:      "Пицца Несанкционированная",
			Price:      990,
			CategoryID: "00000000-0000-0000-0000-000000000000",
		}
		_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/rests/dishes", courierToken, dishReq, nil)
		require.Error(t, err)
		require.True(t, client.IsForbidden(err), "ожидался 403, получено: %v", err)
		t.Logf("    ✓ Correctly blocked courier from creating restaurant dishes: %v", err)
	})
}

// Test_Security_Auth_Gates covers the gates the backend gained around the
// authorization middleware: a malformed token must be told apart from a
// missing one, and each role area must reject the other roles' tokens rather
// than letting them reach business logic (the IDOR class LOCALIAPP-2383 fixed).
func Test_Security_Auth_Gates(t *testing.T) {
	ctx := context.Background()

	t.Run("Malformed token is rejected as INVALID_TOKEN, not as a missing token", func(t *testing.T) {
		_, err := testEngine.HTTPClient.Request(ctx, "POST", "/api/couriers/take-order", "definitely.not.a.jwt",
			client.CourierTakeOrderRequest{OrderID: "order-test-id"}, nil)
		require.Error(t, err)
		require.True(t, client.IsUnauthorized(err), "ожидался 401, получено: %v", err)
		requireErrorCode(t, err, "INVALID_TOKEN")
	})

	t.Run("Client token cannot reach the courier area", func(t *testing.T) {
		_, clientToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
		require.NoError(t, err)

		// Before the courierOnly gate a client token reached take-order's
		// business logic — that is the IDOR this asserts is closed.
		_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/couriers/take-order", clientToken,
			client.CourierTakeOrderRequest{OrderID: "order-test-id"}, nil)
		require.Error(t, err)
		require.True(t, client.IsForbidden(err), "ожидался 403, получено: %v", err)
		requireErrorCode(t, err, "COURIER_ONLY")
	})

	t.Run("Restaurant token cannot reach the courier area", func(t *testing.T) {
		_, restToken, err := testEngine.Fixtures.CreateUniqueRestaurant(ctx)
		require.NoError(t, err)

		_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/couriers/take-order", restToken,
			client.CourierTakeOrderRequest{OrderID: "order-test-id"}, nil)
		require.Error(t, err)
		require.True(t, client.IsForbidden(err), "ожидался 403, получено: %v", err)
		requireErrorCode(t, err, "COURIER_ONLY")
	})

	t.Run("Admin token is accepted by the courier area", func(t *testing.T) {
		adminToken, err := testEngine.SessionMgr.LoginAdmin(ctx,
			testEngine.Config.AdminLogin, testEngine.Config.AdminPassword)
		require.NoError(t, err)

		// courierOnly deliberately lets admin/service tokens through: the
		// distribution service calls take-order with courier_id in the body.
		// Whatever the answer is, it must not be the role gate.
		_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/couriers/take-order", adminToken,
			client.CourierTakeOrderRequest{OrderID: "order-test-id", CourierID: "courier-123"}, nil)
		if err != nil {
			require.False(t, client.IsForbidden(err),
				"админский токен не должен блокироваться гейтом courierOnly: %v", err)
		}
	})

	t.Run("Courier token cannot reach the admin area", func(t *testing.T) {
		_, courierToken, err := testEngine.Fixtures.CreateUniqueCourier(ctx)
		require.NoError(t, err)

		_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/admin/give-order-to-courier", courierToken,
			client.AssignCourierRequest{OrderID: "order-test-id"}, nil)
		require.Error(t, err)
		require.True(t, client.IsForbidden(err), "ожидался 403, получено: %v", err)
		requireErrorCode(t, err, "ADMIN_ONLY")
	})

	t.Run("Public auth endpoints stay reachable without a token", func(t *testing.T) {
		// register/login sit behind the same middleware as everything else and
		// are only public by rule — a broken PUBLIC_RULES list locks every
		// client out of the app.
		_, err := testEngine.HTTPClient.Request(ctx, "POST", "/api/clients/register", "",
			client.ClientRegisterRequest{PhoneNumber: uniquePhone()}, nil)
		require.NoError(t, err, "POST /api/clients/register обязан быть публичным")
	})
}

// requireErrorCode asserts on the machine-readable code of the error envelope.
// A stand that answers with the right status but a different code has changed
// its contract, and the mock has drifted from it.
func requireErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	apiErr, ok := client.AsAPIError(err)
	require.True(t, ok, "ожидалась типизированная ошибка API, получено: %v", err)
	require.Equal(t, want, apiErr.Code, "код ошибки в теле ответа (%d): %v", apiErr.StatusCode, err)
}
