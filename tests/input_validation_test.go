package tests

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/client"
)

func Test_Input_Validation_And_Boundary_Conditions(t *testing.T) {
	ctx := context.Background()

	t.Run("Reject non-Russian phone format in client registration", func(t *testing.T) {
		regReq := client.ClientRegisterRequest{
			PhoneNumber: "+12025550199", // US format
		}
		_, err := testEngine.ClientAPI.Register(ctx, regReq)
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "expected 400, got: %v", err)
		t.Logf("    ✓ Correctly rejected invalid non-RU phone format: %v", err)
	})

	t.Run("Reject login with empty verification code", func(t *testing.T) {
		loginReq := client.ClientLoginRequest{
			PhoneNumber:      "+79991234567",
			VerificationCode: "", // Empty
		}
		_, err := testEngine.ClientAPI.Login(ctx, loginReq)
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "expected 400, got: %v", err)
		t.Logf("    ✓ Correctly rejected login without verification code: %v", err)
	})

	t.Run("Reject parcel order without recipient and cargo type", func(t *testing.T) {
		_, clientToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
		require.NoError(t, err)
		testEngine.SessionMgr.AddToken("client", "Validation", "", clientToken, true)

		// Coordinates only: the backend also wants who receives the parcel and
		// what is inside it.
		bare := client.CreateIndependentOrderRequest{
			FromLat: 55.7649, FromLong: 37.6055,
			ToLat: 55.7749, ToLong: 37.6155,
			PaymentType: client.PaymentCash,
		}
		_, err = testEngine.ClientAPI.CreateIndependentOrder(ctx, bare)
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "expected 400, got: %v", err)
		requireRejectedFields(t, err, "recipientName", "recipientPhone", "cargoType")
	})

	t.Run("Reject parcel order with coordinates out of range", func(t *testing.T) {
		_, clientToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
		require.NoError(t, err)
		testEngine.SessionMgr.AddToken("client", "Validation", "", clientToken, true)

		req := testEngine.Fixtures.ParcelRequest("")
		req.FromLat = 120 // вне -90..90
		_, err = testEngine.ClientAPI.CreateIndependentOrder(ctx, req)
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "expected 400, got: %v", err)
		requireRejectedFields(t, err, "fromLat")
	})

	t.Run("Reject restaurant order without a receive method", func(t *testing.T) {
		_, clientToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
		require.NoError(t, err)
		testEngine.SessionMgr.AddToken("client", "Validation", "", clientToken, true)

		noMethod := client.CreateRestaurantOrderRequest{
			RestID:  "rest_123",
			Payment: client.OrderPayment{Type: client.PaymentCash},
		}
		_, err = testEngine.ClientAPI.CreateRestaurantOrder(ctx, noMethod)
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "expected 400, got: %v", err)
		requireRejectedFields(t, err, "receiveMethod")
	})

	t.Run("Reject restaurant order with an unknown source", func(t *testing.T) {
		_, clientToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
		require.NoError(t, err)
		testEngine.SessionMgr.AddToken("client", "Validation", "", clientToken, true)

		badSource := client.CreateRestaurantOrderRequest{
			RestID:        "rest_123",
			ReceiveMethod: client.ReceiveDelivery,
			Payment:       client.OrderPayment{Type: client.PaymentCash},
			Source:        "telepathy",
		}
		_, err = testEngine.ClientAPI.CreateRestaurantOrder(ctx, badSource)
		require.Error(t, err)
		require.True(t, client.IsStatus(err, http.StatusBadRequest), "expected 400, got: %v", err)
		requireRejectedFields(t, err, "source")
	})
}

// requireRejectedFields asserts that the backend named each of these fields in
// the `details` array of its validation error. Asserting on the field names
// rather than the message keeps the test stable across rewordings — and proves
// the engine surfaces which field was wrong, not just that something was.
func requireRejectedFields(t *testing.T, err error, fields ...string) {
	t.Helper()
	apiErr, ok := client.AsAPIError(err)
	require.True(t, ok, "ожидалась типизированная ошибка API, получено: %v", err)
	require.NotEmpty(t, apiErr.Details, "бэкенд обязан перечислять поля в details: %v", err)

	reported := make(map[string]bool, len(apiErr.Details))
	for _, d := range apiErr.Details {
		reported[d.Field] = true
	}
	for _, f := range fields {
		require.True(t, reported[f], "поле %q не названо в details (%v)", f, apiErr.Details)
	}
}
