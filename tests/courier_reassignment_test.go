package tests

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Courier_Reassignment_Flow(t *testing.T) {
	ctx := context.Background()

	// 1. Create client and 2 different couriers
	octx, err := testEngine.Fixtures.PrepareRestaurantOrder(ctx)
	require.NoError(t, err)

	courier1ID, _, err := testEngine.Fixtures.CreateUniqueCourier(ctx)
	require.NoError(t, err)
	courier2ID, _, err := testEngine.Fixtures.CreateUniqueCourier(ctx)
	require.NoError(t, err)

	adminToken, err := testEngine.SessionMgr.LoginAdmin(ctx, testEngine.Config.AdminLogin, testEngine.Config.AdminPassword)
	require.NoError(t, err)
	testEngine.SessionMgr.SetAdminSession("admin", adminToken, testEngine.Config.AdminLogin)

	// 2. Create order
	order, err := testEngine.ClientAPI.CreateRestaurantOrder(ctx, octx.OrderRequest())
	require.NoError(t, err)

	// 3. Dispatch to Courier 1
	order1, err := testEngine.AdminAPI.AssignCourier(ctx, order.OrderID, courier1ID)
	require.NoError(t, err)
	require.Equal(t, courier1ID, order1.CourierID, "Order must be assigned to Courier 1")
	t.Logf("    ✓ Initially assigned to Courier 1: %s", courier1ID)

	// 4. Reassign to Courier 2
	order2, err := testEngine.AdminAPI.AssignCourier(ctx, order.OrderID, courier2ID)
	require.NoError(t, err)
	require.Equal(t, courier2ID, order2.CourierID, "Order must be successfully reassigned to Courier 2")
	t.Logf("    ✓ Successfully reassigned to Courier 2: %s", courier2ID)
}
