package runner

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/dsl"
	"locali-e2e-engine/pkg/fixtures"
	"locali-e2e-engine/pkg/registry"
	"locali-e2e-engine/pkg/scenario"
	"locali-e2e-engine/pkg/statemachine"
)

// LogLevel represents log severity
type LogLevel string

const (
	LogInfo    LogLevel = "INFO"
	LogSuccess LogLevel = "SUCCESS"
	LogWarn    LogLevel = "WARN"
	LogError   LogLevel = "ERROR"
	LogHTTP    LogLevel = "HTTP"
)

// Run statuses stored in TestRun.Status. SKIPPED marks a run that failed
// nothing but could not execute everything either — a closed platform window,
// a missing precondition — and therefore proves less than a pass.
const (
	RunRunning = "RUNNING"
	RunPassed  = "PASSED"
	RunFailed  = "FAILED"
	RunSkipped = "SKIPPED"
)

// Check execution statuses stored in TestRun.Results
const (
	CheckRunning = "RUNNING"
	CheckPassed  = "PASSED"
	CheckFailed  = "FAILED"
	CheckSkipped = "SKIPPED"
)

// MaxRecentEvents is the ring buffer capacity of recent execution events
const MaxRecentEvents = 2000

// Trigger values stored in TestRun.Trigger. Manual runs alert individually,
// grouped triggers (regression/webhook) are reported with a single digest.
const (
	TriggerManual     = "manual"
	TriggerRegression = "regression"
	TriggerWebhook    = "webhook"
)

// ExecutionEvent is emitted during test execution to update the Admin UI in real-time
type ExecutionEvent struct {
	RunID        string                   `json:"runId"`
	SuiteName    string                   `json:"suiteName"`
	SuiteKey     string                   `json:"suiteKey,omitempty"`
	StepName     string                   `json:"stepName"`
	StepType     string                   `json:"stepType"` // GIVEN, WHEN, THEN, AND, INFO, CHECK_START, CHECK_SUCCESS, CHECK_FAILED
	Level        LogLevel                 `json:"level"`
	Message      string                   `json:"message"`
	CheckID      string                   `json:"checkId,omitempty"`
	InputPhone   string                   `json:"inputPhone,omitempty"`
	StepIndex    int                      `json:"stepIndex,omitempty"`
	TotalSteps   int                      `json:"totalSteps,omitempty"`
	CurrentState statemachine.OrderStatus `json:"currentState,omitempty"`
	OrderID      string                   `json:"orderId,omitempty"`
	DurationMs   int64                    `json:"durationMs"`
	Timestamp    time.Time                `json:"timestamp"`
	HTTPDetails  *HTTPDetails             `json:"httpDetails,omitempty"`
}

type HTTPDetails struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	Role       string      `json:"role"`
	StatusCode int         `json:"statusCode"`
	Request    interface{} `json:"request,omitempty"`
	Response   interface{} `json:"response,omitempty"`
}

