package runner

import (
	"fmt"

	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/fixtures"
)

// apiStatusChecks covers the guards around order status: who may move an
// order, from which state, and what an illegal move is answered with.
//
// Unlike the state-machine suite, which validates the engine's own transition
// table, these checks talk to the backend and assert the backend enforces it.
func apiStatusChecks() []apiCheck {
	return []apiCheck{
		{
			ID:    "courier_status_needs_assignment",
			Title: "Курьер не двигает статус непринятого заказа",
			Needs: []string{"courier"},
			Run: func(env *apiEnv) error {
				// A courier that never took the order must not be able to
				// drive it — the state used to be corruptible this way.
				err := env.request("POST", "/api/couriers/change-status", env.CourierToken,
					client.CourierChangeStatusRequest{OrderID: "00000000-0000-0000-0000-000000000000", Status: "going"}, nil)
				if err == nil {
					return fmt.Errorf("статус несуществующего заказа изменён")
				}
				if status := client.StatusOf(err); status >= 500 {
					return fmt.Errorf("ожидался отказ 4xx, получено %d: %v", status, err)
				}
				return nil
			},
		},
		{
			ID:    "courier_take_unknown_order",
			Title: "POST /couriers/take-order — несуществующий заказ не принимается",
			Needs: []string{"courier"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/couriers/take-order", env.CourierToken,
					client.CourierTakeOrderRequest{OrderID: "00000000-0000-0000-0000-000000000000"}, nil)
				if err == nil {
					return fmt.Errorf("курьер принял несуществующий заказ")
				}
				if status := client.StatusOf(err); status >= 500 {
					return fmt.Errorf("ожидался отказ 4xx, получено %d: %v", status, err)
				}
				return nil
			},
		},
		{
			ID:    "rest_status_unknown_order",
			Title: "POST /rests/order-status — чужой заказ не переводится",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/rests/order-status", env.RestToken,
					client.ChangeRestOrderStatusRequest{OrderID: "00000000-0000-0000-0000-000000000000", Status: "cooking"}, nil)
				if err == nil {
					return fmt.Errorf("ресторан перевёл несуществующий заказ")
				}
				if status := client.StatusOf(err); status >= 500 {
					return fmt.Errorf("ожидался отказ 4xx, получено %d: %v", status, err)
				}
				return nil
			},
		},
		{
			ID:    "rest_status_invalid_value",
			Title: "POST /rests/order-status — неизвестный статус отклоняется",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/rests/order-status", env.RestToken,
					map[string]interface{}{"order_id": "00000000-0000-0000-0000-000000000000", "status": "телепортирован"}, nil)
				if err == nil {
					return fmt.Errorf("несуществующий статус принят")
				}
				if status := client.StatusOf(err); status >= 500 {
					return fmt.Errorf("неизвестный статус уронил бэкенд (%d): %v", status, err)
				}
				return nil
			},
		},
		{
			ID:    "client_cancel_unknown_order",
			Title: "POST /clients/orders/{id}/cancel — чужой заказ не отменяется",
			Needs: []string{"client"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/clients/orders/00000000-0000-0000-0000-000000000000/cancel",
					env.ClientToken, map[string]string{"cancel_reason": "проверка"}, nil)
				if err == nil {
					return fmt.Errorf("клиент отменил несуществующий заказ")
				}
				if status := client.StatusOf(err); status >= 500 {
					return fmt.Errorf("ожидался отказ 4xx, получено %d: %v", status, err)
				}
				return nil
			},
		},
		{
			ID:    "admin_assign_unknown_courier",
			Title: "POST /admin/give-order-to-courier — неизвестный курьер отклоняется",
			Needs: []string{"admin"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/admin/give-order-to-courier", env.AdminToken,
					client.AssignCourierRequest{
						OrderID:   "00000000-0000-0000-0000-000000000000",
						CourierID: "00000000-0000-0000-0000-000000000000",
					}, nil)
				if err == nil {
					return fmt.Errorf("заказ назначен несуществующему курьеру")
				}
				if status := client.StatusOf(err); status >= 500 {
					return fmt.Errorf("ожидался отказ 4xx, получено %d: %v", status, err)
				}
				return nil
			},
		},
	}
}

