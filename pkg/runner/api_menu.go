package runner

import (
	"fmt"
	"net/http"

	"locali-e2e-engine/pkg/client"
)

// apiMenuChecks covers the restaurant menu surface: categories, the dish
// lifecycle (create, read, edit, hide, stop-list, delete) and the validation
// that guards it.
func apiMenuChecks() []apiCheck {
	return []apiCheck{
		{
			ID:    "category_create",
			Title: "POST /rests/categories — категория создаётся",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var created []client.CategoryResponse
				if err := env.expectOK("POST", "/api/rests/categories", env.RestToken,
					client.CreateCategoryRequest{Title: "Категория проверки"}, &created, "создание категории"); err != nil {
					return err
				}
				if len(created) == 0 || created[0].CategoryID == "" {
					return fmt.Errorf("ответ без category_id: %+v", created)
				}
				return nil
			},
		},
		{
			ID:    "category_requires_title",
			Title: "POST /rests/categories — без названия отклоняется",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("POST", "/api/rests/categories", env.RestToken,
					map[string]interface{}{"title": ""}, http.StatusBadRequest, "категория без названия")
			},
		},
		{
			ID:    "category_list",
			Title: "GET /rests/categories — список отдаётся",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var out interface{}
				return env.expectOK("GET", "/api/rests/categories", env.RestToken, nil, &out, "список категорий")
			},
		},
		{
			ID:    "dish_create",
			Title: "POST /rests/dishes — блюдо создаётся под своим рестораном",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				var dish client.DishResponse
				if err := env.expectOK("POST", "/api/rests/dishes", env.RestToken, client.CreateDishRequest{
					Title:      "Блюдо проверки",
					Price:      450,
					CategoryID: env.CategoryID,
				}, &dish, "создание блюда"); err != nil {
					return err
				}
				if dish.DishID == "" {
					return fmt.Errorf("ответ без dish_id")
				}
				// The owning restaurant comes from the token, never from the
				// body — a dish landing under someone else's rest_id is a leak.
				if dish.RestID != "" && dish.RestID != env.RestID {
					return fmt.Errorf("блюдо создано под чужим рестораном: %s вместо %s", dish.RestID, env.RestID)
				}
				return nil
			},
		},
		{
			ID:    "dish_validation",
			Title: "POST /rests/dishes — без названия и категории отклоняется",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				if err := env.expectStatus("POST", "/api/rests/dishes", env.RestToken,
					map[string]interface{}{"price": 100}, http.StatusBadRequest, "блюдо без названия"); err != nil {
					return err
				}
				return env.expectStatus("POST", "/api/rests/dishes", env.RestToken,
					map[string]interface{}{"title": "Без цены", "category_id": env.CategoryID},
					http.StatusBadRequest, "блюдо без цены")
			},
		},
		{
			ID:    "dish_negative_price",
			Title: "POST /rests/dishes — отрицательная цена отклоняется",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("POST", "/api/rests/dishes", env.RestToken,
					map[string]interface{}{"title": "Минус цена", "price": -10, "category_id": env.CategoryID},
					http.StatusBadRequest, "блюдо с отрицательной ценой")
			},
		},
		{
			ID:    "dish_get_one",
			Title: "GET /rests/dishes/{id} — блюдо читается по идентификатору",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				var dish client.DishResponse
				if err := env.expectOK("GET", "/api/rests/dishes/"+env.DishID, env.RestToken, nil, &dish, "чтение блюда"); err != nil {
					return err
				}
				if dish.DishID != env.DishID {
					return fmt.Errorf("вернулось другое блюдо: %s", dish.DishID)
				}
				return nil
			},
		},
		{
			ID:    "dish_update",
			Title: "PATCH /rests/dishes/{id} — редактирование применяется",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				const newTitle = "Блюдо после правки"
				const newPrice = 777

				if err := env.expectOK("PATCH", "/api/rests/dishes/"+env.DishID, env.RestToken,
					map[string]interface{}{"title": newTitle, "price": newPrice}, nil, "редактирование блюда"); err != nil {
					return err
				}

				// Read back: an endpoint that answers 200 and changes nothing
				// is the failure this check exists for.
				var dish client.DishResponse
				if err := env.expectOK("GET", "/api/rests/dishes/"+env.DishID, env.RestToken, nil, &dish, "чтение после правки"); err != nil {
					return err
				}
				if dish.Title != newTitle {
					return fmt.Errorf("название не сохранилось: %q вместо %q", dish.Title, newTitle)
				}
				if dish.Price != newPrice {
					return fmt.Errorf("цена не сохранилась: %v вместо %v", dish.Price, newPrice)
				}
				return nil
			},
		},
		{
			ID:    "dish_hide",
			Title: "PATCH /rests/dishes/{id}/hide — блюдо скрывается и возвращается",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				if err := env.expectOK("PATCH", fmt.Sprintf("/api/rests/dishes/%s/hide", env.DishID), env.RestToken,
					map[string]interface{}{"isHidden": true}, nil, "скрытие блюда"); err != nil {
					return err
				}

				var hidden client.DishResponse
				if err := env.expectOK("GET", "/api/rests/dishes/"+env.DishID, env.RestToken, nil, &hidden, "чтение скрытого блюда"); err != nil {
					return err
				}
				if !hidden.IsHidden {
					return fmt.Errorf("блюдо осталось видимым после скрытия")
				}

				// Put it back: later checks in this suite order this dish.
				return env.expectOK("PATCH", fmt.Sprintf("/api/rests/dishes/%s/hide", env.DishID), env.RestToken,
					map[string]interface{}{"isHidden": false}, nil, "возврат блюда из скрытых")
			},
		},
		{
			ID:    "dish_stop_list",
			Title: "PUT /rests/stop-list — блюдо снимается с продажи",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				if err := env.expectOK("PUT", "/api/rests/stop-list", env.RestToken,
					map[string]interface{}{"dish_id": env.DishID, "on_sell": false}, nil, "стоп-лист блюда"); err != nil {
					return err
				}
				return env.expectOK("PUT", "/api/rests/stop-list", env.RestToken,
					map[string]interface{}{"dish_id": env.DishID, "on_sell": true}, nil, "возврат блюда в продажу")
			},
		},
		{
			ID:    "dish_foreign_update",
			Title: "PATCH /rests/dishes/{id} — чужое блюдо не редактируется",
			Needs: []string{"rest", "dish", "client"},
			Run: func(env *apiEnv) error {
				// A client token has no business in the restaurant's menu at
				// all; whatever the refusal is, it must not be a success.
				err := env.request("PATCH", "/api/rests/dishes/"+env.DishID, env.ClientToken,
					map[string]interface{}{"title": "Захвачено"}, nil)
				if err == nil {
					return fmt.Errorf("клиентский токен отредактировал блюдо ресторана")
				}
				return nil
			},
		},
		{
			ID:    "dish_delete",
			Title: "DELETE /rests/dish — блюдо удаляется",
			Needs: []string{"rest", "dish"},
			Run: func(env *apiEnv) error {
				var created client.DishResponse
				if err := env.expectOK("POST", "/api/rests/dishes", env.RestToken, client.CreateDishRequest{
					Title:      "Блюдо на удаление",
					Price:      120,
					CategoryID: env.CategoryID,
				}, &created, "создание блюда для удаления"); err != nil {
					return err
				}
				return env.expectOK("DELETE", "/api/rests/dish?dish_id="+created.DishID, env.RestToken, nil, nil, "удаление блюда")
			},
		},
	}
}