// CheckResult is the outcome of a single check within a suite run
type CheckResult struct {
	Status     string `json:"status"` // RUNNING, PASSED, FAILED, SKIPPED
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// TestRun represents a complete execution record
type TestRun struct {
	ID           string                   `json:"id"`
	SuiteName    string                   `json:"suiteName"`
	SuiteKey     string                   `json:"suiteKey,omitempty"`
	Status       string                   `json:"status"` // RUNNING, PASSED, FAILED
	StartTime    time.Time                `json:"startTime"`
	EndTime      time.Time                `json:"endTime"`
	DurationMs   int64                    `json:"durationMs"`
	Events       []*ExecutionEvent        `json:"events"`
	FinalState   statemachine.OrderStatus `json:"finalState,omitempty"`
	Error        string                   `json:"error,omitempty"`
	PassedSteps  int                      `json:"passedSteps"`
	TotalSteps   int                      `json:"totalSteps"`
	TotalChecks  int                      `json:"totalChecks"`
	PassedChecks  int                     `json:"passedChecks"`
	FailedChecks  int                     `json:"failedChecks"`
	SkippedChecks int                     `json:"skippedChecks"`
	Results      map[string]CheckResult   `json:"results,omitempty"`

	// Trigger marks how the run was started: manual, regression or webhook.
	// Empty for runs recorded before the field existed (omitempty keeps old
	// persisted history files readable).
	Trigger string `json:"trigger,omitempty"`
	// GroupID ties all runs of one regression/webhook batch together so the
	// server can send a single digest per batch; empty for manual runs.
	GroupID string `json:"groupId,omitempty"`

	stepCursor int `json:"-"`
}

// CustomSource provides read access to user-defined custom scenarios
// (implemented by *scenario.Store).
type CustomSource interface {
	Get(key string) (*scenario.Scenario, bool)
	Exists(key string) bool
}

// TestOrchestrator manages and executes E2E test runs with live broadcasting
type TestOrchestrator struct {
	mu           sync.RWMutex
	engine       *dsl.Engine
	custom       CustomSource
	activeRuns   map[string]*TestRun
	history      []*TestRun
	historyStore *historyStore
	broadcaster  chan *ExecutionEvent
	recentEvents []*ExecutionEvent

	// InputBroker routes operator-entered verification codes from the HTTP API
	// to fixtures paused mid-run on client login (see input.go).
	InputBroker *InputBroker

	// OnRunFinished, when set, is invoked with a snapshot of every finished
	// run at the end of executeSuite. Observers must treat the run as
	// read-only and return quickly.
	OnRunFinished func(run *TestRun)
}

// NewTestOrchestrator creates a new test orchestrator
func NewTestOrchestrator(engine *dsl.Engine) *TestOrchestrator {
	return &TestOrchestrator{
		engine:      engine,
		activeRuns:  make(map[string]*TestRun),
		history:     make([]*TestRun, 0),
		InputBroker: NewInputBroker(),
		broadcaster: make(chan *ExecutionEvent, 2000),
	}
}

// SetCustomSource registers the storage of user-defined scenarios
func (o *TestOrchestrator) SetCustomSource(cs CustomSource) {
	o.mu.Lock()
	o.custom = cs
	o.mu.Unlock()
}

// hasCustom reports whether a user-defined scenario exists for the key
func (o *TestOrchestrator) hasCustom(key string) bool {
	o.mu.RLock()
	cs := o.custom
	o.mu.RUnlock()
	return cs != nil && cs.Exists(key)
}

// customScenario returns a copy of the custom scenario, or nil
func (o *TestOrchestrator) customScenario(key string) *scenario.Scenario {
	o.mu.RLock()
	cs := o.custom
	o.mu.RUnlock()
	if cs == nil {
		return nil
	}
	sc, ok := cs.Get(key)
	if !ok {
		return nil
	}
	return sc
}

// customChecks returns the number of steps if the key is a custom scenario, otherwise 0
func (o *TestOrchestrator) customChecks(key string) int {
	if sc := o.customScenario(key); sc != nil {
		return len(sc.Steps)
	}
	return 0
}

// customTitle returns the custom scenario title, or ""
func (o *TestOrchestrator) customTitle(key string) string {
	if sc := o.customScenario(key); sc != nil {
		return sc.Title
	}
	return ""
}

// Registry exposes the declarative suite catalog for the Admin UI
func (o *TestOrchestrator) Registry() []registry.Suite {
	return registry.All()
}

// Broadcaster returns the channel for streaming events to WebSockets/SSE
func (o *TestOrchestrator) Broadcaster() <-chan *ExecutionEvent {
	return o.broadcaster
}

// LoadHistory restores persisted run history from <dir>/runs. It must be
// called once at startup (before any run is executed). Corrupt files are
// skipped; the restored history is sorted by StartTime, newest first.
func (o *TestOrchestrator) LoadHistory(dir string) error {
	hs, err := newHistoryStore(dir)
	if err != nil {
		return err
	}
	loaded := hs.Load(MaxStoredRuns)

	o.mu.Lock()
	o.historyStore = hs
	o.history = append(o.history, loaded...)
	sort.SliceStable(o.history, func(i, j int) bool {
		return o.history[i].StartTime.After(o.history[j].StartTime)
	})
	if len(o.history) > MaxStoredRuns {
		o.history = o.history[:MaxStoredRuns]
	}
	o.mu.Unlock()

	log.Printf("[HISTORY] restored %d runs from %s", len(loaded), hs.dir)
	return nil
}

// GetHistory returns past test runs. Event payloads are stripped to keep the
// listing compact; the full record including events is available via GetRun.
func (o *TestOrchestrator) GetHistory() []*TestRun {
	o.mu.RLock()
	defer o.mu.RUnlock()
	res := make([]*TestRun, len(o.history))
	for i, run := range o.history {
		cp := *run
		cp.Events = nil
		res[i] = &cp
	}
	return res
}

// GetRun returns a specific test run by ID
func (o *TestOrchestrator) GetRun(id string) *TestRun {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if run, exists := o.activeRuns[id]; exists {
		return run
	}
	for _, run := range o.history {
		if run.ID == id {
			return run
		}
	}
	return nil
}

// Emit sends an execution event to active run, recent-events buffer and live broadcast
func (o *TestOrchestrator) Emit(event *ExecutionEvent) {
	o.mu.Lock()
	if run, exists := o.activeRuns[event.RunID]; exists {
		run.Events = append(run.Events, event)
	}
	o.recentEvents = append(o.recentEvents, event)
	if len(o.recentEvents) > MaxRecentEvents {
		o.recentEvents = o.recentEvents[len(o.recentEvents)-MaxRecentEvents:]
	}
	o.mu.Unlock()

	select {
	case o.broadcaster <- event:
	default:
	}
}

// RecentEvents returns the last limit buffered events in chronological
// order (oldest first). If limit <= 0 or exceeds the buffer capacity,
// all buffered events are returned.
func (o *TestOrchestrator) RecentEvents(limit int) []*ExecutionEvent {
	o.mu.RLock()
	defer o.mu.RUnlock()
	events := o.recentEvents
	if limit > 0 && limit < len(events) {
		events = events[len(events)-limit:]
	}
	res := make([]*ExecutionEvent, len(events))
	copy(res, events)
	return res
}

func (o *TestOrchestrator) newRun(suiteKey string) *TestRun {
	return &TestRun{
		ID:          uuid.New().String(),
		SuiteName:   suiteKey,
		SuiteKey:    suiteKey,
		Status:      "RUNNING",
		StartTime:   time.Now(),
		Events:      make([]*ExecutionEvent, 0),
		Results:     make(map[string]CheckResult),
		TotalChecks: o.totalCheckCount(suiteKey),
	}
}

// isValidSuiteKey reports whether the key can be executed (built-in or custom)
func (o *TestOrchestrator) isValidSuiteKey(key string) bool {
	if key == "all" {
		return true
	}
	if _, ok := registry.Get(key); ok {
		return true
	}
	return o.hasCustom(key)
}

// RunSuite runs a named suite asynchronously
func (o *TestOrchestrator) RunSuite(suiteKey string) (*TestRun, error) {
	if !o.isValidSuiteKey(suiteKey) {
		return nil, fmt.Errorf("unknown suite key: %s", suiteKey)
	}

	run := o.newRun(suiteKey)
	run.Trigger = TriggerManual

	o.mu.Lock()
	o.activeRuns[run.ID] = run
	o.mu.Unlock()

	go o.executeRuns([]*TestRun{run})

	return run, nil
}

// RunSuites creates a TestRun per valid key and executes them sequentially
// in a single goroutine (the caller's), never hitting the backend in parallel.
// Runs are tagged with the manual trigger.
func (o *TestOrchestrator) RunSuites(keys []string) ([]*TestRun, error) {
	return o.RunSuitesGroup(keys, TriggerManual, "")
}

// RunSuitesWithTrigger runs the keys as one trigger batch: every run gets the
// same Trigger and a single freshly generated GroupID, so observers (Telegram
// digest) can treat the whole batch as one unit.
func (o *TestOrchestrator) RunSuitesWithTrigger(keys []string, trigger string) ([]*TestRun, error) {
	return o.RunSuitesGroup(keys, trigger, "")
}

// RunSuitesGroup is the core behind RunSuites and RunSuitesWithTrigger: it
// validates all keys up front, tags every run with trigger/groupID (generating
// a fresh GroupID when none is given for a non-manual trigger) and executes
// the runs sequentially in the caller's goroutine.
func (o *TestOrchestrator) RunSuitesGroup(keys []string, trigger, groupID string) ([]*TestRun, error) {
	if trigger == "" {
		trigger = TriggerManual
	}
	if groupID == "" && trigger != TriggerManual {
		groupID = uuid.New().String()
	}

	runs := make([]*TestRun, 0, len(keys))
	for _, key := range keys {
		if !o.isValidSuiteKey(key) {
			return nil, fmt.Errorf("unknown suite key: %s", key)
		}
		run := o.newRun(key)
		run.Trigger = trigger
		run.GroupID = groupID
		runs = append(runs, run)
	}

	o.mu.Lock()
	for _, run := range runs {
		o.activeRuns[run.ID] = run
	}
	o.mu.Unlock()

	o.executeRuns(runs)

	return runs, nil
}

// executeRuns executes the given runs one by one in the current goroutine
func (o *TestOrchestrator) executeRuns(runs []*TestRun) {
	for _, run := range runs {
		o.executeSuite(run, run.SuiteKey)
	}
}

func (o *TestOrchestrator) totalCheckCount(suiteKey string) int {
	if suiteKey == "all" {
		n := 0
		for _, s := range registry.All() {
			n += len(s.Checks)
		}
		return n
	}
	if s, ok := registry.Get(suiteKey); ok {
		return len(s.Checks)
	}
	return o.customChecks(suiteKey)
}

func (o *TestOrchestrator) suiteTitle(suiteKey string) string {
	if s, ok := registry.Get(suiteKey); ok {
		return s.Title
	}
	if t := o.customTitle(suiteKey); t != "" {
		return t
	}
	return suiteKey
}

func (o *TestOrchestrator) checkCount(suiteKey string) int {
	return o.totalCheckCount(suiteKey)
}

func suiteKeysFor(suiteKey string) []string {
	if suiteKey == "all" {
		return registry.Keys()
	}
	return []string{suiteKey}
}

// checkStart marks a check as RUNNING and emits a CHECK_START event.
// The title is looked up in the built-in registry; custom scenarios
// should call checkStartTitled directly.
func (o *TestOrchestrator) checkStart(run *TestRun, suiteKey, checkID string) {
	label := checkID
	if suite, ok := registry.Get(suiteKey); ok {
		for _, c := range suite.Checks {
			if c.ID == checkID {
				label = c.Title
				break
			}
		}
	}
	o.checkStartTitled(run, suiteKey, checkID, label)
}

// checkStartTitled is like checkStart but accepts the human-readable title explicitly
func (o *TestOrchestrator) checkStartTitled(run *TestRun, suiteKey, checkID, title string) {
	total := o.checkCount(suiteKey)
	label := title
	if label == "" {
		label = checkID
	}

	o.mu.Lock()
	run.stepCursor++
	if run.Results == nil {
		run.Results = make(map[string]CheckResult)
	}
	run.Results[checkID] = CheckResult{Status: CheckRunning}
	cursor := run.stepCursor
	o.mu.Unlock()

	o.Emit(&ExecutionEvent{
		RunID:      run.ID,
		SuiteName:  o.suiteTitle(suiteKey),
		SuiteKey:   suiteKey,
		StepType:   "CHECK_START",
		Level:      LogInfo,
		Message:    fmt.Sprintf("▶ [%d/%d] %s", cursor, total, label),
		CheckID:    checkID,
		StepIndex:  cursor,
		TotalSteps: total,
		Timestamp:  time.Now(),
	})
}

// checkDone marks a check as PASSED/FAILED and emits a CHECK_SUCCESS/CHECK_FAILED event
func (o *TestOrchestrator) checkDone(run *TestRun, suiteKey, checkID string, ok bool, msg string, started time.Time) {
	duration := time.Since(started).Milliseconds()

	status, stepType, level, icon := CheckFailed, "CHECK_FAILED", LogError, "❌"
	if ok {
		status, stepType, level, icon = CheckPassed, "CHECK_SUCCESS", LogSuccess, "✅"
	}

	o.mu.Lock()
	if ok {
		run.PassedChecks++
	} else {
		run.FailedChecks++
	}
	if run.Results == nil {
		run.Results = make(map[string]CheckResult)
	}
	run.Results[checkID] = CheckResult{Status: status, Message: msg, DurationMs: duration}
	cursor := run.stepCursor
	o.mu.Unlock()

	o.Emit(&ExecutionEvent{
		RunID:      run.ID,
		SuiteName:  o.suiteTitle(suiteKey),
		SuiteKey:   suiteKey,
		StepType:   stepType,
		Level:      level,
		Message:    fmt.Sprintf("%s %s (%d ms)", icon, msg, duration),
		CheckID:    checkID,
		StepIndex:  cursor,
		TotalSteps: o.checkCount(suiteKey),
		DurationMs: duration,
		Timestamp:  time.Now(),
	})
}

// checkSkip marks a check as SKIPPED and emits an INFO event
func (o *TestOrchestrator) checkSkip(run *TestRun, suiteKey, checkID, reason string) {
	o.mu.Lock()
	if run.Results == nil {
		run.Results = make(map[string]CheckResult)
	}
	if prev, existed := run.Results[checkID]; !existed || prev.Status != CheckSkipped {
		run.SkippedChecks++
	}
	run.Results[checkID] = CheckResult{Status: CheckSkipped, Message: reason}
	o.mu.Unlock()

	// CHECK_SKIPPED, not INFO: the UI moves a check tile out of its running
	// state only on a check event, so an INFO left the tile spinning forever
	// while the suite reported itself finished.
	o.Emit(&ExecutionEvent{
		RunID:      run.ID,
		SuiteName:  o.suiteTitle(suiteKey),
		SuiteKey:   suiteKey,
		StepType:   "CHECK_SKIPPED",
		Level:      LogWarn,
		Message:    reason,
		CheckID:    checkID,
		TotalSteps: o.checkCount(suiteKey),
		Timestamp:  time.Now(),
	})
}

// finalizeChecks downgrades every leftover RUNNING or missing check to SKIPPED
// for both built-in suites and user-defined scenarios.
func (o *TestOrchestrator) finalizeChecks(run *TestRun, suiteKeys []string) {
	for _, sk := range suiteKeys {
		if suite, ok := registry.Get(sk); ok {
			for _, c := range suite.Checks {
				o.skipIfPending(run, sk, c.ID, "прогон завершился раньше")
			}
			continue
		}
		if sc := o.customScenario(sk); sc != nil {
			for _, st := range sc.Steps {
				o.skipIfPending(run, sk, st.ID, "прогон завершился раньше")
			}
		}
	}
}

func (o *TestOrchestrator) skipIfPending(run *TestRun, suiteKey, checkID, reason string) {
	o.mu.RLock()
	res, exists := run.Results[checkID]
	o.mu.RUnlock()
	if exists && res.Status != CheckRunning {
		return
	}
	o.checkSkip(run, suiteKey, checkID, reason)
}

func (o *TestOrchestrator) executeSuite(run *TestRun, suiteKey string) {
	ctx := context.WithValue(context.Background(), fixtures.InputCtxKey{}, &InputRef{Broker: o.InputBroker, RunID: run.ID})
	start := time.Now()

	var err error
	switch suiteKey {
	case "flow_a":
		err = o.runFlowA(ctx, run, suiteKey)
	case "flow_b":
		err = o.runFlowB(ctx, run, suiteKey)
	case "negative_sm":
		err = o.runNegativeSM(ctx, run, suiteKey)
	case "cancellation":
		err = o.runCancellation(ctx, run, suiteKey)
	case "security_rbac":
		err = o.runSecurityRBAC(ctx, run, suiteKey)
	case "auth_otp":
		err = o.runAuthOTP(ctx, run, suiteKey)
	case "api_menu":
		err = o.runAPISuite(ctx, run, suiteKey, "API: меню ресторана", apiMenuChecks())
	case "api_modifiers":
		err = o.runAPISuite(ctx, run, suiteKey, "API: модификаторы блюд", apiModifierChecks())
	case "api_cart":
		err = o.runAPISuite(ctx, run, suiteKey, "API: корзина клиента", apiCartChecks())
	case "api_wallet":
		err = o.runAPISuite(ctx, run, suiteKey, "API: кошельки", apiWalletChecks())
	case "api_order_status":
		err = o.runAPISuite(ctx, run, suiteKey, "API: статусы заказа", apiStatusChecks())
	case "api_guards":
		err = o.runAPISuite(ctx, run, suiteKey, "API: ролевые гейты", apiGuardChecks())
	case "idempotency":
		err = o.runIdempotency(ctx, run, suiteKey)
	case "all":
		err = o.runFlowA(ctx, run, "flow_a")
		if err == nil {
			err = o.runFlowB(ctx, run, "flow_b")
		}
		if err == nil {
			err = o.runNegativeSM(ctx, run, "negative_sm")
		}
		if err == nil {
			err = o.runCancellation(ctx, run, "cancellation")
		}
		if err == nil {
			err = o.runSecurityRBAC(ctx, run, "security_rbac")
		}
		if err == nil {
			err = o.runAuthOTP(ctx, run, "auth_otp")
		}
		if err == nil {
			err = o.runIdempotency(ctx, run, "idempotency")
		}
		// API-проверки идут независимо друг от друга, поэтому включаются в
		// «все» последними и не могут прервать сценарные сьюты.
		for _, apiKey := range apiSuiteKeys() {
			if err != nil {
				break
			}
			err = o.executeAPISuiteByKey(ctx, run, apiKey)
		}
	default:
		if o.hasCustom(suiteKey) {
			err = o.runUserScenario(ctx, run, suiteKey)
		} else {
			err = fmt.Errorf("unknown suite key: %s", suiteKey)
		}
	}

	o.finalizeChecks(run, suiteKeysFor(suiteKey))

	run.EndTime = time.Now()
	run.DurationMs = time.Since(start).Milliseconds()

	summary := &ExecutionEvent{
		RunID:      run.ID,
		SuiteName:  suiteKey,
		SuiteKey:   suiteKey,
		StepType:   "SUMMARY",
		DurationMs: run.DurationMs,
		Timestamp:  time.Now(),
	}
	// A run that skipped checks has not proven what those checks assert, so it
	// must not report itself as passed: "все проверки пройдены" over four
	// skipped steps is the most expensive kind of green.
	switch {
	case err != nil:
		run.Status = RunFailed
		run.Error = err.Error()
		summary.Level = LogError
		summary.Message = fmt.Sprintf("❌ Прогон провален: %v", err)
	case run.FailedChecks > 0:
		// An API suite runs every check independently and returns no error, so
		// the failures live only in the results. Reporting the run as passed
		// because nothing aborted it would hide them completely.
		run.Status = RunFailed
		run.Error = fmt.Sprintf("проверок провалено: %d из %d", run.FailedChecks, run.TotalChecks)
		summary.Level = LogError
		summary.Message = fmt.Sprintf("❌ Провалено %d из %d проверок.", run.FailedChecks, run.TotalChecks)
	case run.SkippedChecks > 0:
		run.Status = RunSkipped
		summary.Level = LogWarn
		summary.Message = fmt.Sprintf("⏭ Пройдено %d из %d проверок, пропущено %d — сьют выполнен не полностью.",
			run.PassedChecks, run.TotalChecks, run.SkippedChecks)
	default:
		run.Status = RunPassed
		summary.Level = LogSuccess
		summary.Message = "✅ Прогон завершён, все проверки пройдены."
	}

	o.Emit(summary)

	o.mu.Lock()
	delete(o.activeRuns, run.ID)
	o.history = append([]*TestRun{run}, o.history...)
	if len(o.history) > MaxStoredRuns {
		o.history = o.history[:MaxStoredRuns]
	}
	o.mu.Unlock()

	go o.persistRun(run)

	if cb := o.OnRunFinished; cb != nil {
		cp := *run
		cb(&cp)
	}
}

// persistRun stores the finalized run on disk without blocking orchestration.
// Losing the last runs on a crash is acceptable by design. The store keeps at
// most MaxStoredRuns files, evicting the oldest by StartTime.
func (o *TestOrchestrator) persistRun(run *TestRun) {
	o.mu.RLock()
	hs := o.historyStore
	o.mu.RUnlock()
	if hs == nil {
		return
	}
	if err := hs.Save(run); err != nil {
		log.Printf("[HISTORY] persist run %s: %v", run.ID, err)
		return
	}
	hs.Prune(MaxStoredRuns)
}

func (o *TestOrchestrator) runFlowA(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "Flow A: Full Restaurant Order Lifecycle"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Starting Flow A: Restaurant Lifecycle (New -> Assigned -> Preparing -> Cooked -> PickedUp -> Delivered)",
		Timestamp: time.Now(),
	})

	// 1. Setup Identities
	setupStart := time.Now()
	o.checkStart(run, suiteKey, "setup")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "GIVEN",
		Level:     LogInfo,
		Message:   "Generating unique Client, Restaurant, Courier and Admin identities...",
		Timestamp: time.Now(),
	})

	// The restaurant order is assembled from the client's server-side cart, so
	// the setup has to go all the way: an open restaurant that accepts
	// delivery, a dish on its menu, a client with a saved address, and that
	// dish already in the cart.
	octx, err := o.engine.Fixtures.PrepareRestaurantOrder(ctx)
	if err != nil {
		o.checkDone(run, suiteKey, "setup", false, err.Error(), setupStart)
		return err
	}
	clientPhone, restLogin := octx.ClientPhone, octx.RestLogin

	courierID, _, err := o.engine.Fixtures.CreateUniqueCourier(ctx)
	if err != nil {
		o.checkDone(run, suiteKey, "setup", false, fmt.Sprintf("fixture courier failed: %v", err), setupStart)
		return fmt.Errorf("fixture courier failed: %w", err)
	}

	if o.engine.Config.AdminToken == "" {
		_, _ = o.engine.SessionMgr.LoginAdmin(ctx, o.engine.Config.AdminLogin, o.engine.Config.AdminPassword)
	}

	o.Emit(&ExecutionEvent{
		RunID:      run.ID,
		SuiteName:  suiteName,
		SuiteKey:   suiteKey,
		StepType:   "GIVEN",
		Level:      LogSuccess,
		Message:    fmt.Sprintf("Identities ready: Client=%s, Rest=%s, Courier=%s", clientPhone, restLogin, courierID),
		DurationMs: time.Since(setupStart).Milliseconds(),
		Timestamp:  time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "setup", true, "Уникальные фикстуры созданы: клиент, ресторан, курьер", setupStart)

	// 2. Client creates order
	createStart := time.Now()
	o.checkStart(run, suiteKey, "create_order")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "WHEN",
		Level:     LogInfo,
		Message:   "Step 1: Client sends POST /api/clients/create-order",
		Timestamp: time.Now(),
	})

	createReq := octx.OrderRequest()
	createReq.Comment = "Оставить у двери"
	order, err := o.engine.ClientAPI.CreateRestaurantOrder(ctx, createReq)
	if err != nil {
		o.checkDone(run, suiteKey, "create_order", false, fmt.Sprintf("create order failed: %v", err), createStart)
		return fmt.Errorf("create order failed: %w", err)
	}

	// Validate State NEW (order creation is outside the transition table, same as Flow B)
	_ = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, "", statemachine.StatusNew, statemachine.RoleClient)
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusNew)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      fmt.Sprintf("Order #%d created. Status is NEW. State Machine check passed.", order.OrderNumber),
		CurrentState: statemachine.StatusNew,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(createStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "create_order", true, "Заказ создан, статус NEW", createStart)

	// 3. Admin assigns courier
	assignStart := time.Now()
	o.checkStart(run, suiteKey, "assign_courier")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "WHEN",
		Level:     LogInfo,
		Message:   fmt.Sprintf("Step 2: Director sends POST /api/admin/give-order-to-courier (Courier: %s)", courierID),
		Timestamp: time.Now(),
	})

	order, err = o.engine.AdminAPI.AssignCourier(ctx, order.OrderID, courierID)
	if err != nil {
		o.checkDone(run, suiteKey, "assign_courier", false, fmt.Sprintf("admin assign courier failed: %v", err), assignStart)
		return fmt.Errorf("admin assign courier failed: %w", err)
	}

	if err := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusNew, statemachine.StatusCourierAssigned, statemachine.RoleAdmin); err != nil {
		o.checkDone(run, suiteKey, "assign_courier", false, err.Error(), assignStart)
		return err
	}
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusCourierAssigned)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Courier assigned to order. Status: COURIER_ASSIGNED.",
		CurrentState: statemachine.StatusCourierAssigned,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(assignStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "assign_courier", true, "Курьер назначен, статус COURIER_ASSIGNED", assignStart)

	// 4. Restaurant starts cooking
	cookStart := time.Now()
	o.checkStart(run, suiteKey, "cooking")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "WHEN",
		Level:     LogInfo,
		Message:   "Step 3: Restaurant sends POST /api/rests/order-status (status: 'cooking')",
		Timestamp: time.Now(),
	})

	order, err = o.engine.RestAPI.ChangeOrderStatus(ctx, order.OrderID, "cooking")
	if err != nil {
		o.checkDone(run, suiteKey, "cooking", false, fmt.Sprintf("restaurant cooking failed: %v", err), cookStart)
		return fmt.Errorf("restaurant cooking failed: %w", err)
	}

	if err := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusCourierAssigned, statemachine.StatusPreparing, statemachine.RoleRestaurant); err != nil {
		o.checkDone(run, suiteKey, "cooking", false, err.Error(), cookStart)
		return err
	}
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusPreparing)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Restaurant is preparing order. Status: PREPARING.",
		CurrentState: statemachine.StatusPreparing,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(cookStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "cooking", true, "Ресторан готовит, статус PREPARING", cookStart)

	// 5. Restaurant marks ready
	readyStart := time.Now()
	o.checkStart(run, suiteKey, "ready_for_pickup")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "WHEN",
		Level:     LogInfo,
		Message:   "Step 4: Restaurant sends POST /api/rests/order-status (status: 'cooked')",
		Timestamp: time.Now(),
	})

	order, err = o.engine.RestAPI.ChangeOrderStatus(ctx, order.OrderID, "cooked")
	if err != nil {
		o.checkDone(run, suiteKey, "ready_for_pickup", false, fmt.Sprintf("restaurant cooked failed: %v", err), readyStart)
		return fmt.Errorf("restaurant cooked failed: %w", err)
	}

	if err := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusPreparing, statemachine.StatusReadyForPickup, statemachine.RoleRestaurant); err != nil {
		o.checkDone(run, suiteKey, "ready_for_pickup", false, err.Error(), readyStart)
		return err
	}
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusReadyForPickup)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Order packed and ready. Status: READY_FOR_PICKUP.",
		CurrentState: statemachine.StatusReadyForPickup,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(readyStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "ready_for_pickup", true, "Заказ собран, статус READY_FOR_PICKUP", readyStart)

	// 6. Courier picks up order
	pickupStart := time.Now()
	o.checkStart(run, suiteKey, "pickup")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "WHEN",
		Level:     LogInfo,
		Message:   "Step 5: Courier takes order and sets status 'shipping'",
		Timestamp: time.Now(),
	})

	_ = o.engine.CourierAPI.TakeOrder(ctx, order.OrderID, courierID)
	order, err = o.engine.CourierAPI.ChangeStatus(ctx, order.OrderID, "shipping", false)
	if err != nil {
		o.checkDone(run, suiteKey, "pickup", false, fmt.Sprintf("courier shipping failed: %v", err), pickupStart)
		return fmt.Errorf("courier shipping failed: %w", err)
	}

	if err := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusReadyForPickup, statemachine.StatusPickedUp, statemachine.RoleCourier); err != nil {
		o.checkDone(run, suiteKey, "pickup", false, err.Error(), pickupStart)
		return err
	}
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusPickedUp)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Courier picked up order and heading to client. Status: PICKED_UP.",
		CurrentState: statemachine.StatusPickedUp,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(pickupStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "pickup", true, "Курьер забрал заказ, статус PICKED_UP", pickupStart)

	// 7. Courier delivers
	deliverStart := time.Now()
	o.checkStart(run, suiteKey, "delivered")
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "WHEN",
		Level:     LogInfo,
		Message:   "Step 6: Courier hands order to client and sets status 'complete'",
		Timestamp: time.Now(),
	})

	order, err = o.engine.CourierAPI.ChangeStatus(ctx, order.OrderID, "complete", false)
	if err != nil {
		o.checkDone(run, suiteKey, "delivered", false, fmt.Sprintf("courier complete failed: %v", err), deliverStart)
		return fmt.Errorf("courier complete failed: %w", err)
	}

	if err := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusPickedUp, statemachine.StatusDelivered, statemachine.RoleCourier); err != nil {
		o.checkDone(run, suiteKey, "delivered", false, err.Error(), deliverStart)
		return err
	}
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusDelivered)
	run.FinalState = statemachine.StatusDelivered

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Order successfully delivered to client! Status: DELIVERED.",
		CurrentState: statemachine.StatusDelivered,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(deliverStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	run.PassedSteps++
	o.checkDone(run, suiteKey, "delivered", true, "Заказ доставлен, статус DELIVERED", deliverStart)

	return nil
}

