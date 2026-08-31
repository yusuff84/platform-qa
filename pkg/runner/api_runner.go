package runner

import (
	"context"
	"fmt"
	"time"

	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/fixtures"
)

// The API layer answers a different question than the flow suites.
//
// A flow proves that a scenario works end to end and stops at the first broken
// step — there is no point cooking an order that was never created. An API
// check proves one endpoint honours its contract, so the checks are
// independent: every one runs even when its neighbour fails, and a single
// broken handler does not hide the state of the other twenty.

// apiEnv is the state an API suite prepares once and shares across its checks.
// Preparing it per check would multiply setup cost and make failures depend on
// each other again.
type apiEnv struct {
	ctx context.Context
	o   *TestOrchestrator

	ClientToken  string
	ClientPhone  string
	RestToken    string
	RestID       string
	CourierToken string
	CourierID    string
	AdminToken   string

	// Menu artefacts created during setup and reused by later checks.
	CategoryID string
	DishID     string
}

// apiCheck is one endpoint contract verified in isolation.
type apiCheck struct {
	ID    string
	Title string
	// Needs lists the env fields the check cannot run without ("rest",
	// "client", "courier", "admin", "dish"). A check whose prerequisite is
	// missing is skipped with that reason instead of failing on a nil token.
	Needs []string
	Run   func(env *apiEnv) error
}

// request performs a call and returns the decoded body plus the typed error.
func (e *apiEnv) request(method, path, token string, body interface{}, out interface{}) error {
	_, err := e.o.engine.HTTPClient.Request(e.ctx, method, path, token, body, out)
	return err
}

// expectStatus asserts that a call is refused with exactly this status. It is
// the workhorse of guard checks: the point is the refusal, not the payload.
func (e *apiEnv) expectStatus(method, path, token string, body interface{}, want int, what string) error {
	err := e.request(method, path, token, body, nil)
	if err == nil {
		return fmt.Errorf("%s: запрос прошёл, ожидался %d", what, want)
	}
	if got := client.StatusOf(err); got != want {
		return fmt.Errorf("%s: ожидался %d, получено %d (%v)", what, want, got, err)
	}
	return nil
}

