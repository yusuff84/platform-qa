package runner

import (
	"fmt"
	"net/http"

	"locali-e2e-engine/pkg/client"
)

// apiCartChecks covers the cart the restaurant order is assembled from: what
// goes in, what the quantity rules are, and what happens to an order placed
// against an empty one.
func apiCartChecks() []apiCheck {
	return []apiCheck{
		{
			ID:    "cart_add_item",
			Title: "POST /cart/items — блюдо кладётся в корзину",
			Needs: []string{"client", "dish"},
			Run: func(env *apiEnv) error {
				var out client.AddCartItemResponse
				if err := env.expectOK("POST", "/api/cart/items", env.ClientToken, client.AddCartItemRequest{
					EntityID:   env.DishID,
					EntityType: client.EntityTypeDish,
					Quantity:   2,
				}, &out, "добавление в корзину"); err != nil {
					return err
				}
				if out.Item.Quantity != 2 {
					return fmt.Errorf("количество не сохранилось: %d вместо 2", out.Item.Quantity)
				}
				if out.Item.ItemPrice <= 0 {
					return fmt.Errorf("цена позиции не проставлена: %v", out.Item.ItemPrice)
				}
				return nil
			},
		},
		{
			ID:    "cart_requires_entity",
			Title: "POST /cart/items — без entity_id отклоняется",
			Needs: []string{"client"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("POST", "/api/cart/items", env.ClientToken,
					map[string]interface{}{"entity_type": "dish", "quantity": 1},
					http.StatusBadRequest, "позиция без entity_id")
			},
		},
		{
			ID:    "cart_rejects_bad_quantity",
			Title: "POST /cart/items — нулевое количество отклоняется",
			Needs: []string{"client", "dish"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("POST", "/api/cart/items", env.ClientToken,
					map[string]interface{}{"entity_id": env.DishID, "entity_type": "dish", "quantity": 0},
					http.StatusBadRequest, "нулевое количество")
			},
		},
		{
			ID:    "cart_unknown_dish",
			Title: "POST /cart/items — несуществующее блюдо → 404",
			Needs: []string{"client"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("POST", "/api/cart/items", env.ClientToken,
					map[string]interface{}{
						"entity_id":   "00000000-0000-0000-0000-000000000000",
						"entity_type": "dish",
						"quantity":    1,
					}, http.StatusNotFound, "несуществующее блюдо")
			},
		},
		{
			ID:    "cart_read",
			Title: "GET /cart — корзина читается",
			Needs: []string{"client"},
			Run: func(env *apiEnv) error {
				var out interface{}
				return env.expectOK("GET", "/api/cart", env.ClientToken, nil, &out, "чтение корзины")
			},
		},
		{
			ID:    "cart_clear",
			Title: "DELETE /cart/groups/{rest} — корзина очищается",
			Needs: []string{"client", "rest", "dish"},
			Run: func(env *apiEnv) error {
				if _, err := env.o.engine.ClientAPI.AddCartItem(env.ctx, client.AddCartItemRequest{
					EntityID:   env.DishID,
					EntityType: client.EntityTypeDish,
					Quantity:   1,
				}); err != nil {
					return fmt.Errorf("подготовка корзины: %w", err)
				}
				return env.expectOK("DELETE", "/api/cart/groups/"+env.RestID, env.ClientToken, nil, nil, "очистка корзины")
			},
		},
		{
			ID:    "order_needs_cart",
			Title: "POST /clients/create-order — с пустой корзиной заказ не создаётся",
			Needs: []string{"client", "rest"},
			Run: func(env *apiEnv) error {
				// The order is assembled from the cart, so an empty cart must
				// not produce an order — that is the whole contract.
				_ = env.request("DELETE", "/api/cart/groups/"+env.RestID, env.ClientToken, nil, nil)

				err := env.request("POST", "/api/clients/create-order", env.ClientToken,
					client.CreateRestaurantOrderRequest{
						RestID:        env.RestID,
						ReceiveMethod: client.ReceiveDelivery,
						Payment:       client.OrderPayment{Type: client.PaymentCash},
						Source:        client.SourceLokaliEda,
					}, nil)
				if err == nil {
					return fmt.Errorf("заказ создан при пустой корзине")
				}
				if client.StatusOf(err) >= 500 {
					return fmt.Errorf("пустая корзина уронила бэкенд: %v", err)
				}
				return nil
			},
		},
	}
}

// apiWalletChecks covers the courier and restaurant wallets: reading the
// balance, the operation history, and the top-up entry point.
func apiWalletChecks() []apiCheck {
	return []apiCheck{
		{
			ID:    "courier_wallet_read",
			Title: "GET /couriers/wallet — курьер видит свой кошелёк",
			Needs: []string{"courier"},
			Run: func(env *apiEnv) error {
				var wallet map[string]interface{}
				if err := env.expectOK("GET", "/api/couriers/wallet", env.CourierToken, nil, &wallet, "кошелёк курьера"); err != nil {
					return err
				}
				if wallet == nil {
					return fmt.Errorf("пустой ответ кошелька")
				}
				// A wallet is created together with the courier, so a courier
				// without one means the atomic creation broke.
				if !hasAnyKey(wallet, "wallet", "balance", "wallet_id", "data") {
					return fmt.Errorf("в ответе нет признаков кошелька: %v", keysOf(wallet))
				}
				return nil
			},
		},
		{
			ID:    "courier_wallet_operations",
			Title: "GET /couriers/wallet/operations — история операций отдаётся",
			Needs: []string{"courier"},
			Run: func(env *apiEnv) error {
				var out interface{}
				return env.expectOK("GET", "/api/couriers/wallet/operations", env.CourierToken, nil, &out, "операции кошелька курьера")
			},
		},
		{
			ID:    "courier_wallet_topup_validation",
			Title: "POST /couriers/wallet/topup — некорректная сумма отклоняется",
			Needs: []string{"courier"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/couriers/wallet/topup", env.CourierToken,
					map[string]interface{}{"amount": -100}, nil)
				if err == nil {
					return fmt.Errorf("пополнение на отрицательную сумму прошло")
				}
				if client.StatusOf(err) >= 500 {
					return fmt.Errorf("отрицательная сумма уронила бэкенд: %v", err)
				}
				return nil
			},
		},
		{
			ID:    "courier_wallet_isolation",
			Title: "Кошелёк курьера недоступен клиентскому токену",
			Needs: []string{"courier", "client"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("GET", "/api/couriers/wallet", env.ClientToken, nil,
					http.StatusForbidden, "клиент у кошелька курьера")
			},
		},
		{
			ID:    "rest_wallet_read",
			Title: "GET /rests/wallet — ресторан видит свой кошелёк",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var wallet map[string]interface{}
				return env.expectOK("GET", "/api/rests/wallet", env.RestToken, nil, &wallet, "кошелёк ресторана")
			},
		},
		{
			ID:    "rest_wallet_operations",
			Title: "GET /rests/wallet/operations — история операций ресторана",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var out interface{}
				return env.expectOK("GET", "/api/rests/wallet/operations", env.RestToken, nil, &out, "операции кошелька ресторана")
			},
		},
	}
}

func hasAnyKey(m map[string]interface{}, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