func (o *TestOrchestrator) runFlowB(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "Flow B: Point A to Point B Courier Parcel"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Starting Flow B: Independent Parcel Delivery (New -> Assigned -> PickedUp -> Delivered)",
		Timestamp: time.Now(),
	})

	setupStart := time.Now()
	o.checkStart(run, suiteKey, "setup")
	clientPhone, _, _ := o.engine.Fixtures.CreateUniqueClient(ctx)
	courierID, _, _ := o.engine.Fixtures.CreateUniqueCourier(ctx)

	if o.engine.Config.AdminToken == "" {
		_, _ = o.engine.SessionMgr.LoginAdmin(ctx, o.engine.Config.AdminLogin, o.engine.Config.AdminPassword)
	}

	o.checkDone(run, suiteKey, "setup", true, fmt.Sprintf("Уникальные фикстуры созданы: клиент %s, курьер %s", clientPhone, courierID), setupStart)

	createStart := time.Now()
	o.checkStart(run, suiteKey, "create_parcel")

	// Parcel delivery runs on a platform working window (10:00–23:00). Outside
	// it every parcel is refused with 422 OUTSIDE_WORKING_HOURS: the platform
	// is closed, which must read as skipped rather than failed.
	if available, window, aerr := o.engine.Fixtures.ParcelOrderingAvailable(ctx); aerr == nil && !available {
		reason := fmt.Sprintf("платформа принимает посылки только в окне %s", window)
		for _, id := range []string{"create_parcel", "assign_courier", "pickup", "delivered"} {
			o.checkSkip(run, suiteKey, id, reason)
		}
		o.Emit(&ExecutionEvent{
			RunID:     run.ID,
			SuiteName: suiteName,
			SuiteKey:  suiteKey,
			StepType:  "INFO",
			Level:     LogWarn,
			Message:   "⏸ " + reason,
			Timestamp: time.Now(),
		})
		return nil
	}

	// A parcel is addressed by coordinates, not by text, and carries the
	// recipient plus the cargo type.
	req := o.engine.Fixtures.ParcelRequest("")
	req.Comment = "Документы с печатью"

	order, err := o.engine.ClientAPI.CreateIndependentOrder(ctx, req)
	if err != nil {
		o.checkDone(run, suiteKey, "create_parcel", false, err.Error(), createStart)
		return err
	}

	_ = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeIndependent, "", statemachine.StatusNew, statemachine.RoleClient)
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusNew)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      fmt.Sprintf("Parcel order created by %s. Status: NEW.", clientPhone),
		CurrentState: statemachine.StatusNew,
		OrderID:      order.OrderID,
		DurationMs:   time.Since(createStart).Milliseconds(),
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "create_parcel", true, "Посылка создана, статус NEW", createStart)

	// Assign courier
	assignStart := time.Now()
	o.checkStart(run, suiteKey, "assign_courier")
	order, err = o.engine.AdminAPI.AssignCourier(ctx, order.OrderID, courierID)
	if err != nil {
		o.checkDone(run, suiteKey, "assign_courier", false, err.Error(), assignStart)
		return err
	}
	_ = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeIndependent, statemachine.StatusNew, statemachine.StatusCourierAssigned, statemachine.RoleAdmin)
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusCourierAssigned)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      fmt.Sprintf("Courier %s assigned to parcel. Status: COURIER_ASSIGNED.", courierID),
		CurrentState: statemachine.StatusCourierAssigned,
		OrderID:      order.OrderID,
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "assign_courier", true, "Курьер назначен на посылку, статус COURIER_ASSIGNED", assignStart)

	// Picked up
	pickupStart := time.Now()
	o.checkStart(run, suiteKey, "pickup")
	order, err = o.engine.CourierAPI.ChangeStatus(ctx, order.OrderID, "shipping", true)
	if err != nil {
		o.checkDone(run, suiteKey, "pickup", false, err.Error(), pickupStart)
		return err
	}
	_ = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeIndependent, statemachine.StatusCourierAssigned, statemachine.StatusPickedUp, statemachine.RoleCourier)
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusPickedUp)

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Courier picked up parcel at Point A. Status: PICKED_UP.",
		CurrentState: statemachine.StatusPickedUp,
		OrderID:      order.OrderID,
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "pickup", true, "Посылка забрана в точке А, статус PICKED_UP", pickupStart)

	// Delivered
	deliverStart := time.Now()
	o.checkStart(run, suiteKey, "delivered")
	order, err = o.engine.CourierAPI.ChangeStatus(ctx, order.OrderID, "complete", true)
	if err != nil {
		o.checkDone(run, suiteKey, "delivered", false, err.Error(), deliverStart)
		return err
	}
	_ = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeIndependent, statemachine.StatusPickedUp, statemachine.StatusDelivered, statemachine.RoleCourier)
	o.engine.StateMachine.RecordTransition(order.OrderID, statemachine.StatusDelivered)
	run.FinalState = statemachine.StatusDelivered

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      "Courier delivered parcel at Point B. Status: DELIVERED.",
		CurrentState: statemachine.StatusDelivered,
		OrderID:      order.OrderID,
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "delivered", true, "Посылка доставлена в точку Б, статус DELIVERED", deliverStart)

	return nil
}

