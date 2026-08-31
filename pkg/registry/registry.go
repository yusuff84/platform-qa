// Package registry contains the declarative catalog of E2E suites and their checks.
// It is the single source of truth for the Admin UI: suite keys, human-readable
// titles, categories, tags and stable snake_case check identifiers.
package registry

// CheckItem is a single verifiable assertion inside a suite.
type CheckItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Suite describes a runnable group of checks.
type Suite struct {
	Key         string      `json:"key"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Category    string      `json:"category"` // flow | negative | security | reliability | edge
	Tags        []string    `json:"tags,omitempty"`
	Checks      []CheckItem `json:"checks"`
}

var suites = []Suite{
	{
		Key:         "flow_a",
		Title:       "Flow A: Полный цикл ресторанного заказа",
		Description: "Жизненный цикл заказа в ресторан: создание клиентом, назначение курьера, готовка, выдача и доставка.",
		Category:    "flow",
		Tags:        []string{"restaurant", "lifecycle"},
		Checks: []CheckItem{
			{ID: "setup", Title: "Генерация уникальных фикстур: клиент, ресторан, курьер"},
			{ID: "create_order", Title: "Клиент создаёт заказ → статус NEW"},
			{ID: "assign_courier", Title: "Админ назначает курьера → COURIER_ASSIGNED"},
			{ID: "cooking", Title: "Ресторан начинает готовку → PREPARING"},
			{ID: "ready_for_pickup", Title: "Заказ собран → READY_FOR_PICKUP"},
			{ID: "pickup", Title: "Курьер забрал заказ → PICKED_UP"},
			{ID: "delivered", Title: "Заказ доставлен → DELIVERED"},
		},
	},
	{
		Key:         "flow_b",
		Title:       "Flow B: Доставка посылки А→Б",
		Description: "Независимый заказ-посылка из точки А в точку Б без участия ресторана.",
		Category:    "flow",
		Tags:        []string{"independent", "p2p"},
		Checks: []CheckItem{
			{ID: "setup", Title: "Генерация уникальных фикстур: клиент, курьер"},
			{ID: "create_parcel", Title: "Клиент создаёт заказ-посылку → NEW"},
			{ID: "assign_courier", Title: "Админ назначает курьера → COURIER_ASSIGNED"},
			{ID: "pickup", Title: "Курьер забрал в точке А → PICKED_UP"},
			{ID: "delivered", Title: "Доставлено в точку Б → DELIVERED"},
		},
	},
	{
		Key:         "cancellation",
		Title:       "Отмена заказа на разных стадиях",
		Description: "Правила отмены на ранних стадиях и запрет модификации терминальных состояний.",
		Category:    "edge",
		Checks: []CheckItem{
			{ID: "cancel_at_new", Title: "Клиент отменяет заказ на статусе NEW → успех"},
			{ID: "reject_during_cooking", Title: "Отмена во время готовки отклоняется (409 CANCEL_NOT_ALLOWED)"},
			{ID: "terminal_protection", Title: "Модификация после DELIVERED блокируется (терминальное состояние)"},
		},
	},
	{
		Key:         "idempotency",
		Title:       "Идемпотентность и конкурентность",
		Description: "Защита от дублирующих запросов через Idempotency-Key и изоляция параллельных заказов.",
		Category:    "reliability",
		Checks: []CheckItem{
			{ID: "same_key_same_order", Title: "Повтор запроса с тем же Idempotency-Key → тот же OrderID"},
			{ID: "concurrent_isolation", Title: "10 параллельных созданий → уникальные независимые заказы"},
		},
	},
	{
		Key:         "security_rbac",
		Title:       "RBAC и изоляция токенов",
		Description: "Аутентификация без токена и межролевые ограничения доступа к API.",
		Category:    "security",
		Checks: []CheckItem{
			{ID: "no_token_401", Title: "Запрос без Bearer токена → 401"},
			{ID: "client_admin_403", Title: "Токен клиента к Admin API → 403"},
			{ID: "courier_rest_403", Title: "Токен курьера к Restaurant API → 403"},
		},
	},
	{
		Key:         "auth_otp",
		Title:       "Вход, авторизация и верификация",
		Description: "Контракт входа: выдача одноразового кода, его срок и бюджет попыток, ролевые claims в токене и гейты доступа.",
		Category:    "security",
		Tags:        []string{"auth", "otp", "rbac"},
		Checks: []CheckItem{
			{ID: "otp_issued", Title: "Register выдаёт код (debugCode на стенде с DEBUG=true)"},
			{ID: "otp_login", Title: "Вход по коду → токен клиента с корректными claims"},
			{ID: "otp_replay", Title: "Повторное использование израсходованного кода отклоняется"},
			{ID: "otp_attempts", Title: "5 неверных попыток исчерпывают бюджет → 429"},
			{ID: "otp_resend_limit", Title: "Повторная отправка кода внутри 60 секунд → 429"},
			{ID: "role_claims", Title: "Курьер, ресторан и админ получают свои ролевые claims"},
			{ID: "auth_gates", Title: "Гейты доступа: 401 без токена, 403 между ролями"},
		},
	},
	{
		Key:         "api_menu",
		Title:       "API: меню ресторана",
		Description: "Контракты ручек меню: категории, создание и редактирование блюда, скрытие, стоп-лист, удаление.",
		Category:    "api",
		Tags:        []string{"rest", "menu", "dishes"},
		Checks: []CheckItem{
			{ID: "category_create", Title: "POST /rests/categories — категория создаётся"},
			{ID: "category_requires_title", Title: "POST /rests/categories — без названия отклоняется"},
			{ID: "category_list", Title: "GET /rests/categories — список отдаётся"},
			{ID: "dish_create", Title: "POST /rests/dishes — блюдо создаётся под своим рестораном"},
			{ID: "dish_validation", Title: "POST /rests/dishes — без названия и цены отклоняется"},
			{ID: "dish_negative_price", Title: "POST /rests/dishes — отрицательная цена отклоняется"},
			{ID: "dish_get_one", Title: "GET /rests/dishes/{id} — блюдо читается по идентификатору"},
			{ID: "dish_update", Title: "PATCH /rests/dishes/{id} — редактирование применяется"},
			{ID: "dish_hide", Title: "PATCH /rests/dishes/{id}/hide — блюдо скрывается и возвращается"},
			{ID: "dish_stop_list", Title: "PUT /rests/stop-list — блюдо снимается с продажи"},
			{ID: "dish_foreign_update", Title: "PATCH /rests/dishes/{id} — чужое блюдо не редактируется"},
			{ID: "dish_delete", Title: "DELETE /rests/dish — блюдо удаляется"},
		},
	},
	{
		Key:         "api_modifiers",
		Title:       "API: модификаторы блюд",
		Description: "Модификаторы и группы модификаторов: создание, редактирование, лимиты выбора, удаление, изоляция от чужих ролей.",
		Category:    "api",
		Tags:        []string{"rest", "menu", "modifiers"},
		Checks: []CheckItem{
			{ID: "modifier_create", Title: "POST /rests/modificators — модификаторы создаются пачкой"},
			{ID: "modifier_requires_items_array", Title: "POST /rests/modificators — items обязан быть массивом"},
			{ID: "modifier_list", Title: "GET /rests/modificators — список отдаётся"},
			{ID: "modifier_update", Title: "PUT /rests/modificators/{id} — модификатор редактируется"},
			{ID: "modifier_toggle_activity", Title: "PATCH /rests/modificators/{id}/activity — модификатор выключается"},
			{ID: "modifier_delete", Title: "DELETE /rests/modificators/{id} — модификатор удаляется"},
			{ID: "group_modifier_create", Title: "POST /rests/group-modifiers — группа создаётся"},
			{ID: "group_modifier_list", Title: "GET /rests/group-modifiers — список групп отдаётся"},
			{ID: "group_modifier_update", Title: "PUT /rests/group-modifiers/{id} — лимиты выбора меняются"},
			{ID: "group_modifier_delete", Title: "DELETE /rests/group-modifiers/{id} — группа удаляется"},
			{ID: "modifier_foreign_access", Title: "Модификаторы недоступны клиентскому токену"},
		},
	},
	{
		Key:         "api_cart",
		Title:       "API: корзина клиента",
		Description: "Корзина, из которой собирается ресторанный заказ: добавление, валидация количества, очистка, отказ при пустой корзине.",
		Category:    "api",
		Tags:        []string{"client", "cart", "orders"},
		Checks: []CheckItem{
			{ID: "cart_add_item", Title: "POST /cart/items — блюдо кладётся в корзину"},
			{ID: "cart_requires_entity", Title: "POST /cart/items — без entity_id отклоняется"},
			{ID: "cart_rejects_bad_quantity", Title: "POST /cart/items — нулевое количество отклоняется"},
			{ID: "cart_unknown_dish", Title: "POST /cart/items — несуществующее блюдо → 404"},
			{ID: "cart_read", Title: "GET /cart — корзина читается"},
			{ID: "cart_clear", Title: "DELETE /cart/groups/{rest} — корзина очищается"},
			{ID: "order_needs_cart", Title: "POST /clients/create-order — с пустой корзиной заказ не создаётся"},
		},
	},
	{
		Key:         "api_wallet",
		Title:       "API: кошельки курьера и ресторана",
		Description: "Баланс, история операций и пополнение кошелька, а также изоляция кошелька от чужих ролей.",
		Category:    "api",
		Tags:        []string{"wallet", "courier", "rest"},
		Checks: []CheckItem{
			{ID: "courier_wallet_read", Title: "GET /couriers/wallet — курьер видит свой кошелёк"},
			{ID: "courier_wallet_operations", Title: "GET /couriers/wallet/operations — история операций"},
			{ID: "courier_wallet_topup_validation", Title: "POST /couriers/wallet/topup — некорректная сумма отклоняется"},
			{ID: "courier_wallet_isolation", Title: "Кошелёк курьера недоступен клиентскому токену"},
			{ID: "rest_wallet_read", Title: "GET /rests/wallet — ресторан видит свой кошелёк"},
			{ID: "rest_wallet_operations", Title: "GET /rests/wallet/operations — история операций ресторана"},
		},
	},
	{
		Key:         "api_order_status",
		Title:       "API: статусы заказа",
		Description: "Гарды смены статуса: назначение, принятие заказа курьером, переводы ресторана, отмена клиентом.",
		Category:    "api",
		Tags:        []string{"orders", "status", "guards"},
		Checks: []CheckItem{
			{ID: "courier_status_needs_assignment", Title: "Курьер не двигает статус непринятого заказа"},
			{ID: "courier_take_unknown_order", Title: "POST /couriers/take-order — несуществующий заказ не принимается"},
			{ID: "rest_status_unknown_order", Title: "POST /rests/order-status — чужой заказ не переводится"},
			{ID: "rest_status_invalid_value", Title: "POST /rests/order-status — неизвестный статус отклоняется"},
			{ID: "client_cancel_unknown_order", Title: "POST /clients/orders/{id}/cancel — чужой заказ не отменяется"},
			{ID: "admin_assign_unknown_courier", Title: "POST /admin/give-order-to-courier — неизвестный курьер отклоняется"},
		},
	},
	{
		Key:         "api_guards",
		Title:       "API: ролевые гейты по зонам",
		Description: "Матрица доступа: каждая ролевая зона проверяется всеми чужими токенами, плюс отсутствие и порча токена.",
		Category:    "api",
		Tags:        []string{"security", "rbac", "guards"},
		Checks: []CheckItem{
			{ID: "no_token", Title: "Без токена → 401 NO_TOKEN_INCLUDED"},
			{ID: "broken_token", Title: "Битый токен → 401 INVALID_TOKEN"},
			{ID: "public_endpoints_open", Title: "Публичные ручки входа доступны без токена"},
			{ID: "courier_zone", Title: "Курьерская зона закрыта для чужих ролей"},
			{ID: "admin_zone", Title: "Админская зона закрыта для клиента и курьера"},
			{ID: "rest_zone", Title: "Ресторанная зона закрыта для чужих ролей"},
			{ID: "admin_passes_courier_zone", Title: "Админский токен пропускается в курьерскую зону"},
		},
	},
	{
		Key:         "negative_sm",
		Title:       "State Machine: запрет нелегальных переходов",
		Description: "Валидация конечного автомата заказов: нелегальные переходы статусов и чужие роли.",
		Category:    "negative",
		Checks: []CheckItem{
			{ID: "illegal_jump_new_delivered", Title: "NEW → DELIVERED запрещён"},
			{ID: "illegal_jump_preparing_delivered", Title: "PREPARING → DELIVERED запрещён"},
			{ID: "unauthorized_role_client", Title: "Клиент не может установить DELIVERED"},
			{ID: "unauthorized_role_restaurant", Title: "Ресторан не может назначать курьера"},
		},
	},
}

// All returns a copy of the full suite catalog in canonical order:
// сценарные сьюты, затем API-проверки (category=api), затем negative_sm.
func All() []Suite {
	out := make([]Suite, len(suites))
	copy(out, suites)
	return out
}

// Get returns the suite by key.
func Get(key string) (Suite, bool) {
	for _, s := range suites {
		if s.Key == key {
			return s, true
		}
	}
	return Suite{}, false
}

// Keys returns all suite keys (the virtual key "all" is not part of the catalog).
func Keys() []string {
	keys := make([]string, 0, len(suites))
	for _, s := range suites {
		keys = append(keys, s.Key)
	}
	return keys
}
