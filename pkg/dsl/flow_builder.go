package dsl

import (
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"locali-e2e-engine/config"
	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/events"
	"locali-e2e-engine/pkg/fixtures"
	"locali-e2e-engine/pkg/statemachine"
)

// Engine is the central test orchestrator
type Engine struct {
	Config       *config.Config
	HTTPClient   *client.HTTPClient
	SessionMgr   *client.SessionManager
	ClientAPI    *client.ClientAPI
	RestAPI      *client.RestAPI
	CourierAPI   *client.CourierAPI
	AdminAPI     *client.AdminAPI
	StateMachine *statemachine.OrderStateMachine
	Events       *events.NatsEventListener
	Fixtures     *fixtures.FixtureManager
}

// NewEngine initializes the full testing engine
func NewEngine(cfg *config.Config) (*Engine, error) {
	if cfg == nil {
		cfg = config.LoadFromEnv()
	}

	httpClient := client.NewHTTPClient(cfg.BaseURL, cfg.RequestTimeout)
	sessionMgr := client.NewSessionManager(httpClient)

	clientAPI := client.NewClientAPI(sessionMgr)
	restAPI := client.NewRestAPI(sessionMgr)
	courierAPI := client.NewCourierAPI(sessionMgr)
	adminAPI := client.NewAdminAPI(sessionMgr)

	eventListener, err := events.NewNatsEventListener(cfg.NatsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to init event listener: %w", err)
	}

	sm := statemachine.NewOrderStateMachine()
	fixMgr := fixtures.NewFixtureManager(clientAPI, restAPI, courierAPI, adminAPI)
	fixMgr.SetVerificationCode(cfg.VerificationCode)
	fixMgr.SetCity(cfg.FixtureCity)
	fixMgr.SetCourierGroupID(cfg.FixtureCourierGroupID)
	fixMgr.SetCoordinates(cfg.FixtureLatitude, cfg.FixtureLongitude)

	// A pinned client phone trades isolation for a stable identity. USE_TEST_PHONE
	// selects the backend's DEBUG-only backdoor number; FIXTURE_CLIENT_PHONE wins
	// when both are set, since it is the more specific instruction.
	switch {
	case cfg.FixtureClientPhone != "":
		fixMgr.PinClientPhone(cfg.FixtureClientPhone)
	case cfg.UseTestPhone:
		fixMgr.PinClientPhone(fixtures.ReservedTestPhone)
	}

	if cfg.HTTPDebug {
		httpClient.SetDebug(true)
	}

	// Preset tokens from env: register them as active sessions and let fixtures
	// reuse them without register/login calls (isolation is knowingly sacrificed
	// when running against a real backend with pre-provisioned identities).
	for role, token := range map[string]string{
		"client":  cfg.ClientToken,
		"rest":    cfg.RestToken,
		"courier": cfg.CourierToken,
		"admin":   cfg.AdminToken,
	} {
		if token == "" {
			continue
		}
		sessionMgr.AddToken(role, "Preset (env)", "", token, true)
		// A token that does not match the role it was configured under is
		// reported rather than swallowed: tests would otherwise run as the
		// wrong identity and still look green.
		if err := fixMgr.BindAccount(role, "Preset (env)", "", token); err != nil {
			log.Printf("[ENGINE] %s_TOKEN не привязан к роли: %v", strings.ToUpper(role), err)
		}
	}

	return &Engine{
		Config:       cfg,
		HTTPClient:   httpClient,
		SessionMgr:   sessionMgr,
		ClientAPI:    clientAPI,
		RestAPI:      restAPI,
		CourierAPI:   courierAPI,
		AdminAPI:     adminAPI,
		StateMachine: sm,
		Events:       eventListener,
		Fixtures:     fixMgr,
	}, nil
}

// ScenarioBuilder provides fluent BDD style test step execution
type ScenarioBuilder struct {
	t       *testing.T
	name    string
	engine  *Engine
	ctx     *TestContext
	started time.Time
}

// TestContext holds state between scenario steps
type TestContext struct {
	t            *testing.T
	Engine       *Engine
	GoCtx        context.Context
	OrderType    statemachine.OrderType
	CurrentOrder *client.OrderResponse
	LastStatus   statemachine.OrderStatus
	ClientPhone  string
	RestID       string
	CourierID    string
	AdminToken   string
	// Order is the prepared ordering situation (restaurant, dish, address,
	// filled cart). SetupAllRoles builds it, and OrderRequest() turns it into
	// a valid create-order body.
	Order *fixtures.OrderContext
	Data  map[string]interface{}
}

