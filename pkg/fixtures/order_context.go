package fixtures

import (
	"context"
	"fmt"

	"locali-e2e-engine/pkg/client"
)

// OrderContext is a stand prepared to the point where an order can actually be
// placed: an open restaurant that accepts delivery, a dish on its menu, a
// client with a saved address, and that dish already in the client's cart.
//
// The restaurant order endpoint assembles the order from the server-side cart
// and carries no line items, so every one of these steps is a precondition
// rather than a convenience.
type OrderContext struct {
	RestID     string
	RestLogin  string
	RestToken  string
	CategoryID string
	DishID     string
	DishPrice  float64

	ClientPhone string
	ClientToken string
	AddressID   string

	City      string
	Latitude  float64
	Longitude float64

	// RestIsBound marks that the restaurant is an operator-picked account
	// rather than a freshly generated one — its menu and schedule are real
	// data, not throwaway fixtures.
	RestIsBound bool
}

// DishFixturePrice is the price given to the fixture dish. Round and well
// above any minimum order amount a stand is likely to configure.
const DishFixturePrice = 500

// PrepareRestaurantOrder builds an OrderContext from scratch: it registers a
// restaurant that accepts delivery, opens it around the clock, gives it a
// category and a dish, registers a client with an address, and puts the dish
// into that client's cart.
//
// Every failure is reported with the step that produced it, because a stand
// can refuse at any of them for reasons that have nothing to do with the test
// (city outside the platform directory, no Locali tariff, pending migrations).
func (fm *FixtureManager) PrepareRestaurantOrder(ctx context.Context) (*OrderContext, error) {
	fm.mu.Lock()
	city, lat, lon := fm.city, fm.latitude, fm.longitude
	fm.mu.Unlock()

	restLogin, restToken, err := fm.CreateUniqueRestaurant(ctx)
	if err != nil {
		return nil, fmt.Errorf("подготовка заказа, шаг «ресторан»: %w", err)
	}

	claims, err := client.DecodeClaims(restToken)
	if err != nil {
		return nil, fmt.Errorf("подготовка заказа: не удалось прочитать rest_id из токена ресторана: %w", err)
	}
	restID := claims.UserID
	if restID == "" {
		return nil, fmt.Errorf("подготовка заказа: токен ресторана не несёт user_id")
	}

	octx := &OrderContext{
		RestID:    restID,
		RestLogin: restLogin,
		RestToken: restToken,
		DishPrice: DishFixturePrice,
		City:      city,
		Latitude:  lat,
		Longitude: lon,
	}

	fm.mu.Lock()
	restIsBound := fm.presetRest != nil
	fm.mu.Unlock()
	octx.RestIsBound = restIsBound

	// A restaurant without a schedule is closed, and a closed restaurant
	// refuses orders with RESTAURANT_CLOSED regardless of anything else.
	//
	// On a bound restaurant this overwrites the operator's own working hours —
	// it is the one mutation the flow cannot avoid, so it is announced rather
	// than done quietly.
	if restIsBound {
		fm.note("[FIXTURE] ресторан привязан: выставляю круглосуточное расписание, чтобы точка была открыта")
	}
	if err := fm.restAPI.UpdateSchedule(ctx, client.AlwaysOpenSchedule()); err != nil {
		return nil, fmt.Errorf("подготовка заказа, шаг «расписание»: %w", err)
	}

	// A bound restaurant usually already has a menu; adding a dish to it on
	// every run would pile up rubbish in somebody's real point.
	if restIsBound {
		if dishes, lerr := fm.restAPI.ListDishes(ctx); lerr == nil {
			for _, d := range dishes {
				if d.DishID != "" && !d.IsHidden && d.Price > 0 {
					octx.DishID = d.DishID
					octx.DishPrice = d.Price
					fm.note("[FIXTURE] ресторан привязан: заказываю существующее блюдо «%s»", d.Title)
					break
				}
			}
		}
	}

	if octx.DishID == "" {
		category, err := fm.restAPI.CreateCategory(ctx, client.CreateCategoryRequest{Title: "Категория E2E"})
		if err != nil {
			return nil, fmt.Errorf("подготовка заказа, шаг «категория»: %w", err)
		}
		octx.CategoryID = category.CategoryID

		dish, err := fm.restAPI.CreateDish(ctx, client.CreateDishRequest{
			Title:      fmt.Sprintf("Блюдо E2E %d", newSuffix()),
			Price:      DishFixturePrice,
			CategoryID: category.CategoryID,
		})
		if err != nil {
			return nil, fmt.Errorf("подготовка заказа, шаг «блюдо»: %w", err)
		}
		octx.DishID = dish.DishID
	}

	clientPhone, clientToken, err := fm.CreateUniqueClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("подготовка заказа, шаг «клиент»: %w", err)
	}
	octx.ClientPhone = clientPhone
	octx.ClientToken = clientToken

	address, err := fm.clientAPI.CreateAddress(ctx, client.CreateAddressRequest{
		FullAddress: fmt.Sprintf("%s, ул. Тестовая, 10", city),
		Latitude:    &lat,
		Longitude:   &lon,
		Apartment:   "42",
		IsDefault:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("подготовка заказа, шаг «адрес клиента»: %w", err)
	}
	octx.AddressID = address.AddressID

	if _, err := fm.clientAPI.AddCartItem(ctx, client.AddCartItemRequest{
		EntityID:   octx.DishID,
		EntityType: client.EntityTypeDish,
		Quantity:   1,
	}); err != nil {
		return nil, fmt.Errorf("подготовка заказа, шаг «корзина»: %w", err)
	}

	return octx, nil
}