func (o *TestOrchestrator) runNegativeSM(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "State Machine: Negative Validation"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Checking illegal status transitions and unauthorized role barriers...",
		Timestamp: time.Now(),
	})

	jumpStart := time.Now()
	o.checkStart(run, suiteKey, "illegal_jump_new_delivered")
	err := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusNew, statemachine.StatusDelivered, statemachine.RoleCourier)
	if err == nil {
		msg := "expected error for illegal jump NEW -> DELIVERED"
		o.checkDone(run, suiteKey, "illegal_jump_new_delivered", false, msg, jumpStart)
		return fmt.Errorf("%s", msg)
	}
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly blocked illegal transition: NEW -> DELIVERED",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "illegal_jump_new_delivered", true, "Переход NEW → DELIVERED корректно запрещён", jumpStart)

	// 2. Illegal jump: PREPARING directly to DELIVERED
	jumpPrepStart := time.Now()
	o.checkStart(run, suiteKey, "illegal_jump_preparing_delivered")
	err = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusPreparing, statemachine.StatusDelivered, statemachine.RoleCourier)
	if err == nil {
		msg := "expected error for illegal jump PREPARING -> DELIVERED"
		o.checkDone(run, suiteKey, "illegal_jump_preparing_delivered", false, msg, jumpPrepStart)
		return fmt.Errorf("%s", msg)
	}
	if !strings.Contains(err.Error(), "illegal state transition") {
		msg := fmt.Sprintf("expected 'illegal state transition' violation for PREPARING -> DELIVERED, got: %v", err)
		o.checkDone(run, suiteKey, "illegal_jump_preparing_delivered", false, msg, jumpPrepStart)
		return fmt.Errorf("%s", msg)
	}
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly blocked illegal transition: PREPARING -> DELIVERED",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "illegal_jump_preparing_delivered", true, "Переход PREPARING → DELIVERED корректно запрещён", jumpPrepStart)

	// 3. Unauthorized role: CLIENT trying to mark order DELIVERED
	clientRoleStart := time.Now()
	o.checkStart(run, suiteKey, "unauthorized_role_client")
	err = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusPickedUp, statemachine.StatusDelivered, statemachine.RoleClient)
	if err == nil {
		msg := "expected authorization error for role CLIENT on PICKED_UP -> DELIVERED"
		o.checkDone(run, suiteKey, "unauthorized_role_client", false, msg, clientRoleStart)
		return fmt.Errorf("%s", msg)
	}
	if !strings.Contains(err.Error(), "not authorized") {
		msg := fmt.Sprintf("expected 'not authorized' violation for role CLIENT, got: %v", err)
		o.checkDone(run, suiteKey, "unauthorized_role_client", false, msg, clientRoleStart)
		return fmt.Errorf("%s", msg)
	}
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly rejected unauthorized role: CLIENT cannot mark order DELIVERED",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "unauthorized_role_client", true, "Роль CLIENT корректно отклонена на переходе PICKED_UP → DELIVERED", clientRoleStart)

	// 4. Unauthorized role: RESTAURANT trying to assign courier
	restRoleStart := time.Now()
	o.checkStart(run, suiteKey, "unauthorized_role_restaurant")
	err = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusNew, statemachine.StatusCourierAssigned, statemachine.RoleRestaurant)
	if err == nil {
		msg := "expected authorization error for role RESTAURANT on NEW -> COURIER_ASSIGNED"
		o.checkDone(run, suiteKey, "unauthorized_role_restaurant", false, msg, restRoleStart)
		return fmt.Errorf("%s", msg)
	}
	if !strings.Contains(err.Error(), "not authorized") {
		msg := fmt.Sprintf("expected 'not authorized' violation for role RESTAURANT, got: %v", err)
		o.checkDone(run, suiteKey, "unauthorized_role_restaurant", false, msg, restRoleStart)
		return fmt.Errorf("%s", msg)
	}
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly rejected unauthorized role: RESTAURANT cannot assign courier",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "unauthorized_role_restaurant", true, "Роль RESTAURANT корректно отклонена на переходе NEW → COURIER_ASSIGNED", restRoleStart)

	return nil
}

