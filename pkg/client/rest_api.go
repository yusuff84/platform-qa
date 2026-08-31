package client

import (
	"context"
	"fmt"
)

// RestAPI provides methods for Restaurant App interaction
type RestAPI struct {
	sessionMgr *SessionManager
}

// NewRestAPI creates a new Restaurant API service
func NewRestAPI(sm *SessionManager) *RestAPI {
	return &RestAPI{sessionMgr: sm}
}

// Register registers a new restaurant
func (api *RestAPI) Register(ctx context.Context, req RestRegisterRequest) error {
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/register", "", req, nil)
	if err != nil {
		return fmt.Errorf("restaurant register failed: %w", err)
	}
	return nil
}

// Login authenticates restaurant and sets active session
func (api *RestAPI) Login(ctx context.Context, req RestLoginRequest) (string, error) {
	var token string
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/login", "", req, &token)
	if err != nil {
		return "", fmt.Errorf("restaurant login failed: %w", err)
	}

	api.sessionMgr.SetRestSession(req.Login, token, req.Login, req.Login)
	return token, nil
}

// ChangeOrderStatus transitions restaurant order status (e.g. 'cooking', 'cooked', 'shipping', 'complete')
func (api *RestAPI) ChangeOrderStatus(ctx context.Context, orderID, status string) (*OrderResponse, error) {
	token := api.sessionMgr.GetRestSession().Token
	req := ChangeRestOrderStatusRequest{
		OrderID: orderID,
		Status:  status,
	}

	var updatedOrder OrderResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/order-status", token, req, &updatedOrder)
	if err != nil {
		return nil, fmt.Errorf("restaurant change order status to '%s' failed: %w", status, err)
	}
	return &updatedOrder, nil
}

// CreateDish adds a dish to the restaurant menu
func (api *RestAPI) CreateDish(ctx context.Context, req CreateDishRequest) (*DishResponse, error) {
	token := api.sessionMgr.GetRestSession().Token
	var dishResp DishResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/dishes", token, req, &dishResp)
	if err != nil {
		return nil, fmt.Errorf("restaurant create dish failed: %w", err)
	}
	return &dishResp, nil
}

// RequestPasswordReset sends a reset OTP to the restaurant's phone.
// Repeating within 60 seconds is answered with 429 and a retryAfter hint.
func (api *RestAPI) RequestPasswordReset(ctx context.Context, phone string) error {
	req := RestPasswordResetRequestRequest{PhoneNumber: phone}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/password-reset/request", "", req, nil)
	if err != nil {
		return fmt.Errorf("restaurant password reset request failed: %w", err)
	}
	return nil
}

// VerifyPasswordResetCode exchanges the OTP for a reset token valid 5 minutes.
// Five wrong codes invalidate the OTP entirely (429).
func (api *RestAPI) VerifyPasswordResetCode(ctx context.Context, phone, code string) (string, error) {
	req := RestPasswordResetVerifyRequest{PhoneNumber: phone, Code: code}
	var out RestPasswordResetVerifyResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/password-reset/verify", "", req, &out)
	if err != nil {
		return "", fmt.Errorf("restaurant password reset verify failed: %w", err)
	}
	return out.ResetToken, nil
}

// ConfirmPasswordReset consumes the reset token and sets the new password.
// The backend enforces 8+ chars with an uppercase letter and a digit.
func (api *RestAPI) ConfirmPasswordReset(ctx context.Context, resetToken, newPassword string) error {
	req := RestPasswordResetConfirmRequest{ResetToken: resetToken, NewPassword: newPassword}
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/password-reset/confirm", "", req, nil)
	if err != nil {
		return fmt.Errorf("restaurant password reset confirm failed: %w", err)
	}
	return nil
}

// CreateCategory adds a menu category. A dish cannot be created without one:
// the dish validator requires category_id.
func (api *RestAPI) CreateCategory(ctx context.Context, req CreateCategoryRequest) (*CategoryResponse, error) {
	token := api.sessionMgr.GetRestSession().Token
	// The endpoint answers with an array (it accepts batches), so decode into
	// a slice and take the first entry.
	var created []CategoryResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "POST", "/api/rests/categories", token, req, &created)
	if err != nil {
		return nil, fmt.Errorf("restaurant create category failed: %w", err)
	}
	if len(created) == 0 {
		return nil, fmt.Errorf("restaurant create category returned no category")
	}
	return &created[0], nil
}

// UpdateSchedule sets the restaurant's working hours. Without a schedule the
// restaurant counts as closed and every order is refused with RESTAURANT_CLOSED.
func (api *RestAPI) UpdateSchedule(ctx context.Context, req UpdateScheduleRequest) error {
	token := api.sessionMgr.GetRestSession().Token
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "PUT", "/api/rests/schedule", token, req, nil)
	if err != nil {
		return fmt.Errorf("restaurant update schedule failed: %w", err)
	}
	return nil
}

// ListDishes returns the restaurant's menu. A flow that runs against an
// operator-bound restaurant reuses a dish that is already there instead of
// adding another one to somebody's real menu on every run.
func (api *RestAPI) ListDishes(ctx context.Context) ([]DishResponse, error) {
	token := api.sessionMgr.GetRestSession().Token
	var dishes []DishResponse
	_, err := api.sessionMgr.HTTPClient().Request(ctx, "GET", "/api/rests/dishes", token, nil, &dishes)
	if err != nil {
		return nil, fmt.Errorf("restaurant list dishes failed: %w", err)
	}
	return dishes, nil
}
