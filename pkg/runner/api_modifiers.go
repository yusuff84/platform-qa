package runner

import (
	"fmt"
	"net/http"
)

// apiModifierChecks covers modifiers and modifier groups: the pieces a dish is
// configured with, and the selection limits the cart later enforces.
func apiModifierChecks() []apiCheck {
	return []apiCheck{
		{
			ID:    "modifier_create",
			Title: "POST /rests/modificators — модификаторы создаются пачкой",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var created []map[string]interface{}
				if err := env.expectOK("POST", "/api/rests/modificators", env.RestToken, map[string]interface{}{
					"items": []map[string]interface{}{
						{"title": "Сыр", "price": 50},
						{"title": "Бекон", "price": 80},
					},
				}, &created, "создание модификаторов"); err != nil {
					return err
				}
				if len(created) != 2 {
					return fmt.Errorf("создано %d модификаторов вместо 2", len(created))
				}
				return nil
			},
		},
		{
			ID:    "modifier_requires_items_array",
			Title: "POST /rests/modificators — items обязан быть массивом",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				return env.expectStatus("POST", "/api/rests/modificators", env.RestToken,
					map[string]interface{}{"items": "не массив"}, http.StatusBadRequest, "items строкой")
			},
		},
		{
			ID:    "modifier_list",
			Title: "GET /rests/modificators — список отдаётся",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var out interface{}
				return env.expectOK("GET", "/api/rests/modificators", env.RestToken, nil, &out, "список модификаторов")
			},
		},
		{
			ID:    "modifier_update",
			Title: "PUT /rests/modificators/{id} — модификатор редактируется",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				id, err := env.createModifier("Соус", 30)
				if err != nil {
					return err
				}
				return env.expectOK("PUT", "/api/rests/modificators/"+id, env.RestToken,
					map[string]interface{}{"title": "Соус острый", "price": 45}, nil, "редактирование модификатора")
			},
		},
		{
			ID:    "modifier_toggle_activity",
			Title: "PATCH /rests/modificators/{id}/activity — модификатор выключается",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				id, err := env.createModifier("Лук", 10)
				if err != nil {
					return err
				}
				if err := env.expectOK("PATCH", fmt.Sprintf("/api/rests/modificators/%s/activity", id), env.RestToken,
					map[string]interface{}{"isActive": false}, nil, "выключение модификатора"); err != nil {
					return err
				}
				return env.expectOK("PATCH", fmt.Sprintf("/api/rests/modificators/%s/activity", id), env.RestToken,
					map[string]interface{}{"isActive": true}, nil, "включение модификатора")
			},
		},
		{
			ID:    "modifier_delete",
			Title: "DELETE /rests/modificators/{id} — модификатор удаляется",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				id, err := env.createModifier("Временный", 5)
				if err != nil {
					return err
				}
				return env.expectOK("DELETE", "/api/rests/modificators/"+id, env.RestToken, nil, nil, "удаление модификатора")
			},
		},
		{
			ID:    "group_modifier_create",
			Title: "POST /rests/group-modifiers — группа модификаторов создаётся",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				itemID, err := env.createModifier("Двойной сыр", 60)
				if err != nil {
					return err
				}

				var group map[string]interface{}
				if err := env.expectOK("POST", "/api/rests/group-modifiers", env.RestToken, map[string]interface{}{
					"name":         "Добавки",
					"minSelection": 0,
					"maxSelection": 2,
					"isRequired":   false,
					"items":        []string{itemID},
				}, &group, "создание группы модификаторов"); err != nil {
					return err
				}
				if group == nil {
					return fmt.Errorf("пустой ответ при создании группы")
				}
				return nil
			},
		},
		{
			ID:    "group_modifier_list",
			Title: "GET /rests/group-modifiers — список групп отдаётся",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				var out interface{}
				return env.expectOK("GET", "/api/rests/group-modifiers", env.RestToken, nil, &out, "список групп модификаторов")
			},
		},
		{
			ID:    "group_modifier_update",
			Title: "PUT /rests/group-modifiers/{id} — лимиты выбора меняются",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				id, err := env.createGroupModifier("Соусы", 0, 1)
				if err != nil {
					return err
				}
				return env.expectOK("PUT", "/api/rests/group-modifiers/"+id, env.RestToken,
					map[string]interface{}{"name": "Соусы", "minSelection": 1, "maxSelection": 3, "isRequired": true},
					nil, "изменение лимитов группы")
			},
		},
		{
			ID:    "group_modifier_delete",
			Title: "DELETE /rests/group-modifiers/{id} — группа удаляется",
			Needs: []string{"rest"},
			Run: func(env *apiEnv) error {
				id, err := env.createGroupModifier("На удаление", 0, 1)
				if err != nil {
					return err
				}
				return env.expectOK("DELETE", "/api/rests/group-modifiers/"+id, env.RestToken, nil, nil, "удаление группы")
			},
		},
		{
			ID:    "modifier_foreign_access",
			Title: "Модификаторы недоступны клиентскому токену",
			Needs: []string{"rest", "client"},
			Run: func(env *apiEnv) error {
				err := env.request("POST", "/api/rests/modificators", env.ClientToken,
					map[string]interface{}{"items": []map[string]interface{}{{"title": "Чужой", "price": 1}}}, nil)
				if err == nil {
					return fmt.Errorf("клиентский токен создал модификатор ресторана")
				}
				return nil
			},
		},
	}
}

// createModifier makes one modifier and returns its id.
func (env *apiEnv) createModifier(title string, price float64) (string, error) {
	var created []map[string]interface{}
	if err := env.expectOK("POST", "/api/rests/modificators", env.RestToken, map[string]interface{}{
		"items": []map[string]interface{}{{"title": title, "price": price}},
	}, &created, "подготовка модификатора"); err != nil {
		return "", err
	}
	if len(created) == 0 {
		return "", fmt.Errorf("модификатор не создан")
	}
	id := idFrom(created[0], "modificator_id", "id")
	if id == "" {
		return "", fmt.Errorf("в ответе нет идентификатора модификатора: %v", keysOf(created[0]))
	}
	return id, nil
}

// createGroupModifier makes one modifier group and returns its id.
func (env *apiEnv) createGroupModifier(name string, min, max int) (string, error) {
	itemID, err := env.createModifier(name+" элемент", 15)
	if err != nil {
		return "", err
	}

	var group map[string]interface{}
	if err := env.expectOK("POST", "/api/rests/group-modifiers", env.RestToken, map[string]interface{}{
		"name":         name,
		"minSelection": min,
		"maxSelection": max,
		"isRequired":   false,
		"items":        []string{itemID},
	}, &group, "подготовка группы модификаторов"); err != nil {
		return "", err
	}
	if id := idFrom(group, "groupmodifier_id", "group_id", "id"); id == "" {
		return "", fmt.Errorf("в ответе нет идентификатора группы: %v", keysOf(group))
	}
	return idFrom(group, "groupmodifier_id", "group_id", "id"), nil
}

// idFrom pulls the first present identifier out of a loosely typed response.
// The menu endpoints are not consistent about which name they use.
func idFrom(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