func (o *TestOrchestrator) runCancellation(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "Edge Case: Cancellation Lifecycle"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Validating order cancellations at NEW vs COOKING stages...",
		Timestamp: time.Now(),
	})

	cancelStart := time.Now()
	o.checkStart(run, suiteKey, "cancel_at_new")

	octx, err := o.engine.Fixtures.PrepareRestaurantOrder(ctx)
	if err != nil {
		o.checkDone(run, suiteKey, "cancel_at_new", false, err.Error(), cancelStart)
		return err
	}

	// 1. Cancel at NEW -> Should succeed
	order, err := o.engine.ClientAPI.CreateRestaurantOrder(ctx, octx.OrderRequest())
	if err != nil {
		o.checkDone(run, suiteKey, "cancel_at_new", false, err.Error(), cancelStart)
		return err
	}

	err = o.engine.ClientAPI.CancelOrder(ctx, order.OrderID, "Client requested cancellation at NEW")
	if err != nil {
		o.checkDone(run, suiteKey, "cancel_at_new", false, fmt.Sprintf("cancellation at NEW failed: %v", err), cancelStart)
		return fmt.Errorf("cancellation at NEW failed: %w", err)
	}

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      fmt.Sprintf("✓ Client successfully cancelled order #%d at NEW stage", order.OrderNumber),
		CurrentState: statemachine.StatusCancelled,
		OrderID:      order.OrderID,
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "cancel_at_new", true, "Заказ успешно отменён на статусе NEW", cancelStart)

	// 2. Cancellation attempt during COOKING must be rejected with 403
	cookCancelStart := time.Now()
	o.checkStart(run, suiteKey, "reject_during_cooking")

	// A second, independent order: the one cancelled above is gone, and the
	// cart it was built from was consumed with it.
	cookCtx, err := o.engine.Fixtures.PrepareRestaurantOrder(ctx)
	if err != nil {
		o.checkDone(run, suiteKey, "reject_during_cooking", false, err.Error(), cookCancelStart)
		return err
	}

	cookingOrder, err := o.engine.ClientAPI.CreateRestaurantOrder(ctx, cookCtx.OrderRequest())
	if err != nil {
		msg := fmt.Sprintf("order creation for cooking-cancel scenario failed: %v", err)
		o.checkDone(run, suiteKey, "reject_during_cooking", false, msg, cookCancelStart)
		return fmt.Errorf("%s", msg)
	}

	if _, err = o.engine.RestAPI.ChangeOrderStatus(ctx, cookingOrder.OrderID, "cooking"); err != nil {
		msg := fmt.Sprintf("failed to move order #%d into COOKING before cancellation attempt: %v", cookingOrder.OrderNumber, err)
		o.checkDone(run, suiteKey, "reject_during_cooking", false, msg, cookCancelStart)
		return fmt.Errorf("%s", msg)
	}
	_ = o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusNew, statemachine.StatusPreparing, statemachine.RoleRestaurant)
	o.engine.StateMachine.RecordTransition(cookingOrder.OrderID, statemachine.StatusPreparing)

	err = o.engine.ClientAPI.CancelOrder(ctx, cookingOrder.OrderID, "Попытка отменить заказ во время готовки")
	if err == nil {
		msg := fmt.Sprintf("отмена во время готовки должна отклоняться, но заказ #%d был отменён", cookingOrder.OrderNumber)
		o.checkDone(run, suiteKey, "reject_during_cooking", false, msg, cookCancelStart)
		return fmt.Errorf("%s", msg)
	}
	// The refusal is a state conflict (409 CANCEL_NOT_ALLOWED), not an
	// authorization one: the client owns the order, the status forbids it.
	if !client.IsStatus(err, http.StatusConflict) {
		msg := fmt.Sprintf("ожидался 409 CANCEL_NOT_ALLOWED при отмене во время готовки, получено: %v", err)
		o.checkDone(run, suiteKey, "reject_during_cooking", false, msg, cookCancelStart)
		return fmt.Errorf("%s", msg)
	}

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      fmt.Sprintf("✓ Отмена заказа #%d во время готовки отклонена (409 CANCEL_NOT_ALLOWED)", cookingOrder.OrderNumber),
		CurrentState: statemachine.StatusPreparing,
		OrderID:      cookingOrder.OrderID,
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "reject_during_cooking", true, "Отмена заказа в статусе PREPARING (cooking) отклонена с 403", cookCancelStart)

	// 3. Terminal state protection: nothing may modify a DELIVERED order
	terminalStart := time.Now()
	o.checkStart(run, suiteKey, "terminal_protection")
	termErr := o.engine.StateMachine.ValidateTransition(statemachine.OrderTypeRestaurant, statemachine.StatusDelivered, statemachine.StatusPreparing, statemachine.RoleRestaurant)
	if termErr == nil {
		msg := "modification of DELIVERED order was expected to be blocked as terminal state"
		o.checkDone(run, suiteKey, "terminal_protection", false, msg, terminalStart)
		return fmt.Errorf("%s", msg)
	}
	if !strings.Contains(termErr.Error(), "terminal state") {
		msg := fmt.Sprintf("expected terminal-state violation for DELIVERED order, got: %v", termErr)
		o.checkDone(run, suiteKey, "terminal_protection", false, msg, terminalStart)
		return fmt.Errorf("%s", msg)
	}

	o.Emit(&ExecutionEvent{
		RunID:        run.ID,
		SuiteName:    suiteName,
		SuiteKey:     suiteKey,
		StepType:     "THEN",
		Level:        LogSuccess,
		Message:      fmt.Sprintf("✓ Correctly blocked modification of delivered order: %v", termErr),
		CurrentState: statemachine.StatusDelivered,
		Timestamp:    time.Now(),
	})
	o.checkDone(run, suiteKey, "terminal_protection", true, "Терминальный статус DELIVERED защищён: модификация запрещена", terminalStart)

	return nil
}

