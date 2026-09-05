package analysis

import (
	"fmt"
	"strings"

	"locali-e2e-engine/pkg/runner"
)

// Diagnostic categories for test failures
const (
	CatHealthy             = "HEALTHY"
	CatSkipped             = "SKIPPED"
	CatRateLimit           = "RATE_LIMIT_COOLDOWN"
	CatAuthExpired         = "AUTH_EXPIRED"
	CatRBACForbidden       = "RBAC_FORBIDDEN"
	CatStateConflict       = "STATE_CONFLICT"
	CatOutsideHours        = "OUTSIDE_WORKING_HOURS"
	CatPreconditionFailed  = "PRECONDITION_FAILED"
	CatValidationError     = "VALIDATION_ERROR"
	CatServerPanic         = "SERVER_PANIC"
	CatNetworkTimeout      = "NETWORK_TIMEOUT"
	CatAssertionFailed     = "ASSERTION_FAILED"
	CatUnknown             = "UNKNOWN_FAILURE"
)

// Severity levels
const (
	SeverityNone     = "NONE"
	SeverityLow      = "LOW"
	SeverityMedium   = "MEDIUM"
	SeverityHigh     = "HIGH"
	SeverityCritical = "CRITICAL"
)

// Diagnosis represents an automated root cause failure analysis.
type Diagnosis struct {
	RunID          string `json:"runId"`
	SuiteKey       string `json:"suiteKey"`
	SuiteName      string `json:"suiteName"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	RootCause      string `json:"rootCause"`
	Recommendation string `json:"recommendation"`
	FailedStep     string `json:"failedStep,omitempty"`
	FailedCheck    string `json:"failedCheck,omitempty"`
	HTTPStatus     int    `json:"httpStatus,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	ErrorDetail    string `json:"errorDetail,omitempty"`
}