// Scenario starts a new declarative test scenario
func (e *Engine) Scenario(t *testing.T, name string) *ScenarioBuilder {
	ctx := &TestContext{
		t:         t,
		Engine:    e,
		GoCtx:     context.Background(),
		Data:      make(map[string]interface{}),
		OrderType: statemachine.OrderTypeRestaurant,
	}

	sb := &ScenarioBuilder{
		t:       t,
		name:    name,
		engine:  e,
		ctx:     ctx,
		started: time.Now(),
	}

	t.Logf("\n======================================================\n[SCENARIO START] %s\n======================================================", name)
	return sb
}

// Given executes background or initial setup
func (sb *ScenarioBuilder) Given(description string, step func(ctx *TestContext)) *ScenarioBuilder {
	sb.t.Logf("  [GIVEN] %s", description)
	step(sb.ctx)
	return sb
}

// When executes an action step
func (sb *ScenarioBuilder) When(description string, step func(ctx *TestContext)) *ScenarioBuilder {
	sb.t.Logf("  [WHEN]  %s", description)
	step(sb.ctx)
	return sb
}

// Then executes assertion and validation step
func (sb *ScenarioBuilder) Then(description string, step func(ctx *TestContext)) *ScenarioBuilder {
	sb.t.Logf("  [THEN]  %s", description)
	step(sb.ctx)
	return sb
}

// And chains an additional check or step
func (sb *ScenarioBuilder) And(description string, step func(ctx *TestContext)) *ScenarioBuilder {
	sb.t.Logf("  [AND]   %s", description)
	step(sb.ctx)
	return sb
}

// SetupAllRoles generates/authenticates unique Client, Restaurant, Courier and Admin identities
func (c *TestContext) SetupAllRoles() {
	var err error

	// Admin Auth
	if c.Engine.Config.AdminToken != "" {
		c.Engine.SessionMgr.SetAdminSession("admin_root", c.Engine.Config.AdminToken, c.Engine.Config.AdminLogin)
		c.AdminToken = c.Engine.Config.AdminToken
	} else {
		token, err := c.Engine.SessionMgr.LoginAdmin(c.GoCtx, c.Engine.Config.AdminLogin, c.Engine.Config.AdminPassword)
		require.NoError(c.t, err, "Admin login must succeed")
		c.AdminToken = token
	}

	// Client + Restaurant, prepared far enough that an order can be placed:
	// the restaurant order is assembled from the client's server-side cart, so
	// a menu, an address and a filled cart are preconditions, not extras.
	order, err := c.Engine.Fixtures.PrepareRestaurantOrder(c.GoCtx)
	require.NoError(c.t, err, "Order context preparation must succeed")
	c.Order = order
	c.ClientPhone = order.ClientPhone
	c.RestID = order.RestID

	// Courier Auth
	c.CourierID, _, err = c.Engine.Fixtures.CreateUniqueCourier(c.GoCtx)
	require.NoError(c.t, err, "Courier fixture creation must succeed")

	// Register teardown at test conclusion
	c.t.Cleanup(func() {
		errs := c.Engine.Fixtures.Teardown(context.Background())
		if len(errs) > 0 {
			c.t.Logf("[TEARDOWN WARNING] %d errors during cleanup: %v", len(errs), errs)
		}
	})
}

// AssertOrderStatus checks order status with State Machine transition validation
func (c *TestContext) AssertOrderStatus(expected statemachine.OrderStatus, actor statemachine.Role) {
	require.NotNil(c.t, c.CurrentOrder, "Current order must not be nil")

	// State machine check
	if c.LastStatus != "" {
		err := c.Engine.StateMachine.ValidateTransition(c.OrderType, c.LastStatus, expected, actor)
		require.NoError(c.t, err, "State Machine transition rule violation")
	}

	c.Engine.StateMachine.RecordTransition(c.CurrentOrder.OrderID, expected)
	c.LastStatus = expected

	c.t.Logf("    ✓ State validated: %s (Actor: %s, OrderID: %s)", expected, actor, c.CurrentOrder.OrderID)
}