// expectOK asserts that a call succeeds, decoding into out when given.
func (e *apiEnv) expectOK(method, path, token string, body interface{}, out interface{}, what string) error {
	if err := e.request(method, path, token, body, out); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// prepareAPIEnv authenticates one account per role and builds a minimal menu.
// Failures are not fatal: a suite about the wallet still runs when the menu
// could not be created, it just skips the checks that needed it.
func (o *TestOrchestrator) prepareAPIEnv(ctx context.Context, needs map[string]bool) *apiEnv {
	env := &apiEnv{ctx: ctx, o: o}

	if needs["client"] {
		if phone, token, err := o.engine.Fixtures.CreateUniqueClient(ctx); err == nil {
			env.ClientPhone, env.ClientToken = phone, token
		} else {
			o.logf("[API] клиент не создан: %v", err)
		}
	}
	if needs["rest"] {
		if _, token, err := o.engine.Fixtures.CreateUniqueRestaurant(ctx); err == nil {
			env.RestToken = token
			if claims, cerr := client.DecodeClaims(token); cerr == nil {
				env.RestID = claims.UserID
			}
		} else {
			o.logf("[API] ресторан не создан: %v", err)
		}
	}
	if needs["courier"] {
		if id, token, err := o.engine.Fixtures.CreateUniqueCourier(ctx); err == nil {
			env.CourierID, env.CourierToken = id, token
		} else {
			o.logf("[API] курьер не создан: %v", err)
		}
	}
	if needs["admin"] {
		if token, err := o.engine.SessionMgr.LoginAdmin(ctx, o.engine.Config.AdminLogin, o.engine.Config.AdminPassword); err == nil {
			env.AdminToken = token
		} else {
			o.logf("[API] вход администратора не выполнен: %v", err)
		}
	}

	if needs["dish"] && env.RestToken != "" {
		var category client.CategoryResponse
		var created []client.CategoryResponse
		if err := env.request("POST", "/api/rests/categories", env.RestToken,
			client.CreateCategoryRequest{Title: "Категория API"}, &created); err == nil && len(created) > 0 {
			category = created[0]
			env.CategoryID = category.CategoryID

			var dish client.DishResponse
			if err := env.request("POST", "/api/rests/dishes", env.RestToken, client.CreateDishRequest{
				Title:      fmt.Sprintf("Блюдо API %d", time.Now().UnixNano()%100000),
				Price:      fixtures.DishFixturePrice,
				CategoryID: category.CategoryID,
			}, &dish); err == nil {
				env.DishID = dish.DishID
			} else {
				o.logf("[API] блюдо не создано: %v", err)
			}
		} else if err != nil {
			o.logf("[API] категория не создана: %v", err)
		}
	}

	return env
}

// missingPrerequisite names the first prerequisite the env does not satisfy.
func (env *apiEnv) missingPrerequisite(needs []string) string {
	for _, need := range needs {
		switch need {
		case "client":
			if env.ClientToken == "" {
				return "не удалось получить токен клиента"
			}
		case "rest":
			if env.RestToken == "" {
				return "не удалось получить токен ресторана"
			}
		case "courier":
			if env.CourierToken == "" {
				return "не удалось получить токен курьера"
			}
		case "admin":
			if env.AdminToken == "" {
				return "не удалось получить токен администратора"
			}
		case "dish":
			if env.DishID == "" {
				return "не удалось подготовить блюдо в меню"
			}
		}
	}
	return ""
}

// runAPISuite executes every check of an API suite independently.
//
// It never returns an error for a failed check: an endpoint suite reports the
// state of each endpoint, and aborting on the first failure would hide the
// rest. Only a setup that produced nothing usable ends the suite early.
func (o *TestOrchestrator) runAPISuite(ctx context.Context, run *TestRun, suiteKey, suiteName string, checks []apiCheck) error {
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   fmt.Sprintf("Проверка контрактов ручек: %d независимых проверок", len(checks)),
		Timestamp: time.Now(),
	})

	needs := map[string]bool{}
	for _, c := range checks {
		for _, n := range c.Needs {
			needs[n] = true
		}
	}

	setupStart := time.Now()
	env := o.prepareAPIEnv(ctx, needs)
	o.Emit(&ExecutionEvent{
		RunID:      run.ID,
		SuiteName:  suiteName,
		SuiteKey:   suiteKey,
		StepType:   "GIVEN",
		Level:      LogInfo,
		Message:    "Подготовлены аккаунты и данные для проверок",
		DurationMs: time.Since(setupStart).Milliseconds(),
		Timestamp:  time.Now(),
	})

	for _, check := range checks {
		if reason := env.missingPrerequisite(check.Needs); reason != "" {
			o.checkSkip(run, suiteKey, check.ID, reason)
			continue
		}

		start := time.Now()
		o.checkStart(run, suiteKey, check.ID)

		err := check.Run(env)
		if err != nil {
			o.checkDone(run, suiteKey, check.ID, false, err.Error(), start)
			o.Emit(&ExecutionEvent{
				RunID:     run.ID,
				SuiteName: suiteName,
				SuiteKey:  suiteKey,
				StepType:  "THEN",
				Level:     LogError,
				Message:   fmt.Sprintf("✗ %s — %v", check.Title, err),
				CheckID:   check.ID,
				Timestamp: time.Now(),
			})
			continue
		}

		o.checkDone(run, suiteKey, check.ID, true, check.Title, start)
		o.Emit(&ExecutionEvent{
			RunID:     run.ID,
			SuiteName: suiteName,
			SuiteKey:  suiteKey,
			StepType:  "THEN",
			Level:     LogSuccess,
			Message:   "✓ " + check.Title,
			CheckID:   check.ID,
			Timestamp: time.Now(),
		})
	}

	return nil
}

// logf routes a diagnostic line into the engine log.
func (o *TestOrchestrator) logf(format string, args ...interface{}) {
	if o.engine != nil && o.engine.Fixtures != nil {
		o.engine.Fixtures.Logf(format, args...)
	}
}

// apiSuiteKeys lists the endpoint suites in catalog order.
func apiSuiteKeys() []string {
	return []string{"api_menu", "api_modifiers", "api_cart", "api_wallet", "api_order_status", "api_guards"}
}

// executeAPISuiteByKey dispatches one endpoint suite by its catalog key.
func (o *TestOrchestrator) executeAPISuiteByKey(ctx context.Context, run *TestRun, key string) error {
	switch key {
	case "api_menu":
		return o.runAPISuite(ctx, run, key, "API: меню ресторана", apiMenuChecks())
	case "api_modifiers":
		return o.runAPISuite(ctx, run, key, "API: модификаторы блюд", apiModifierChecks())
	case "api_cart":
		return o.runAPISuite(ctx, run, key, "API: корзина клиента", apiCartChecks())
	case "api_wallet":
		return o.runAPISuite(ctx, run, key, "API: кошельки", apiWalletChecks())
	case "api_order_status":
		return o.runAPISuite(ctx, run, key, "API: статусы заказа", apiStatusChecks())
	case "api_guards":
		return o.runAPISuite(ctx, run, key, "API: ролевые гейты", apiGuardChecks())
	}
	return fmt.Errorf("unknown api suite: %s", key)
}
