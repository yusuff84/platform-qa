# Аудит дефектов и уязвимостей платформы Locali (QA & Security Findings)

Документ сформирован на основе результатов автоматизированных End-to-End и API сьютов тестирования на стенде `dev` (`https://r1.loca-li.com/`) и анализа кодовой базы бэкенда (`locali-app-backend`).

---

## Сводная таблица дефектов

| ID | Важность | Компонент / Эндпоинт | Категория | Статус |
|---|---|---|---|---|
| **BUG-01** | **CRITICAL (P0)** | `POST /api/rests/dishes` | Security / RBAC Leak | Открыт |
| **BUG-02** | **CRITICAL (P0)** | `POST /api/rests/modificators` | Security / RBAC Leak | Открыт |
| **BUG-03** | **HIGH (P1)** | `POST /api/couriers/change-status` | Reliability / 500 Panic | Открыт |
| **BUG-04** | **HIGH (P1)** | `POST /api/clients/create-order` | Idempotency / Data Loss | Открыт |
| **BUG-05** | **MEDIUM (P2)** | `POST /api/clients/create-order` | Business Logic / Geofencing | Открыт |
| **BUG-06** | **LOW (P3)** | `POST /api/clients/independent-order` | Configuration / Working Hours | Открыт |

---

## Детальное описание дефектов

### BUG-01: Несанкционированное создание блюда чужими ролями (RBAC Leak)
- **Уязвимость:** Межролевая эскалация привилегий (Broken Access Control).
- **Эндпоинт:** `POST /api/rests/dishes`
- **Обнаружено сьютами:** `security_rbac` (проверка `courier_rest_403`), `api_guards` (проверка `rest_zone`).
- **Симптом:** Курьерский или клиентский Bearer JWT токен успешно создаёт блюдо ресторана. Сервер возвращает `201 Created` с созданным объектом блюда вместо обязательного `403 Forbidden`.
- **Шаги воспроизведения:**
  ```bash
  # Токен курьера или клиента
  curl -X POST "https://r1.loca-li.com/api/rests/dishes" \
    -H "Authorization: Bearer $COURIER_OR_CLIENT_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"title":"Тестовое блюдо","price":500,"category_id":"<uuid>"}'
  # Ожидаемый ответ: HTTP 403 Forbidden
  # Фактический ответ: HTTP 201 Created
  ```
- **Первопричина в коде бэкенда:**
  В файле `routes/rests.js` маршрут `/dishes` подключён без ролевого middleware `restaurantOnly`. Токен проходит базовую валидацию подписи JWT (`authMiddleware`), но не проверяется на наличие claim `isRestaurant === true`.
- **Рекомендация по исправлению:**
  В `routes/rests.js` обернуть обработчик в проверку прав:
  ```javascript
  router.post('/dishes', restaurantOnly, RestController.createDish);
  ```

---

### BUG-02: Несанкционированное создание модификаторов ресторана клиентом (RBAC Leak)
- **Уязвимость:** Broken Access Control в ресторанном модуле.
- **Эндпоинт:** `POST /api/rests/modificators`
- **Обнаружено сьютом:** `api_modifiers` (проверка `modifier_foreign_access`).
- **Симптом:** Клиентский токен может создавать пачки модификаторов к меню ресторана. Сервер возвращает `200/201` вместо `403 Forbidden`.
- **Первопричина:**
  В `controllers/RestController.js` и `routes/rests.js` метод создания модификаторов не проверяет `isRestaurant` и не валидирует принадлежность `dishId` ресторану из токена.
- **Рекомендация по исправлению:**
  Назначить ролевой гард `restaurantOnly` и убедиться, что создаваемые модификаторы привязаны к ресторану текущей сессии: `req.user_id === dish.rest_id`.

---

### BUG-03: Аварийное падение бэкенда (500 Panic) при смене статуса непринятого заказа курьером
- **Тип дефекта:** Необработанное исключение (Unhandled Exception / Server Error).
- **Эндпоинт:** `POST /api/couriers/change-status`
- **Обнаружено сьютом:** `api_order_status` (проверка `courier_status_needs_assignment`).
- **Симптом:** Если курьер отправляет смену статуса (`status: "going"`) для заказа, который ему не назначен или не существует, сервер падает с `HTTP 500 Internal Server Error`:
  ```json
  {
    "code": 500,
    "error": "Не удалось изменить статус заказа",
    "message": "Не удалось изменить статус заказа"
  }
  ```
