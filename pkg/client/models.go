package client

import (
	"encoding/json"
	"fmt"
	"time"
)

// Auth Tokens response
type AuthTokenResponse struct {
	Token   string `json:"token"`
	Message string `json:"message"`
}

// Client Register & Login
type ClientRegisterRequest struct {
	PhoneNumber string   `json:"phoneNumber"`
	Address     string   `json:"address,omitempty"`
	CityKey     string   `json:"cityKey,omitempty"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
}

type ClientLoginRequest struct {
	PhoneNumber      string `json:"phoneNumber"`
	VerificationCode string `json:"verificationCode"`
	FirstName        string `json:"firstName,omitempty"`
	LastName         string `json:"lastName,omitempty"`
}

// ClientRegisterResponse is the body of POST /api/clients/register.
//
// DebugCode carries the freshly issued OTP and is present only while the
// backend runs with DEBUG=true (dev/test stands, LOCALIAPP-2343). On
// production the field is absent — never rely on it outside a test stand.
type ClientRegisterResponse struct {
	Message   string `json:"message"`
	FirstTime bool   `json:"firstTime"`
	DebugCode string `json:"debugCode,omitempty"`
}

// Client phone-change flow: POST /api/clients/phone-change/*.
// Codes travel by SMS only (no debug channel), so automated coverage is
// limited to the guard paths: no session, wrong stage, duplicate number.
type PhoneChangeVerifyOldRequest struct {
	OldCode string `json:"oldCode"`
}

type PhoneChangeStartNewRequest struct {
	NewPhoneNumber string `json:"newPhoneNumber"`
}

type PhoneChangeVerifyNewRequest struct {
	NewCode string `json:"newCode"`
}

// RestRegisterRequest is POST /api/rests/register.
//
// restName, login, password and city are required by the backend
// (LOCALIAPP-2387); city is additionally resolved through the geocoder and
// must belong to the platform directory (supportedCities of the active Locali
// tariff), otherwise the call is rejected with 400 before any write.
type RestRegisterRequest struct {
	RestName       string `json:"restName"`
	Login          string `json:"login"`
	Password       string `json:"password"`
	Email          string `json:"email,omitempty"`
	PhoneNumber    string `json:"phoneNumber,omitempty"`
	City           string `json:"city"`
	Street         string `json:"street,omitempty"`
	House          string `json:"house,omitempty"`
	DeliveryMethod string `json:"deliveryMethod,omitempty"`
	// DeliveryEnabled / PickupEnabled gate the receive methods a client may
	// pick. A restaurant registered without them refuses every order with
	// RECEIVE_METHOD_DISABLED, so fixtures must set them explicitly.
	DeliveryEnabled bool     `json:"deliveryEnabled,omitempty"`
	PickupEnabled   bool     `json:"pickupEnabled,omitempty"`
	Latitude        *float64 `json:"latitude,omitempty"`
	Longitude       *float64 `json:"longitude,omitempty"`
}

type RestLoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Restaurant password reset: request -> verify -> confirm.
// The OTP is delivered by SMS; verify exchanges it for a 5-minute resetToken
// which confirm consumes.
type RestPasswordResetRequestRequest struct {
	PhoneNumber string `json:"phoneNumber"`
}

type RestPasswordResetVerifyRequest struct {
	PhoneNumber string `json:"phoneNumber"`
	Code        string `json:"code"`
}

type RestPasswordResetVerifyResponse struct {
	ResetToken string `json:"resetToken"`
}

type RestPasswordResetConfirmRequest struct {
	ResetToken  string `json:"resetToken"`
	NewPassword string `json:"newPassword"`
}

// CourierRegisterRequest is POST /api/couriers/register.
//
// The backend reads the family name from `surname` (it maps surname -> lastName
// and patronymic -> middleName internally), so sending only `lastName` creates
// a courier with an empty name. Both are sent: `surname` is what lands in the
// row, `lastName` keeps the embedded mock and older stands working.
type CourierRegisterRequest struct {
	FirstName    string `json:"firstName"`
	Surname      string `json:"surname"`
	LastName     string `json:"lastName,omitempty"`
	Patronymic   string `json:"patronymic,omitempty"`
	MiddleName   string `json:"middleName,omitempty"`
	Gender       string `json:"gender,omitempty"` // "М" | "Ж"
	Email        string `json:"email,omitempty"`
	PhoneNumber  string `json:"phoneNumber"`
	Login        string `json:"login"`
	Password     string `json:"password"`
	DeliveryType string `json:"deliveryType,omitempty"`
	CityKey      string `json:"cityKey,omitempty"`
}

type CourierLoginRequest struct {
	PhoneNumber string `json:"phoneNumber"`
	Password    string `json:"password"`
}

// Admin Login
type AdminLoginRequest struct {
	Login       string `json:"login,omitempty"`
	PhoneNumber string `json:"phoneNumber,omitempty"`
	Password    string `json:"password"`
}

// Receive methods accepted by POST /api/clients/create-order. Each one is
// gated by its own restaurant flag (deliveryEnabled, pickupEnabled,
// tableEnabled, terminalEnabled) — a disabled method is a 400, not a 403.
const (
	ReceiveDelivery = "delivery"
	ReceivePickup   = "pickup"
	ReceiveTakeout  = "takeout"
	ReceiveDineIn   = "dinein"
	ReceiveTable    = "table"
	ReceiveTerminal = "terminal"
)

// Payment types accepted across order endpoints (validators/common: PAYMENT_TYPES).
const (
	PaymentCash   = "cash"
	PaymentOnline = "online"
)

// Order sources the backend recognises. An unknown value is rejected; omitting
// it is allowed and the backend substitutes its default.
const (
	SourceLokaliEda = "lokali_eda"
	SourceCrispyApp = "crispy_app"
)

// Courier types for parcel orders — utilities/courierPricing.js. One of these
// (or a deliveryType that maps to a courier group) is required: without it the
// backend cannot price the trip and answers COURIER_TYPE_UNRESOLVED.
const (
	CourierBike  = "bike"
	CourierCar   = "car"
	CourierTruck = "truck"
)

// Cargo types for parcel (independent) orders — utilities/courierPricing.js.
const (
	CargoDocuments    = "documents"
	CargoFood         = "food"
	CargoClothes      = "clothes"
	CargoFragile      = "fragile"
	CargoGoods        = "goods"
	CargoPersonalItem = "personal_items"
	CargoOther        = "other"
)

// OrderPayment is the payment block of a restaurant order. It is an object,
// not a string: the backend validates the type and, for cash, the change
// amount against the order total.
type OrderPayment struct {
	Type string `json:"type"` // cash | online
	// ChangeAmount is digits only, up to 6 characters, and must not be less
	// than the order total. Only meaningful for cash.
	ChangeAmount string `json:"changeAmount,omitempty"`
}

// CreateRestaurantOrderRequest is POST /api/clients/create-order.
//
// The order is assembled from the client's server-side cart — the request
// carries no line items at all. Fill the cart with POST /api/cart/items first,
// then place the order against the same rest_id.
type CreateRestaurantOrderRequest struct {
	RestID    string `json:"rest_id,omitempty"`
	SettingID string `json:"setting_id,omitempty"`
	// ReceiveMethod is required; see the Receive* constants.
	ReceiveMethod string `json:"receiveMethod"`
	// AddressID is required for delivery; falls back to the cart group's
	// selected address when omitted.
	AddressID    string       `json:"address_id,omitempty"`
	Payment      OrderPayment `json:"payment"`
	Comment      string       `json:"comment,omitempty"`
	DeliveryTime string       `json:"deliveryTime,omitempty"`
	PromoCode    string       `json:"promoCode,omitempty"`
	Source       string       `json:"source,omitempty"`
}

// CreateIndependentOrderRequest is POST /api/clients/independent-order — a
// parcel from point A to point B, addressed by coordinates rather than text.
type CreateIndependentOrderRequest struct {
	FromLat  float64 `json:"fromLat"`
	FromLong float64 `json:"fromLong"`
	ToLat    float64 `json:"toLat"`
	ToLong   float64 `json:"toLong"`

	PaymentType string `json:"paymentType"` // cash | online
	// CargoType is required; see the Cargo* constants.
	CargoType string `json:"cargoType"`
	// RecipientName: letters, spaces and hyphens, up to 30 characters.
	RecipientName string `json:"recipientName"`
	// RecipientPhone must be a strict +7XXXXXXXXXX number.
	RecipientPhone    string `json:"recipientPhone"`
	SenderPhoneNumber string `json:"senderPhoneNumber,omitempty"`
	Comment           string `json:"comment,omitempty"`
	CourierType       string `json:"courierType,omitempty"`
	DeliveryType      string `json:"deliveryType,omitempty"`
	// When is a pickup time in HH:mm.
	When string `json:"when,omitempty"`
	// ChangeAmount applies to cash orders and must be greater than zero.
	ChangeAmount float64 `json:"change_amount,omitempty"`
}

// CourierTypeAvailability is one entry of GET /api/clients/courier-types.
// Parcel delivery runs on a platform-wide working window; outside it every
// parcel order is refused with 422 OUTSIDE_WORKING_HOURS, which is a closed
// platform rather than a broken flow.
type CourierTypeAvailability struct {
	IsAvailable bool   `json:"is_available"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
}

