package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// ClientAPI provides methods for Client App interaction
type ClientAPI struct {
	sessionMgr *SessionManager
}

// NewClientAPI creates a new Client API service
func NewClientAPI(sm *SessionManager) *ClientAPI {
	return &ClientAPI{sessionMgr: sm}
}

// Register requests an OTP for a client phone number.
//
// The returned response carries DebugCode whenever the stand runs with
// DEBUG=true — that is the automated path to the one-time code, replacing the
// manual "read it from the Telegram group" step.
func (api *ClientAPI) Register(ctx context.Context, req ClientRegisterRequest) (*ClientRegisterResponse, error) {
	var out ClientRegisterResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/register", "", req, &out)
	if err != nil {
		return nil, fmt.Errorf("client register failed: %w", err)
	}
	return &out, nil
}

// Login authenticates client with verification code and sets active session
func (api *ClientAPI) Login(ctx context.Context, req ClientLoginRequest) (string, error) {
	var token string
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/login", "", req, &token)
	if err != nil {
		return "", fmt.Errorf("client login failed: %w", err)
	}

	fullName := fmt.Sprintf("%s %s", req.FirstName, req.LastName)
	api.sessionMgr.SetClientSession(req.PhoneNumber, token, req.PhoneNumber, fullName)
	return token, nil
}

// LoginWithoutSession authenticates client without mutating the shared SessionManager.
func (api *ClientAPI) LoginWithoutSession(ctx context.Context, req ClientLoginRequest) (string, error) {
	var token string
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/login", "", req, &token)
	if err != nil {
		return "", fmt.Errorf("client login failed: %w", err)
	}
	return token, nil
}

// CreateRestaurantOrder places a food delivery order
func (api *ClientAPI) CreateRestaurantOrder(ctx context.Context, req CreateRestaurantOrderRequest) (*OrderResponse, error) {
	return api.CreateRestaurantOrderWithIdempotency(ctx, req, "")
}

// CreateRestaurantOrderWithIdempotency places a food delivery order with optional Idempotency-Key
func (api *ClientAPI) CreateRestaurantOrderWithIdempotency(ctx context.Context, req CreateRestaurantOrderRequest, idempotencyKey string) (*OrderResponse, error) {
	token := api.sessionMgr.GetClientSession().Token
	var orderResp OrderResponse

	// If idempotency key is passed, we can send header via custom request
	headers := make(map[string]string)
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}

	_, err := api.sessionMgr.HTTPClient().RequestWithHeaders(ctx, "POST", "/api/clients/create-order", token, headers, req, &orderResp)
	if err != nil {
		return nil, fmt.Errorf("client create order failed: %w", err)
	}
	return &orderResp, nil
}

// CreateIndependentOrder places a point A to point B parcel delivery order
func (api *ClientAPI) CreateIndependentOrder(ctx context.Context, req CreateIndependentOrderRequest) (*OrderResponse, error) {
	token := api.sessionMgr.GetClientSession().Token
	var orderResp OrderResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/independent-order", token, req, &orderResp)
	if err != nil {
		return nil, fmt.Errorf("client create independent order failed: %w", err)
	}
	return &orderResp, nil
}

// CancelOrder cancels client order. The backend reads the reason from
// `cancel_reason` and the free text from `comment`; an unknown key silently
// falls back to the default reason.
func (api *ClientAPI) CancelOrder(ctx context.Context, orderID, reason string) error {
	token := api.sessionMgr.GetClientSession().Token
	path := fmt.Sprintf("/api/clients/orders/%s/cancel", orderID)
	req := map[string]string{"cancel_reason": reason, "comment": reason}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", path, token, req, nil)
	if err != nil {
		return fmt.Errorf("client cancel order failed: %w", err)
	}
	return nil
}

// GetOrder fetches current status of an order for client
func (api *ClientAPI) GetOrder(ctx context.Context, orderID string) (*OrderResponse, error) {
	token := api.sessionMgr.GetClientSession().Token
	var orderResp OrderResponse
	path := fmt.Sprintf("/api/clients/active-orders/%s", orderID)
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "GET", path, token, nil, &orderResp)
	if err != nil {
		return nil, fmt.Errorf("client get order failed: %w", err)
	}
	return &orderResp, nil
}

// StartPhoneChange sends an OTP to the client's current number (step 1).
func (api *ClientAPI) StartPhoneChange(ctx context.Context) error {
	token := api.sessionMgr.GetClientSession().Token
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/phone-change/start", token, nil, nil)
	if err != nil {
		return fmt.Errorf("phone change start failed: %w", err)
	}
	return nil
}

// VerifyOldPhoneCode confirms the code sent to the current number (step 2a).
func (api *ClientAPI) VerifyOldPhoneCode(ctx context.Context, code string) error {
	token := api.sessionMgr.GetClientSession().Token
	req := PhoneChangeVerifyOldRequest{OldCode: code}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/phone-change/verify-old", token, req, nil)
	if err != nil {
		return fmt.Errorf("phone change verify-old failed: %w", err)
	}
	return nil
}