// apiGuardChecks walks every role area with every foreign token. The matrix is
// the point: a gate that exists for one zone and not another is exactly the
// hole that gets shipped.
func apiGuardChecks() []apiCheck {
	type zone struct {
		id     string
		title  string
		method string
		path   string
		body   interface{}
		// allowed roles; everyone else must be refused with 403
		allow map[string]bool
		code  string
	}

	zones := []zone{
		{
			id: "courier_zone", title: "Курьерская зона закрыта для чужих ролей",
			method: "POST", path: "/api/couriers/take-order",
			body:  client.CourierTakeOrderRequest{OrderID: "00000000-0000-0000-0000-000000000000"},
			allow: map[string]bool{"courier": true, "admin": true}, code: "COURIER_ONLY",
		},
		{
			// adminOnly deliberately admits restaurants: they drive part of the
			// admin surface. Клиент и курьер — нет, и это тот гейт, который
			// проверяется здесь.
			id: "admin_zone", title: "Админская зона закрыта для клиента и курьера",
			method: "POST", path: "/api/admin/give-order-to-courier",
			body:  client.AssignCourierRequest{OrderID: "00000000-0000-0000-0000-000000000000"},
			allow: map[string]bool{"admin": true, "rest": true}, code: "ADMIN_ONLY",
		},
		{
			id: "rest_zone", title: "Ресторанная зона закрыта для чужих ролей",
			method: "POST", path: "/api/rests/dishes",
			body:  map[string]interface{}{"title": "Чужое блюдо", "price": 100, "category_id": "00000000-0000-0000-0000-000000000000"},
			allow: map[string]bool{"rest": true, "admin": true}, code: "",
		},
	}

	checks := []apiCheck{
		{
			ID:    "no_token",
			Title: "Без токена защищённые ручки отвечают 401 NO_TOKEN_INCLUDED",
			Needs: []string{},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/couriers/take-order", "",
					client.CourierTakeOrderRequest{OrderID: "x"}, nil)
				if !client.IsUnauthorized(err) {
					return fmt.Errorf("ожидался 401, получено: %v", err)
				}
				if apiErr, ok := client.AsAPIError(err); ok && apiErr.Code != "NO_TOKEN_INCLUDED" {
					return fmt.Errorf("ожидался код NO_TOKEN_INCLUDED, получен %q", apiErr.Code)
				}
				return nil
			},
		},
		{
			ID:    "broken_token",
			Title: "Битый токен отвечает 401 INVALID_TOKEN, а не «токена нет»",
			Needs: []string{},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/couriers/take-order", "definitely.not.a.jwt",
					client.CourierTakeOrderRequest{OrderID: "x"}, nil)
				if !client.IsUnauthorized(err) {
					return fmt.Errorf("ожидался 401, получено: %v", err)
				}
				if apiErr, ok := client.AsAPIError(err); ok && apiErr.Code != "INVALID_TOKEN" {
					return fmt.Errorf("ожидался код INVALID_TOKEN, получен %q", apiErr.Code)
				}
				return nil
			},
		},
		{
			ID:    "public_endpoints_open",
			Title: "Публичные ручки входа доступны без токена",
			Needs: []string{},
			Run: func(env *apiEnv) error {
				// A broken PUBLIC_RULES list locks every user out of the app,
				// so the openness of these is as much a contract as the gates.
				return env.expectOK("POST", "/api/clients/register", "",
					map[string]string{"phoneNumber": fixtures.NewClientPhone()}, nil, "публичный register")
			},
		},
	}

	for _, z := range zones {
		z := z
		checks = append(checks, apiCheck{
			ID:    z.id,
			Title: z.title,
			Needs: []string{"client", "rest", "courier"},
			Run: func(env *apiEnv) error {
				tokens := map[string]string{
					"client":  env.ClientToken,
					"rest":    env.RestToken,
					"courier": env.CourierToken,
				}

				for role, token := range tokens {
					if z.allow[role] {
						continue
					}
					err := env.request(z.method, z.path, token, z.body, nil)
					if err == nil {
						return fmt.Errorf("токен роли %s прошёл в %s", role, z.path)
					}
					if !client.IsForbidden(err) {
						return fmt.Errorf("токен роли %s: ожидался 403 на %s, получено %d (%v)",
							role, z.path, client.StatusOf(err), err)
					}
					if z.code != "" {
						if apiErr, ok := client.AsAPIError(err); ok && apiErr.Code != z.code {
							return fmt.Errorf("токен роли %s: ожидался код %s, получен %q", role, z.code, apiErr.Code)
						}
					}
				}
				return nil
			},
		})
	}

	checks = append(checks, apiCheck{
		ID:    "admin_passes_courier_zone",
		Title: "Админский токен пропускается в курьерскую зону",
		Needs: []string{"admin"},
		Run: func(env *apiEnv) error {
			// courierOnly deliberately lets service tokens through — the
			// distribution service calls take-order with courier_id in body.
			err := env.request("POST", "/api/couriers/take-order", env.AdminToken,
				client.CourierTakeOrderRequest{OrderID: "00000000-0000-0000-0000-000000000000"}, nil)
			if err != nil && client.IsForbidden(err) {
				return fmt.Errorf("админский токен заблокирован гейтом courierOnly: %v", err)
			}
			return nil
		},
	})

	return checks
}