// Cart: the restaurant order is built from it, so a flow that places an order
// has to fill it first.
type AddCartItemRequest struct {
	EntityID   string `json:"entity_id"`
	EntityType string `json:"entity_type"` // dish
	Quantity   int    `json:"quantity"`
	Comment    string `json:"comment,omitempty"`
}

type AddCartItemResponse struct {
	Item struct {
		CartItemID string  `json:"cartItemId"`
		EntityID   string  `json:"entityId"`
		EntityType string  `json:"entityType"`
		ItemPrice  float64 `json:"itemPrice"`
		Quantity   int     `json:"quantity"`
	} `json:"item"`
	Message string `json:"message"`
}

// EntityTypeDish is the only cart entity the order flows use.
const EntityTypeDish = "dish"

// Client addresses: a delivery order needs an address_id, not a free-text
// address.
type CreateAddressRequest struct {
	FullAddress string   `json:"fullAddress"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	Apartment   string   `json:"apartment,omitempty"`
	Entrance    string   `json:"entrance,omitempty"`
	Floor       string   `json:"floor,omitempty"`
	Intercom    string   `json:"intercom,omitempty"`
	Comment     string   `json:"comment,omitempty"`
	Tag         string   `json:"tag,omitempty"`
	IsDefault   bool     `json:"isDefault,omitempty"`
}

type AddressResponse struct {
	AddressID   string  `json:"address_id"`
	ClientID    string  `json:"client_id"`
	FullAddress string  `json:"fullAddress"`
	City        string  `json:"city"`
	Street      string  `json:"street"`
	House       string  `json:"house"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	IsDefault   bool    `json:"isDefault"`
	// Code is set when the backend answers 409 ADDRESS_ALREADY_EXISTS, in
	// which case AddressID still points at the existing address.
	Code string `json:"code,omitempty"`
}