// StartNewPhoneCode sends an OTP to the requested new number (step 2b).
func (api *ClientAPI) StartNewPhoneCode(ctx context.Context, newPhone string) error {
	token := api.sessionMgr.GetClientSession().Token
	req := PhoneChangeStartNewRequest{NewPhoneNumber: newPhone}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/phone-change/start-new", token, req, nil)
	if err != nil {
		return fmt.Errorf("phone change start-new failed: %w", err)
	}
	return nil
}

// VerifyNewPhoneCode confirms the code from the new number and applies the
// change (step 3).
func (api *ClientAPI) VerifyNewPhoneCode(ctx context.Context, code string) error {
	token := api.sessionMgr.GetClientSession().Token
	req := PhoneChangeVerifyNewRequest{NewCode: code}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/phone-change/verify-new", token, req, nil)
	if err != nil {
		return fmt.Errorf("phone change verify-new failed: %w", err)
	}
	return nil
}

// CancelPhoneChange drops the in-flight phone-change session. It is
// idempotent: cancelling without an active session succeeds.
func (api *ClientAPI) CancelPhoneChange(ctx context.Context) error {
	token := api.sessionMgr.GetClientSession().Token
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/phone-change/cancel", token, nil, nil)
	if err != nil {
		return fmt.Errorf("phone change cancel failed: %w", err)
	}
	return nil
}

// AddCartItem puts a dish into the client's server-side cart. The restaurant
// order is assembled from that cart, so a flow that places an order must fill
// it first.
func (api *ClientAPI) AddCartItem(ctx context.Context, req AddCartItemRequest) (*AddCartItemResponse, error) {
	token := api.sessionMgr.GetClientSession().Token
	if req.EntityType == "" {
		req.EntityType = EntityTypeDish
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}
	var out AddCartItemResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/cart/items", token, req, &out)
	if err != nil {
		return nil, fmt.Errorf("cart add item failed: %w", err)
	}
	return &out, nil
}

// ClearCart empties the client's cart for one restaurant group.
func (api *ClientAPI) ClearCart(ctx context.Context, restID string) error {
	token := api.sessionMgr.GetClientSession().Token
	path := fmt.Sprintf("/api/cart/groups/%s", restID)
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "DELETE", path, token, nil, nil)
	if err != nil {
		return fmt.Errorf("cart clear failed: %w", err)
	}
	return nil
}

// CreateAddress saves a delivery address and returns its id, which a delivery
// order refers to instead of carrying the address as text.
//
// A byte-identical address is answered with 409 ADDRESS_ALREADY_EXISTS that
// still carries the existing address_id; that is treated as success, since the
// caller only needs an address to order to.
func (api *ClientAPI) CreateAddress(ctx context.Context, req CreateAddressRequest) (*AddressResponse, error) {
	token := api.sessionMgr.GetClientSession().Token
	var out AddressResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/clients/addresses", token, req, &out)
	if err != nil {
		if apiErr, ok := AsAPIError(err); ok && apiErr.StatusCode == 409 {
			if existing := addressIDFromBody(apiErr.Body); existing != "" {
				return &AddressResponse{AddressID: existing, FullAddress: req.FullAddress, Code: apiErr.Code}, nil
			}
		}
		return nil, fmt.Errorf("client create address failed: %w", err)
	}
	return &out, nil
}

// addressIDFromBody digs the address_id out of the duplicate-address envelope.
func addressIDFromBody(body string) string {
	var envelope struct {
		AddressID string `json:"address_id"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return ""
	}
	return envelope.AddressID
}

// GetFinalOrder reads an order regardless of whether it is still active.
// GetOrder only covers active orders, so a delivered or cancelled order has to
// be read from the full history — which is exactly when a flow wants to assert
// the terminal state that was actually persisted.
func (api *ClientAPI) GetFinalOrder(ctx context.Context, orderID string) (*OrderResponse, error) {
	token := api.sessionMgr.GetClientSession().Token
	var order OrderResponse
	path := fmt.Sprintf("/api/clients/all-orders/%s", orderID)
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "GET", path, token, nil, &order)
	if err != nil {
		return nil, fmt.Errorf("client get final order failed: %w", err)
	}
	return &order, nil
}

// HasSession reports whether the client API currently has an active authenticated session.
func (api *ClientAPI) HasSession() bool {
	return api.sessionMgr != nil && api.sessionMgr.GetClientSession().Token != ""
}

// ParcelAvailability reports whether the platform accepts parcel orders right
// now, and the window it accepts them in. The backend answers per courier
// group; the window is platform-wide, so the first entry describes all of them.
func (api *ClientAPI) ParcelAvailability(ctx context.Context) (available bool, window string, err error) {
	token := api.sessionMgr.GetClientSession().Token
	var groups []CourierTypeAvailability
	_, err = api.sessionMgr.HTTPClient().Request(ctx, "GET", "/api/clients/courier-types", token, nil, &groups)
	if err != nil {
		return false, "", fmt.Errorf("client courier types failed: %w", err)
	}
	if len(groups) == 0 {
		return false, "", fmt.Errorf("на стенде не заведено ни одной группы курьеров")
	}

	// Any group being open means the platform window is open.
	for _, g := range groups {
		if g.IsAvailable {
			return true, fmt.Sprintf("%s–%s", g.StartTime, g.EndTime), nil
		}
	}
	return false, fmt.Sprintf("%s–%s", groups[0].StartTime, groups[0].EndTime), nil
}