// RefillCart puts the fixture dish back into the cart. Placing an order
// consumes the cart, so a second order in the same suite needs this.
func (fm *FixtureManager) RefillCart(ctx context.Context, octx *OrderContext, quantity int) error {
	if octx == nil {
		return fmt.Errorf("refill cart: контекст заказа не подготовлен")
	}
	if quantity <= 0 {
		quantity = 1
	}
	if _, err := fm.clientAPI.AddCartItem(ctx, client.AddCartItemRequest{
		EntityID:   octx.DishID,
		EntityType: client.EntityTypeDish,
		Quantity:   quantity,
	}); err != nil {
		return fmt.Errorf("наполнение корзины: %w", err)
	}
	return nil
}

// OrderRequest returns the request that places a delivery order for this
// context, filled with everything the backend requires.
func (octx *OrderContext) OrderRequest() client.CreateRestaurantOrderRequest {
	return client.CreateRestaurantOrderRequest{
		RestID:        octx.RestID,
		ReceiveMethod: client.ReceiveDelivery,
		AddressID:     octx.AddressID,
		Payment:       client.OrderPayment{Type: client.PaymentCash},
		Source:        client.SourceLokaliEda,
	}
}

// ParcelRequest returns a valid point-A-to-point-B parcel order around the
// configured fixture coordinates.
func (fm *FixtureManager) ParcelRequest(recipientPhone string) client.CreateIndependentOrderRequest {
	fm.mu.Lock()
	lat, lon := fm.latitude, fm.longitude
	fm.mu.Unlock()

	if recipientPhone == "" {
		recipientPhone = newPhone(operatorClient)
	}

	// Roughly a kilometre apart, so the delivery is priced as a real trip
	// rather than a degenerate zero-distance one.
	return client.CreateIndependentOrderRequest{
		FromLat:  lat,
		FromLong: lon,
		ToLat:    lat + 0.01,
		ToLong:   lon + 0.01,
		// CourierType is what lets the backend price the trip; without it (or
		// a deliveryType mapping to a courier group) it answers
		// COURIER_TYPE_UNRESOLVED. A bike covers the ~1 km this request spans.
		CourierType:    client.CourierBike,
		PaymentType:    client.PaymentCash,
		CargoType:      client.CargoDocuments,
		RecipientName:  "Получатель Тестовый",
		RecipientPhone: recipientPhone,
		Comment:        "E2E посылка",
	}
}

// ParcelOrderingAvailable reports whether the platform currently accepts
// parcel orders. Outside the working window every parcel order is refused with
// 422 OUTSIDE_WORKING_HOURS — a closed platform, not a defect, and a suite
// should say so rather than fail.
//
// It needs an authenticated client, so one is created if none is active.
func (fm *FixtureManager) ParcelOrderingAvailable(ctx context.Context) (bool, string, error) {
	if _, _, err := fm.CreateUniqueClient(ctx); err != nil {
		return false, "", fmt.Errorf("проверка окна приёма посылок: %w", err)
	}
	return fm.clientAPI.ParcelAvailability(ctx)
}