// Order Status & Assignment Requests
type AssignCourierRequest struct {
	OrderID   string `json:"order_id"`
	CourierID string `json:"courier_id"`
}

type ChangeRestOrderStatusRequest struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"` // "cooking", "cooked", "shipping", "complete", "cancelled"
}

type CourierTakeOrderRequest struct {
	OrderID   string `json:"order_id"`
	CourierID string `json:"courier_id"`
}

type CourierChangeStatusRequest struct {
	OrderID       string `json:"order_id"`
	Status        string `json:"status"` // "going", "shipping", "complete"
	IsIndependent bool   `json:"isIndependent,omitempty"`
}

// OrderResponse is an order as the backend returns it.
//
// Field naming is not consistent across endpoints: creation answers in
// snake_case (order_id, courier_id) while the read endpoints run the row
// through toCamelCase (orderId, courierId). UnmarshalJSON accepts both so a
// caller never has to know which endpoint produced the value.
type OrderResponse struct {
	OrderID          string    `json:"order_id"`
	OrderNumber      int       `json:"orderNumber"`
	Status           string    `json:"status"`
	CourierStatus    string    `json:"courierStatus"`
	ClientID         string    `json:"clientId"`
	RestID           string    `json:"restId"`
	CourierID        string    `json:"courier_id"`
	CourierName      string    `json:"courierName"`
	Price            float64   `json:"price"`
	DeliveryAddress  string    `json:"deliveryAddress"`
	AddressA         string    `json:"addressA,omitempty"`
	AddressB         string    `json:"addressB,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// CreateDishRequest is POST /api/rests/dishes. The backend names the field
// `title` (not `name`) and takes the owning restaurant from the token, so
// rest_id is never sent. CategoryID is required by the validator.
type CreateDishRequest struct {
	Title       string  `json:"title"`
	Price       float64 `json:"price"`
	CategoryID  string  `json:"category_id"`
	Description string  `json:"description,omitempty"`
	WeightGrams int     `json:"weightGrams,omitempty"`
}

type DishResponse struct {
	DishID      string             `json:"dish_id"`
	Title       string             `json:"title"`
	Price       float64            `json:"price"`
	RestID      string             `json:"restId"`
	IsHidden    bool               `json:"isHidden"`
	OnSell      bool               `json:"on_sell"`
	Categories  []CategoryResponse `json:"categories,omitempty"`
	Description string             `json:"description,omitempty"`
}

// CreateCategoryRequest is POST /api/rests/categories. The endpoint also
// accepts an array for batch creation; a single object is enough here.
type CreateCategoryRequest struct {
	Title string `json:"title"`
	Queue int    `json:"queue,omitempty"`
}

type CategoryResponse struct {
	CategoryID string `json:"category_id"`
	RestID     string `json:"rest_id"`
	Title      string `json:"title"`
	OnSell     bool   `json:"on_sell"`
}

// Schedule: a restaurant with no schedule is closed, and a closed restaurant
// refuses orders with RESTAURANT_CLOSED.
type ScheduleDay struct {
	Day       string `json:"day"` // "1".."7"
	From      string `json:"from"`
	To        string `json:"to"`
	BreakFrom string `json:"breakFrom,omitempty"`
	BreakTo   string `json:"breakTo,omitempty"`
}

type UpdateScheduleRequest struct {
	Days []ScheduleDay `json:"days"`
}

// AlwaysOpenSchedule returns a 7-day schedule covering the whole day, so a
// fixture restaurant is never closed when a suite runs.
func AlwaysOpenSchedule() UpdateScheduleRequest {
	days := make([]ScheduleDay, 0, 7)
	for d := 1; d <= 7; d++ {
		days = append(days, ScheduleDay{Day: fmt.Sprintf("%d", d), From: "00:00", To: "23:59"})
	}
	return UpdateScheduleRequest{Days: days}
}

// UnmarshalJSON decodes an order written in either naming convention.
func (o *OrderResponse) UnmarshalJSON(data []byte) error {
	type alias OrderResponse // avoids recursing into this method
	var base alias
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	*o = OrderResponse(base)

	var camel struct {
		OrderID   string `json:"orderId"`
		CourierID string `json:"courierId"`
		ClientID  string `json:"clientId"`
		RestID    string `json:"restId"`
	}
	if err := json.Unmarshal(data, &camel); err != nil {
		// A payload that decoded as an order but not as these four aliases is
		// still usable — the snake_case pass already filled what it could.
		return nil
	}
	if o.OrderID == "" {
		o.OrderID = camel.OrderID
	}
	if o.CourierID == "" {
		o.CourierID = camel.CourierID
	}
	if o.ClientID == "" {
		o.ClientID = camel.ClientID
	}
	if o.RestID == "" {
		o.RestID = camel.RestID
	}
	return nil
}
