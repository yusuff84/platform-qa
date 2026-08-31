package tests

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"locali-e2e-engine/pkg/client"
)

func Test_Idempotency_And_HighConcurrency(t *testing.T) {
	ctx := context.Background()

	t.Run("Idempotency: duplicate request with same Idempotency-Key returns identical order", func(t *testing.T) {
		// A restaurant order needs a whole prepared situation: an open
		// restaurant with a dish, a client with an address, and a filled cart.
		octx, err := testEngine.Fixtures.PrepareRestaurantOrder(ctx)
		require.NoError(t, err)

		idemKey := uuid.New().String()
		req := octx.OrderRequest()

		// First submission
		order1, err := testEngine.ClientAPI.CreateRestaurantOrderWithIdempotency(ctx, req, idemKey)
		require.NoError(t, err)
		require.NotEmpty(t, order1.OrderID)

		// Duplicate submission with same Idempotency-Key
		order2, err := testEngine.ClientAPI.CreateRestaurantOrderWithIdempotency(ctx, req, idemKey)
		require.NoError(t, err)
		require.NotEmpty(t, order2.OrderID)

		// Must return identical OrderID (cached idempotent response)
		require.Equal(t, order1.OrderID, order2.OrderID, "Idempotent requests must return identical OrderID")
		require.Equal(t, order1.OrderNumber, order2.OrderNumber)
		t.Logf("    ✓ Idempotency verified: Order1 ID (%s) == Order2 ID (%s)", order1.OrderID, order2.OrderID)
	})

	t.Run("Concurrency: 10 parallel independent order creations without session crosstalk", func(t *testing.T) {
		requireParcelWindowOpen(t)

		parallelCount := 10
		var wg sync.WaitGroup
		errs := make(chan error, parallelCount)
		orderIDs := make(chan string, parallelCount)

		for i := 0; i < parallelCount; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				// Each parallel worker creates its own unique client session
				clientPhone, cToken, err := testEngine.Fixtures.CreateUniqueClient(ctx)
				if err != nil {
					errs <- fmt.Errorf("worker %d client create failed: %w", idx, err)
					return
				}

				// Independent HTTP client call with client token
				var orderResp client.OrderResponse
				orderReq := testEngine.Fixtures.ParcelRequest("")
				orderReq.Comment = fmt.Sprintf("Параллельный воркер %d", idx)

				_, err = testEngine.HTTPClient.Request(ctx, "POST", "/api/clients/independent-order", cToken, orderReq, &orderResp)
				if err != nil {
					errs <- fmt.Errorf("worker %d (%s) order failed: %w", idx, clientPhone, err)
					return
				}

				orderIDs <- orderResp.OrderID
			}(i)
		}

		wg.Wait()
		close(errs)
		close(orderIDs)

		for err := range errs {
			require.NoError(t, err, "Parallel execution error")
		}

		// Ensure all returned order IDs are unique
		seen := make(map[string]bool)
		for id := range orderIDs {
			require.False(t, seen[id], "Duplicate OrderID found under concurrency")
			seen[id] = true
		}

		require.Equal(t, parallelCount, len(seen), "All concurrent orders must produce distinct unique IDs")
		t.Logf("    ✓ Successfully executed %d concurrent order flows with zero data races", parallelCount)
	})
}
