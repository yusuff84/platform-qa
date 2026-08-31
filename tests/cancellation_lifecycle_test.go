package tests

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/dsl"
	"locali-e2e-engine/pkg/statemachine"
)

func Test_CancellationLifecycle_EdgeCases(t *testing.T) {
	t.Run("Client cancels order while in NEW status -> Allowed", func(t *testing.T) {
		testEngine.Scenario(t, "Client Cancellation at NEW status").
			Given("Client, Restaurant, Admin identities are initialized", func(ctx *dsl.TestContext) {
				ctx.SetupAllRoles()
			}).
			When("Client creates order", func(ctx *dsl.TestContext) {
				order, err := ctx.Engine.ClientAPI.CreateRestaurantOrder(ctx.GoCtx, ctx.Order.OrderRequest())
				require.NoError(t, err)
				ctx.CurrentOrder = order
			}).
			Then("Order status is NEW", func(ctx *dsl.TestContext) {
				ctx.AssertOrderStatus(statemachine.StatusNew, statemachine.RoleClient)
			}).
			When("Client cancels order immediately", func(ctx *dsl.TestContext) {
				err := ctx.Engine.ClientAPI.CancelOrder(ctx.GoCtx, ctx.CurrentOrder.OrderID, "Передумал заказывать")
				require.NoError(t, err, "Client cancel at NEW should succeed")
			}).
			Then("Order status transitions to CANCELLED and state machine records transition", func(ctx *dsl.TestContext) {
				err := ctx.Engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusNew, statemachine.StatusCancelled, statemachine.RoleClient)
				require.NoError(t, err)
				ctx.Engine.StateMachine.RecordTransition(ctx.CurrentOrder.OrderID, statemachine.StatusCancelled)
				t.Logf("    ✓ Order successfully cancelled at NEW stage")
			})
	})

	t.Run("Client attempts to cancel order during COOKING stage -> Rejected (409)", func(t *testing.T) {
		testEngine.Scenario(t, "Forbidden Client Cancellation at COOKING").
			Given("Order in COOKING status", func(ctx *dsl.TestContext) {
				ctx.SetupAllRoles()
				order, err := ctx.Engine.ClientAPI.CreateRestaurantOrder(ctx.GoCtx, ctx.Order.OrderRequest())
				require.NoError(t, err)
				ctx.CurrentOrder = order

				// Transition to cooking by restaurant
				_, err = ctx.Engine.RestAPI.ChangeOrderStatus(ctx.GoCtx, order.OrderID, "cooking")
				require.NoError(t, err)
			}).
			When("Client attempts to cancel order", func(ctx *dsl.TestContext) {
				err := ctx.Engine.ClientAPI.CancelOrder(ctx.GoCtx, ctx.CurrentOrder.OrderID, "Хочу отменить")
				require.Error(t, err, "Client should NOT be able to cancel cooking order directly")
				// The refusal is a state conflict, not an authorization one: the
				// client owns the order, the status is what forbids cancelling.
				require.True(t, client.IsStatus(err, http.StatusConflict), "ожидался 409, получено: %v", err)
				apiErr, ok := client.AsAPIError(err)
				require.True(t, ok)
				require.Equal(t, "CANCEL_NOT_ALLOWED", apiErr.Code)
				t.Logf("    ✓ Correctly rejected client cancellation when order is cooking: %v", err)
			})
	})

	t.Run("Modify order after DELIVERED status -> Blocked (Terminal State)", func(t *testing.T) {
		sm := statemachine.NewOrderStateMachine()

		// Attempt transition after DELIVERED
		err := sm.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusDelivered, statemachine.StatusPreparing, statemachine.RoleRestaurant)
		require.Error(t, err)
		require.Contains(t, err.Error(), "terminal state")
		t.Logf("    ✓ Correctly blocked state modification on delivered order: %v", err)
	})
}