// DiagnoseRun performs deep root cause analysis on a completed test run.
func DiagnoseRun(run *runner.TestRun) *Diagnosis {
	if run == nil {
		return &Diagnosis{
			Category:       CatHealthy,
			Severity:       SeverityNone,
			Title:          "Нет данных прогона",
			Description:    "Прогон отсутствует или не инициализирован.",
			Recommendation: "Запустите тесты для формирования диагностического отчёта.",
		}
	}

	diag := &Diagnosis{
		RunID:     run.ID,
		SuiteKey:  run.SuiteKey,
		SuiteName: run.SuiteName,
	}

	if run.Status == runner.RunPassed {
		diag.Category = CatHealthy
		diag.Severity = SeverityNone
		diag.Title = "Все проверки успешно пройдены"
		diag.Description = fmt.Sprintf("Сьют «%s» выполнен штатно. Пройдено проверок: %d/%d.", run.SuiteName, run.PassedChecks, run.TotalChecks)
		diag.RootCause = "Ошибок не обнаружено. Поведение сервиса полностью соответствует контракту."
		diag.Recommendation = "Вмешательство не требуется. Контракт API соблюдён."
		return diag
	}

	if run.Status == runner.RunSkipped {
		diag.Category = CatSkipped
		diag.Severity = SeverityLow
		diag.Title = "Прогон пропущен (SKIPPED)"
		diag.Description = fmt.Sprintf("Часть проверок в «%s» была пропущена из-за внешних предусловий.", run.SuiteName)
		diag.RootCause = "Предусловия теста не выполнены (например, закрытая платформа или пропущенный шаг)."
		diag.Recommendation = "Проверьте предусловия стенда (рабочее время, активные фикстуры)."
		return diag
	}

	// For FAILED runs, find the failing check, step, and error details
	var failedCheckID string
	var failedCheckMsg string
	for checkID, res := range run.Results {
		if res.Status == runner.CheckFailed {
			failedCheckID = checkID
			failedCheckMsg = res.Message
			break
		}
	}

	var lastHTTPStatus int
	var lastHTTPEndpoint string
	var lastEventMsg string
	var stepName string

	for i := len(run.Events) - 1; i >= 0; i-- {
		ev := run.Events[i]
		if ev.StepType == "CHECK_FAILED" || ev.Level == runner.LogError {
			if stepName == "" {
				stepName = ev.StepName
			}
			if lastEventMsg == "" {
				lastEventMsg = ev.Message
			}
		}
		if ev.HTTPDetails != nil {
			if lastHTTPStatus == 0 {
				lastHTTPStatus = ev.HTTPDetails.StatusCode
			}
			if lastHTTPEndpoint == "" {
				lastHTTPEndpoint = fmt.Sprintf("%s %s", ev.HTTPDetails.Method, ev.HTTPDetails.URL)
			}
		}
	}

	diag.FailedCheck = failedCheckID
	diag.FailedStep = stepName
	diag.HTTPStatus = lastHTTPStatus
	diag.Endpoint = lastHTTPEndpoint

	errorText := strings.TrimSpace(run.Error)
	if (errorText == "" || strings.Contains(errorText, "проверок провалено")) && failedCheckMsg != "" {
		errorText = failedCheckMsg
	}
	if errorText == "" {
		errorText = lastEventMsg
	}
	diag.ErrorDetail = errorText

	lowerErr := strings.ToLower(errorText)

	// Rule 1: Rate limiting / OTP attempts
	if lastHTTPStatus == 429 || strings.Contains(lowerErr, "429") ||
		strings.Contains(lowerErr, "rate limit") || strings.Contains(lowerErr, "too many") ||
		strings.Contains(lowerErr, "retry-after") || strings.Contains(lowerErr, "бюджет попыток") ||
		strings.Contains(lowerErr, "внутри 60 секунд") {
		diag.Category = CatRateLimit
		diag.Severity = SeverityHigh
		diag.Title = "Превышен лимит запросов (HTTP 429 Too Many Requests)"
		diag.Description = "Бэкенд заблокировал запрос из-за превышения частоты вызовов (исчерпан бюджет OTP-попыток или повторная отправка внутри 60 секунд)."
		diag.RootCause = "Стенд наложил временное ограничение по политике защиты от перебора кодов или спама."
		diag.Recommendation = "Подождите 60 секунд перед повторным запуском либо используйте привязанный аккаунт во вкладке «Хранилище»."
		return diag
	}

	// Rule 2: Outside working hours
	if strings.Contains(lowerErr, "outside_working_hours") || strings.Contains(lowerErr, "рабочее окно") ||
		strings.Contains(lowerErr, "10:00") || strings.Contains(lowerErr, "23:00") {
		diag.Category = CatOutsideHours
		diag.Severity = SeverityMedium
		diag.Title = "Заказ вне рабочих часов сервиса (HTTP 422 Outside Working Hours)"
		diag.Description = "Платформа Locali принимает отправку посылок строго в интервале с 10:00 до 23:00."
		diag.RootCause = "Тест доставки посылок запущен вне операционного окна сервиса."
		diag.Recommendation = "Запуск посылочных тестов (Flow B) поддерживается в рабочее время платформы либо на стенде с отключенной проверкой времени."
		return diag
	}

	// Rule 3: Auth missing / invalid token
	if lastHTTPStatus == 401 || strings.Contains(lowerErr, "401") ||
		strings.Contains(lowerErr, "no_token_included") || strings.Contains(lowerErr, "invalid_token") ||
		strings.Contains(lowerErr, "unauthorized") || strings.Contains(lowerErr, "токен не найден") ||
		strings.Contains(lowerErr, "битый токен") {
		diag.Category = CatAuthExpired
		diag.Severity = SeverityHigh
		diag.Title = "Ошибка аутентификации (HTTP 401 Unauthorized)"
		diag.Description = "Запрос отклонён сервером из-за отсутствия, порчи или истечения срока действия Bearer JWT токена."
		diag.RootCause = "Токен сессии для роли не установлен или был отозван бэкендом стенда."
		diag.Recommendation = "Перейдите во вкладку «Токены и доступ» и сгенерируйте/обновите токены для роли через быстрое действие «Логин через API»."
		return diag
	}

	// Rule 4a: RBAC Security Leak (CRITICAL: role check was bypassed!)
	if strings.Contains(lowerErr, "expected 403") || strings.Contains(lowerErr, "прошёл в /api/rests") ||
		strings.Contains(lowerErr, "создал модификатор ресторана") || strings.Contains(lowerErr, "request succeeded (status 20") {
		diag.Category = CatRBACForbidden
		diag.Severity = SeverityCritical
		diag.Title = "Уязвимость безопасности: отсутствие проверки ролей (RBAC Leak)"
		diag.Description = "Бэкенд успешно выполнил запрос к ресторанному API по чужому токену (клиента или курьера) вместо отказа HTTP 403 Forbidden."
		diag.RootCause = "На обработчиках управления меню/модификаторами ресторана на бэкенде отсутствует middleware проверки роли (isRestaurant)."
		diag.Recommendation = "Критический дефект бэкенда: добавьте обязательную проверку роли (isRestaurant) на эндпоинты POST /api/rests/dishes и POST /api/rests/modificators."
		return diag
	}

	// Rule 4b: RBAC Forbidden
	if lastHTTPStatus == 403 || strings.Contains(lowerErr, "403") ||
		strings.Contains(lowerErr, "courier_only") || strings.Contains(lowerErr, "forbidden") ||
		strings.Contains(lowerErr, "ролевой гейт") || strings.Contains(lowerErr, "чужой набор claims") {
		diag.Category = CatRBACForbidden
		diag.Severity = SeverityHigh
		diag.Title = "Отказ в доступе по ролевой политике (HTTP 403 Forbidden)"
		diag.Description = "Вызов эндпоинта заблокирован системой разграничения прав доступа (RBAC)."
		diag.RootCause = "Токен роли не содержит требуемых claims (например, у курьера отсутствует флаг isCourier или клиент обращается к /admin)."
		diag.Recommendation = "Убедитесь, что запрос отправляется от имени корректной роли, и проверьте claims токена в отчёте сьюта auth_otp."
		return diag
	}

	// Rule 5: State Machine Conflict
	if lastHTTPStatus == 409 || strings.Contains(lowerErr, "409") ||
		strings.Contains(lowerErr, "cancel_not_allowed") || strings.Contains(lowerErr, "illegal") ||
		strings.Contains(lowerErr, "state machine") || strings.Contains(lowerErr, "терминальное состояние") {
		diag.Category = CatStateConflict
		diag.Severity = SeverityHigh
		diag.Title = "Конфликт жизненного цикла заказа (HTTP 409 Conflict)"
		diag.Description = "Действие нарушает правила конечного автомата (State Machine) заказа Locali."
		diag.RootCause = "Попытка перевода заказа в недопустимый статус (например, отмена во время готовки или изменение после DELIVERED)."
		diag.Recommendation = "Проверьте последовательность статусов: переход допустим только по легальной цепочке NEW -> COURIER_ASSIGNED -> PREPARING -> READY_FOR_PICKUP -> PICKED_UP -> DELIVERED."
		return diag
	}

	// Rule 6a: Delivery Address Unavailable (distance / zones / city mismatch)
	if strings.Contains(lowerErr, "delivery_address_unavailable") || strings.Contains(lowerErr, "доставка по вашему адресу от этого ресторана недоступна") {
		diag.Category = CatPreconditionFailed
		diag.Severity = SeverityHigh
		diag.Title = "Адрес доставки недоступен для ресторана (DELIVERY_ADDRESS_UNAVAILABLE)"
		diag.Description = "Бэкенд отклонил создание заказа: адрес клиента не входит в зону доставки ресторана либо произошла конкурентная смена сессии при параллельном запуске."
		diag.RootCause = "У ресторана не настроен полигон order_region, либо координаты адреса клиента и ресторана не попадают в один город (isSameCity вернул false)."
		diag.Recommendation = "Убедитесь в совпадении города (FIXTURE_CITY) у ресторана и адреса клиента, либо используйте привязанный аккаунт ресторана с настроенными зонами доставки во вкладке «Токены и доступ»."
		return diag
	}

	// Rule 6b: Idempotency Cart Loss (GROUP_NOT_FOUND on repeat)
	if strings.Contains(lowerErr, "group_not_found") || strings.Contains(lowerErr, "в корзине нет товаров от этого ресторана") {
		diag.Category = CatAssertionFailed
		diag.Severity = SeverityHigh
		diag.Title = "Отсутствует кэширование идемпотентности (Idempotency-Key)"
		diag.Description = "Повторный запрос с тем же ключом идемпотентности не вернул сохранённый заказ, а попытался пересоздать его из пустой корзины (404 GROUP_NOT_FOUND)."
		diag.RootCause = "Бэкенд стенда не сохраняет результат первого заказа в Redis под ключом idem:order, поэтому повторный запрос повторно обращается к корзине."
		diag.Recommendation = "Проверьте сервис идемпотентности на бэкенде: при совпадении Idempotency-Key сервер обязан возвращать HTTP 200 с телом созданного ранее заказа."
		return diag
	}

	// Rule 6c: Preconditions failed (Cart empty, Restaurant closed, Tariff not configured)
	if strings.Contains(lowerErr, "cart_empty") || strings.Contains(lowerErr, "restaurant_closed") ||
		strings.Contains(lowerErr, "locali_delivery_tariff_not_configured") ||
		strings.Contains(lowerErr, "receive_method_disabled") ||
		strings.Contains(lowerErr, "delivery_address_required") {
		diag.Category = CatPreconditionFailed
		diag.Severity = SeverityHigh
		diag.Title = "Не подготовлены предусловия для заказа"
		diag.Description = "Бэкенд отклонил создание заказа из-за отсутствия обязательных сущностей (пустая корзина, закрытое заведение или ненастроенный тариф)."
		diag.RootCause = "Для оформления заказа ресторан должен иметь круглосуточное расписание, блюда в меню, настроенный тариф доставки в городе и наполненную корзину."
		diag.Recommendation = "Используйте встроенную подготовку фикстур Fixtures.PrepareRestaurantOrder или настройте тариф доставки через админ-панель стенда."
		return diag
	}

	// Rule 7: Server Panic / 5xx
	if lastHTTPStatus >= 500 || strings.Contains(lowerErr, "500") || strings.Contains(lowerErr, "502") ||
		strings.Contains(lowerErr, "503") || strings.Contains(lowerErr, "internal server error") ||
		strings.Contains(lowerErr, "panic") {
		diag.Category = CatServerPanic
		diag.Severity = SeverityCritical
		diag.Title = "Внутренний сбой сервера стенда (HTTP 5xx Server Error)"
		diag.Description = "Сервис стенда аварийно завершил обработку запроса (паника, сбой базы данных или исключение в коде микросервиса)."
		diag.RootCause = "Необработанная ошибка на стороне бэкенда при обращении к эндпоинту."
		diag.Recommendation = "Проверьте логи контейнера бэкенда (`docker logs`) стенда и обратитесь к команде бэкенд-разработки."
		return diag
	}

	// Rule 8: Network Timeout
	if strings.Contains(lowerErr, "timeout") || strings.Contains(lowerErr, "connection refused") ||
		strings.Contains(lowerErr, "context deadline exceeded") || strings.Contains(lowerErr, "no such host") {
		diag.Category = CatNetworkTimeout
		diag.Severity = SeverityHigh
		diag.Title = "Сетевой таймаут или недоступность стенда"
		diag.Description = "Движок не смог установить HTTP-соединение с адресом стенда."
		diag.RootCause = "Хост стенда недоступен, порт закрыт или бэкенд перегружен."
		diag.Recommendation = "Проверьте доступность URL стенда в шапке панели и корректность переменной BASE_URL."
		return diag
	}

	// Rule 9: Validation error
	if lastHTTPStatus == 400 || lastHTTPStatus == 422 || strings.Contains(lowerErr, "validation") ||
		strings.Contains(lowerErr, "required") || strings.Contains(lowerErr, "отклоняется") {
		diag.Category = CatValidationError
		diag.Severity = SeverityMedium
		diag.Title = "Ошибка валидации контракта API (HTTP 400/422)"
		diag.Description = "Бэкенд вернул ошибку валидации входных параметров или схемы тела запроса."
		diag.RootCause = "Один или несколько параметров запроса не прошли серверную валидацию (неверный формат телефона, отрицательная цена, пустые обязательные поля)."
		diag.Recommendation = "Сверьте формат передаваемого JSON со спецификацией OpenAPI стенда."
		return diag
	}

	// Rule 10: Contract Assertion Failure
	if strings.Contains(lowerErr, "ожидался статус") || strings.Contains(lowerErr, "assert") ||
		strings.Contains(lowerErr, "не найден в ответе") || strings.Contains(lowerErr, "ожидалось") {
		diag.Category = CatAssertionFailed
		diag.Severity = SeverityMedium
		diag.Title = "Нарушение контрактных ожиданий (Assertion Failed)"
		diag.Description = "Фактический ответ бэкенда не совпал с утверждением (assert), заданным в сценарии."
		diag.RootCause = "Тело ответа или статус-код отличается от зафиксированного в тесте эталона."
		diag.Recommendation = "Ознакомьтесь с телом ответа в журнале событий и обновите проверку либо исправьте регрессию в бэкенде."
		return diag
	}

	// Fallback: Unknown
	diag.Category = CatUnknown
	diag.Severity = SeverityMedium
	diag.Title = "Сбой выполнения сценария"
	diag.Description = fmt.Sprintf("Шаг завершился с ошибкой: %s", errorText)
	diag.RootCause = "Причина сбоя требует дополнительного анализа журнала событий."
	diag.Recommendation = "Изучите детальный лог шагов и сетевые дампы запросов/ответов в окне «Отчёт по прогону»."
	return diag
}