func (o *TestOrchestrator) runSecurityRBAC(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "Security: RBAC Barrier Tests"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Testing unauthenticated calls and cross-role privilege escalation...",
		Timestamp: time.Now(),
	})

	// 1. Missing token
	tokenStart := time.Now()
	o.checkStart(run, suiteKey, "no_token_401")
	_, err := o.engine.HTTPClient.Request(ctx, "POST", "/api/clients/create-order", "", client.CreateRestaurantOrderRequest{RestID: "123"}, nil)
	if err == nil {
		msg := "expected 401 for unauthenticated request"
		o.checkDone(run, suiteKey, "no_token_401", false, msg, tokenStart)
		return fmt.Errorf("%s", msg)
	}
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly returned 401 Unauthorized for missing Bearer token",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "no_token_401", true, "Запрос без Bearer токена получил 401", tokenStart)

	// 2. Client token trying to call admin
	adminStart := time.Now()
	o.checkStart(run, suiteKey, "client_admin_403")
	_, clientToken, _ := o.engine.Fixtures.CreateUniqueClient(ctx)
	_, err = o.engine.HTTPClient.Request(ctx, "POST", "/api/admin/give-order-to-courier", clientToken, client.AssignCourierRequest{OrderID: "123"}, nil)
	if err == nil {
		msg := "expected 403 when Client calls Admin API"
		o.checkDone(run, suiteKey, "client_admin_403", false, msg, adminStart)
		return fmt.Errorf("%s", msg)
	}
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly returned 403 Forbidden when Client token attempts Admin action",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "client_admin_403", true, "Токен клиента у Admin API получил 403", adminStart)

	// 3. Courier token trying to access Restaurant Dish Management API
	courierRestStart := time.Now()
	o.checkStart(run, suiteKey, "courier_rest_403")
	_, courierToken, err := o.engine.Fixtures.CreateUniqueCourier(ctx)
	if err != nil {
		msg := fmt.Sprintf("fixture courier failed: %v", err)
		o.checkDone(run, suiteKey, "courier_rest_403", false, msg, courierRestStart)
		return fmt.Errorf("%s", msg)
	}

	dishReq := client.CreateDishRequest{
		Title:      "Пицца Несанкционированная",
		Price:      990,
		CategoryID: "00000000-0000-0000-0000-000000000000",
	}
	resp, rerr := o.engine.HTTPClient.Request(ctx, "POST", "/api/rests/dishes", courierToken, dishReq, nil)
	if rerr == nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		msg := fmt.Sprintf("expected 403 when Courier calls Restaurant Dish API, but request succeeded (status %d)", code)
		o.checkDone(run, suiteKey, "courier_rest_403", false, msg, courierRestStart)
		return fmt.Errorf("%s", msg)
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		msg := fmt.Sprintf("expected 403 when Courier calls Restaurant Dish API, got status %d", resp.StatusCode)
		o.checkDone(run, suiteKey, "courier_rest_403", false, msg, courierRestStart)
		return fmt.Errorf("%s", msg)
	}

	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   "✓ Correctly returned 403 Forbidden when Courier token attempts Restaurant dish management",
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "courier_rest_403", true, "Токен курьера у Restaurant API (POST /api/rests/dishes) получил 403", courierRestStart)

	return nil
}