- **Ожидаемое поведение:** контролируемый ответ со статусом `400 Bad Request`, `403 Forbidden` или `409 Conflict` (`ORDER_NOT_ASSIGNED_TO_COURIER`).
- **Первопричина в коде бэкенда:**
  В сервисе смены статуса курьера отсутствует предварительная проверка:
  ```javascript
  if (!order || order.courier_id !== courier_id) {
    return res.status(403).json({ error: 'ORDER_NOT_ASSIGNED', message: 'Заказ не назначен этому курьеру' });
  }
  ```
  Запрос падает в нижних слоях Sequelize транзакции и всплывает как необработанная 500 ошибка.
- **Рекомендация по исправлению:**
  Добавить guard clause перед транзакцией смены статуса.

---

### BUG-04: Сбой идемпотентности заказов (404 GROUP_NOT_FOUND при повторном вызове)
- **Тип дефекта:** Нарушение контракта идемпотентности API (RFC 7396 / Idempotency-Key).
- **Эндпоинт:** `POST /api/clients/create-order` с заголовком `Idempotency-Key`
- **Обнаружено сьютом:** `idempotency` (проверка `same_key_same_order`).
- **Симптом:**
  1. Клиент делает первый запрос с заголовком `Idempotency-Key: <UUID>` → заказ успешно создаётся, корзина на сервере очищается.
  2. Клиент повторяет ровно тот же запрос с тем же `Idempotency-Key: <UUID>` (эмуляция сбоя сети / retry).
  3. Сервер возвращает `404 GROUP_NOT_FOUND: В корзине нет товаров от этого ресторана` вместо сохранённого ответа первого заказа.
- **Первопричина в коде бэкенда:**
  Сервис `idempotency.service.js` опирается на Redis ключ `idem:order:${clientId}:${key}`.
  На стенде либо отключён Redis, либо при возникновении ошибок ключ удаляется через `clearIdempotency`, либо `finishIdempotency` не успевает зафиксировать статус `done`. В результате второй запрос выполняет создание заново и падает на пустой корзине.
- **Рекомендация по исправлению:**
  Убедиться, что `finishIdempotency` всегда сохраняет снапшот созданного заказа в Redis с TTL не менее 600 секунд, а `ClientController.js` возвращает закешированный ответ ДО любых проверок корзины (`buildCheckoutContext`).

---

### BUG-05: Ложное срабатывание отказа доставки (400 DELIVERY_ADDRESS_UNAVAILABLE)
- **Тип дефекта:** Логическая коллизия геозоны доставки при регистрации ресторана без полигонов.
- **Эндпоинт:** `POST /api/clients/create-order`
- **Обнаружено сьютами:** `flow_a`, `cancellation`.
- **Симптом:** При оформлении заказа в свежесозданный ресторан бэкенд возвращает:
  `400 DELIVERY_ADDRESS_UNAVAILABLE: Доставка по вашему адресу от этого ресторана недоступна`.
- **Первопричина в коде бэкенда:**
  В `services/order/createOrder.service.js`:
  ```javascript
  if (receiveMethod === 'delivery' && checkout.addressAvailability) {
    if (checkout.addressAvailability.isAvailable === false) {
      return { error: { error: CartErrorCodes.DELIVERY_ADDRESS_UNAVAILABLE, ... } };
    }
  }
  ```
  В `checkout.service.js` строка 80:
  `isAvailable = inArea === true || (inArea === null && sameCity)`
  Если у ресторана нет полигонов доставки (`order_region == null`), `isInDeliveryArea` возвращает `null`. Тогда доступность зависит строго от функции `isSameCity`. Если в таблице `client_addresses` город был сохранён в другом регистре или геокодер Яндекса вернул префикс «городской округ», `isSameCity` возвращает `false`, и заказ отклоняется.
- **Рекомендация по исправлению:**
  На бэкенде унифицировать нормализацию `cityKey` в `cityHelpers.js` с учётом синонимов геокодера («г. Грозный», «городской округ Грозный», «Грозный»).
  В движке тестирования: для стабильных заказов привязывать подготовленный стендовый ресторан с преднастроенными зонами доставки во вкладке «Хранилище».

---

### BUG-06: Жёсткое ограничение рабочего окна посылок без возможности отключения
- **Тип дефекта:** Эксплуатационное ограничение тестовых стендов.
- **Эндпоинт:** `POST /api/clients/independent-order`
- **Обнаружено сьютом:** `flow_b`.
- **Симптом:** Вне интервала 10:00–23:00 сервис блокирует заказы посылок с `422 OUTSIDE_WORKING_HOURS`. На dev/test стенде это блокирует ночные прогоны автотестов в CI/CD.
- **Рекомендация по исправлению:**
  Внедрить на бэкенде флаг конфигурации `IGNORE_WORKING_HOURS=true` для тестовых стендов (`DEBUG=true`).
