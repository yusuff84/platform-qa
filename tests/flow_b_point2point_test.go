package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/dsl"
	"locali-e2e-engine/pkg/events"
	"locali-e2e-engine/pkg/statemachine"
)

func Test_FlowB_IndependentOrderPointAToPointB(t *testing.T) {
	// Parcel delivery runs on a platform working window (10:00–23:00). Outside
	// it the backend refuses every parcel with 422 OUTSIDE_WORKING_HOURS — the
	// platform is closed, which is not a defect to report as a failure.
	requireParcelWindowOpen(t)

	testEngine.Scenario(t, "Flow B: Independent Point A to Point B Courier Parcel Delivery").
		Given("Client, Courier and Director identities are initialized", func(ctx *dsl.TestContext) {
			ctx.SetupAllRoles()
			ctx.OrderType = statemachine.OrderTypeIndependent
		}).
		When("Step 1: Client creates an independent parcel delivery order", func(ctx *dsl.TestContext) {
			// A parcel is addressed by coordinates and names its recipient
			// and cargo type; free-text A/B addresses are no longer accepted.
			req := ctx.Engine.Fixtures.ParcelRequest("")
			req.Comment = "Передать конверт с документами"

			order, err := ctx.Engine.ClientAPI.CreateIndependentOrder(ctx.GoCtx, req)
			require.NoError(t, err, "Client independent order creation failed")
			require.NotEmpty(t, order.OrderID, "OrderID must be generated")

			ctx.CurrentOrder = order

			_ = ctx.Engine.Events.PublishEvent(&events.Event{
				Subject:   "independent_order.created",
				OrderID:   order.OrderID,
				Status:    "new",
				Timestamp: time.Now(),
			})
		}).
		Then("Status must be NEW and valid according to State Machine", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusNew, statemachine.RoleClient)
			require.Equal(t, "new", ctx.CurrentOrder.Status)
		}).
		When("Step 2: Director assigns courier to independent parcel order", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.AdminAPI.AssignCourier(ctx.GoCtx, ctx.CurrentOrder.OrderID, ctx.CourierID)
			require.NoError(t, err, "Director assigning courier failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Status must be COURIER_ASSIGNED", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusCourierAssigned, statemachine.RoleAdmin)
			require.NotEmpty(t, ctx.CurrentOrder.CourierID)
		}).
		When("Step 3: Courier picks up parcel at Point A (PICKED_UP / shipping)", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.CourierAPI.ChangeStatus(ctx.GoCtx, ctx.CurrentOrder.OrderID, "shipping", true)
			require.NoError(t, err, "Courier picking up parcel failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Status must transition to PICKED_UP", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusPickedUp, statemachine.RoleCourier)
			require.Equal(t, "shipping", ctx.CurrentOrder.CourierStatus)
		}).
		When("Step 4: Courier arrives at Point B and completes delivery (DELIVERED / complete)", func(ctx *dsl.TestContext) {
			updatedOrder, err := ctx.Engine.CourierAPI.ChangeStatus(ctx.GoCtx, ctx.CurrentOrder.OrderID, "complete", true)
			require.NoError(t, err, "Courier completing independent delivery failed")

			ctx.CurrentOrder = updatedOrder
		}).
		Then("Status must transition to DELIVERED and trajectory verified", func(ctx *dsl.TestContext) {
			ctx.AssertOrderStatus(statemachine.StatusDelivered, statemachine.RoleCourier)
			require.Equal(t, "complete", ctx.CurrentOrder.Status)

			history := ctx.Engine.StateMachine.GetHistory(ctx.CurrentOrder.OrderID)
			expectedPath := []statemachine.OrderStatus{
				statemachine.StatusNew,
				statemachine.StatusCourierAssigned,
				statemachine.StatusPickedUp,
				statemachine.StatusDelivered,
			}
			require.Equal(t, expectedPath, history, "Independent order must have followed Flow B path")
		})
}

// requireParcelWindowOpen skips the calling test when the platform is not
// accepting parcel orders at this hour.
func requireParcelWindowOpen(t *testing.T) {
	t.Helper()
	available, window, err := testEngine.Fixtures.ParcelOrderingAvailable(context.Background())
	if err != nil {
		t.Fatalf("не удалось узнать окно приёма посылок: %v", err)
	}
	if !available {
		t.Skipf("платформа не принимает посылки вне окна %s — прогон возможен только внутри него", window)
	}
}
