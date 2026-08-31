package tests

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/dsl"
	"locali-e2e-engine/pkg/events"
	"locali-e2e-engine/pkg/statemachine"
)

func Test_FlowA_RestaurantOrderFullCycle(t *testing.T) {
	testEngine.Scenario(t, "Flow A: Full Restaurant Order Lifecycle from Creation to Delivery").
		Given("All 4 role identities (Client, Restaurant, Courier, Director) are isolated and authenticated", func(ctx *dsl.TestContext) {
			ctx.SetupAllRoles()
			ctx.OrderType = statemachine.OrderTypeRestaurant
		}).
		When("Step 1: Client creates an order from the restaurant", func(ctx *dsl.TestContext) {
			// The order body carries no line items: the backend assembles it
			// from the cart that SetupAllRoles filled.
			req := ctx.Order.OrderRequest()
			req.Comment = "Оставить у двери"

			order, err := ctx.Engine.ClientAPI.CreateRestaurantOrder(ctx.GoCtx, req)
			require.NoError(t, err, "Client order creation failed")
			require.NotEmpty(t, order.OrderID, "OrderID must be returned")

			ctx.CurrentOrder = order

			// Publish async event to listener for telemetry
			_ = ctx.Engine.Events.PublishEvent(&events.Event{
				Subject:   "order.status.new",
				OrderID:   order.OrderID,
				Status:    "new",
				Timestamp: time.Now(),
			})
		}).
		Then("Order status must transition to NEW and pass State Machine validation", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusNew, statemachine.RoleClient)
			require.Equal(t, "new", ctx.CurrentOrder.Status)
		}).
		When("Step 2: Director/Admin assigns an active Courier to the order", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.AdminAPI.AssignCourier(ctx.GoCtx, ctx.CurrentOrder.OrderID, ctx.CourierID)
			require.NoError(t, err, "Admin assigning courier failed")
			require.Equal(t, ctx.CourierID, updatedOrder.CourierID)

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Order status must transition to COURIER_ASSIGNED", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusCourierAssigned, statemachine.RoleAdmin)
			require.NotEmpty(t, ctx.CurrentOrder.CourierID)
		}).
		When("Step 3: Restaurant accepts the order and starts cooking (PREPARING)", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.RestAPI.ChangeOrderStatus(ctx.GoCtx, ctx.CurrentOrder.OrderID, "cooking")
			require.NoError(t, err, "Restaurant changing status to cooking failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Order status must transition to PREPARING", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusPreparing, statemachine.RoleRestaurant)
			require.Equal(t, "cooking", ctx.CurrentOrder.Status)
		}).
		When("Step 4: Restaurant marks the dishes as cooked and ready (READY_FOR_PICKUP)", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.RestAPI.ChangeOrderStatus(ctx.GoCtx, ctx.CurrentOrder.OrderID, "cooked")
			require.NoError(t, err, "Restaurant changing status to cooked failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Order status must transition to READY_FOR_PICKUP", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusReadyForPickup, statemachine.RoleRestaurant)
			require.Equal(t, "cooked", ctx.CurrentOrder.Status)
		}).
		When("Step 5: Courier arrives, takes the packed order and starts shipping (PICKED_UP)", func(ctx *dsl.TestContext) {
			err := ctx.Engine.CourierAPI.TakeOrder(ctx.GoCtx, ctx.CurrentOrder.OrderID, ctx.CourierID)
			require.NoError(t, err, "Courier take order failed")

			updatedOrder, err := ctx.Engine.CourierAPI.ChangeStatus(ctx.GoCtx, ctx.CurrentOrder.OrderID, "shipping", false)
			require.NoError(t, err, "Courier update status to shipping failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Order status must transition to PICKED_UP", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusPickedUp, statemachine.RoleCourier)
			require.Equal(t, "shipping", ctx.CurrentOrder.CourierStatus)
		}).
		When("Step 6: Courier hands order to client and marks as completed (DELIVERED)", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.CourierAPI.ChangeStatus(ctx.GoCtx, ctx.CurrentOrder.OrderID, "complete", false)
			require.NoError(t, err, "Courier completing delivery failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Order status must transition to DELIVERED and history verified", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusDelivered, statemachine.RoleCourier)

			// change-status does not return the order, so the terminal state is
			// read back from the backend instead of trusted from the response.
			// A delivered order is no longer active, hence the full history.
			finalOrder, err := ctx.Engine.ClientAPI.GetFinalOrder(ctx.GoCtx, ctx.CurrentOrder.OrderID)
			require.NoError(t, err, "Reading the delivered order back must succeed")
			require.Equal(t, "complete", finalOrder.Status, "заказ должен быть сохранён завершённым")

			// Validate complete recorded history path
			history := ctx.Engine.StateMachine.GetHistory(ctx.CurrentOrder.OrderID)
			expectedPath := []statemachine.OrderStatus{
				statemachine.StatusNew,
				statemachine.StatusCourierAssigned,
				statemachine.StatusPreparing,
				statemachine.StatusReadyForPickup,
				statemachine.StatusPickedUp,
				statemachine.StatusDelivered,
			}
			require.Equal(t, expectedPath, history, "Order must have strictly followed standard Flow A trajectory")
		})
}