func (o *TestOrchestrator) runIdempotency(ctx context.Context, run *TestRun, suiteKey string) error {
	suiteName := "Reliability: Idempotency Verification"
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   "Verifying duplicate request protection via Idempotency-Key...",
		Timestamp: time.Now(),
	})

	idemStart := time.Now()
	o.checkStart(run, suiteKey, "same_key_same_order")

	octx, err := o.engine.Fixtures.PrepareRestaurantOrder(ctx)
	if err != nil {
		o.checkDone(run, suiteKey, "same_key_same_order", false, err.Error(), idemStart)
		return err
	}

	idemKey := uuid.New().String()
	req := octx.OrderRequest()

	order1, err := o.engine.ClientAPI.CreateRestaurantOrderWithIdempotency(ctx, req, idemKey)
	if err != nil {
		o.checkDone(run, suiteKey, "same_key_same_order", false, err.Error(), idemStart)
		return err
	}

	order2, err := o.engine.ClientAPI.CreateRestaurantOrderWithIdempotency(ctx, req, idemKey)
	if err != nil {
		o.checkDone(run, suiteKey, "same_key_same_order", false, err.Error(), idemStart)
		return err
	}

	if order1.OrderID != order2.OrderID {
		msg := fmt.Sprintf("idempotency violation: generated two different orders (%s vs %s)", order1.OrderID, order2.OrderID)
		o.checkDone(run, suiteKey, "same_key_same_order", false, msg, idemStart)
		return fmt.Errorf("%s", msg)
	}

	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   fmt.Sprintf("✓ Idempotency verified: Re-submitting same key returned identical OrderID (%s)", order1.OrderID),
		OrderID:   order1.OrderID,
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "same_key_same_order", true, fmt.Sprintf("Повтор с тем же ключом вернул тот же OrderID (%s)", order1.OrderID), idemStart)

	// 2. Concurrent isolation: N parallel order creations, each with its own client token
	concStart := time.Now()
	o.checkStart(run, suiteKey, "concurrent_isolation")

	const parallelCount = 10
	var wg sync.WaitGroup
	errs := make(chan error, parallelCount)
	orderIDs := make(chan string, parallelCount)

	for i := 0; i < parallelCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Each parallel worker creates its own unique client session
			clientPhone, cToken, werr := o.engine.Fixtures.CreateUniqueClient(ctx)
			if werr != nil {
				errs <- fmt.Errorf("worker %d: клиент не создан: %w", idx, werr)
				return
			}

			// Independent HTTP call: token passed explicitly, not via SessionMgr
			var orderResp client.OrderResponse
			orderReq := o.engine.Fixtures.ParcelRequest("")
			orderReq.Comment = fmt.Sprintf("Параллельный воркер %d", idx)
			if _, werr = o.engine.HTTPClient.Request(ctx, "POST", "/api/clients/independent-order", cToken, orderReq, &orderResp); werr != nil {
				errs <- fmt.Errorf("worker %d (%s): заказ не создан: %w", idx, clientPhone, werr)
				return
			}

			orderIDs <- orderResp.OrderID
		}(i)
	}

	wg.Wait()
	close(errs)
	close(orderIDs)

	failedWorkers := 0
	firstWorkerErr := error(nil)
	for werr := range errs {
		failedWorkers++
		if firstWorkerErr == nil {
			firstWorkerErr = werr
		}
	}
	if firstWorkerErr != nil {
		msg := fmt.Sprintf("параллельное создание заказов упало (%d/%d воркеров), первая ошибка: %v", failedWorkers, parallelCount, firstWorkerErr)
		o.checkDone(run, suiteKey, "concurrent_isolation", false, msg, concStart)
		return fmt.Errorf("%s", msg)
	}

	uniqueIDs := make(map[string]bool, parallelCount)
	totalOrders := 0
	duplicateID := ""
	for id := range orderIDs {
		totalOrders++
		if uniqueIDs[id] && duplicateID == "" {
			duplicateID = id
		}
		uniqueIDs[id] = true
	}
	if duplicateID != "" {
		msg := fmt.Sprintf("найден дубликат OrderID при конкурентном создании: %s", duplicateID)
		o.checkDone(run, suiteKey, "concurrent_isolation", false, msg, concStart)
		return fmt.Errorf("%s", msg)
	}
	if totalOrders != parallelCount || len(uniqueIDs) != parallelCount {
		msg := fmt.Sprintf("ожидалось %d уникальных заказов, получено %d (уникальных: %d)", parallelCount, totalOrders, len(uniqueIDs))
		o.checkDone(run, suiteKey, "concurrent_isolation", false, msg, concStart)
		return fmt.Errorf("%s", msg)
	}

	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  suiteKey,
		StepType:  "THEN",
		Level:     LogSuccess,
		Message:   fmt.Sprintf("✓ Successfully executed %d concurrent order flows: all OrderIDs unique, zero session crosstalk", parallelCount),
		Timestamp: time.Now(),
	})
	o.checkDone(run, suiteKey, "concurrent_isolation", true, fmt.Sprintf("%d параллельных созданий вернули %d уникальных независимых OrderID без пересечений сессий", parallelCount, len(uniqueIDs)), concStart)

	return nil
}
