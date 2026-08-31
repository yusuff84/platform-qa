package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// CourierAPI provides methods for Courier App interaction
type CourierAPI struct {
	sessionMgr *SessionManager
}

// NewCourierAPI creates a new Courier API service
func NewCourierAPI(sm *SessionManager) *CourierAPI {
	return &CourierAPI{sessionMgr: sm}
}

// Register registers a new courier
func (api *CourierAPI) Register(ctx context.Context, req CourierRegisterRequest) error {
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/couriers/register", "", req, nil)
	if err != nil {
		return fmt.Errorf("courier register failed: %w", err)
	}
	return nil
}

// Login authenticates courier and sets active session
func (api *CourierAPI) Login(ctx context.Context, req CourierLoginRequest) (string, error) {
	var token string
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/couriers/login", "", req, &token)
	if err != nil {
		return "", fmt.Errorf("courier login failed: %w", err)
	}

	api.sessionMgr.SetCourierSession(req.PhoneNumber, token, req.PhoneNumber, req.PhoneNumber)
	return token, nil
}

// TakeOrder accepts/takes an assigned order
func (api *CourierAPI) TakeOrder(ctx context.Context, orderID, courierID string) error {
	token := api.sessionMgr.GetCourierSession().Token
	req := CourierTakeOrderRequest{
		OrderID:   orderID,
		CourierID: courierID,
	}

	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/couriers/take-order", token, req, nil)
	if err != nil {
		return fmt.Errorf("courier take order failed: %w", err)
	}
	return nil
}

// ChangeStatus updates courier delivery status (e.g. 'going', 'shipping', 'complete').
//
// The endpoint does not promise an order back: depending on the branch it
// answers with the updated order or with Sequelize's raw update result — a
// bare [1] of affected rows. Decoding straight into an order therefore fails
// on a call that actually succeeded, so the body is parsed leniently and the
// caller always gets the order id plus the status it asked for.
func (api *CourierAPI) ChangeStatus(ctx context.Context, orderID, status string, isIndependent bool) (*OrderResponse, error) {
	token := api.sessionMgr.GetCourierSession().Token
	req := CourierChangeStatusRequest{
		OrderID:       orderID,
		Status:        status,
		IsIndependent: isIndependent,
	}

	var raw json.RawMessage
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/couriers/change-status", token, req, &raw)
	if err != nil {
		return nil, fmt.Errorf("courier change status to '%s' failed: %w", status, err)
	}

	updated := OrderResponse{OrderID: orderID, CourierStatus: status}
	// Only an object can be an order; [1] and other shapes carry no state.
	if len(raw) > 0 && raw[0] == '{' {
		var decoded OrderResponse
		if json.Unmarshal(raw, &decoded) == nil && decoded.OrderID != "" {
			updated = decoded
		}
	}
	return &updated, nil
}

// Complete marks order delivery as finished
func (api *CourierAPI) Complete(ctx context.Context, orderID string) error {
	token := api.sessionMgr.GetCourierSession().Token
	req := map[string]string{"order_id": orderID}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/couriers/complete", token, req, nil)
	if err != nil {
		return fmt.Errorf("courier complete order failed: %w", err)
	}
	return nil
}
