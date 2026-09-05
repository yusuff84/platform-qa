package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"locali-e2e-engine/config"
	"locali-e2e-engine/pkg/analysis"
	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/dsl"
	"locali-e2e-engine/pkg/mobilecontract"
	"locali-e2e-engine/pkg/registry"
	"locali-e2e-engine/pkg/report"
	"locali-e2e-engine/pkg/runner"
	"locali-e2e-engine/pkg/scenario"
	"locali-e2e-engine/pkg/stands"
	"locali-e2e-engine/pkg/spec"
	"locali-e2e-engine/testserver"
)

// Server is the HTTP platform server hosting the Web Admin Panel and REST/SSE APIs
type Server struct {
	cfg          *config.Config
	engine       *dsl.Engine
	orchestrator *runner.TestOrchestrator
	mockBackend  *testserver.MockBackend
	store        *scenario.Store
	stands       *stands.Store
	specs        *spec.Store
	webDir       string

	// Per-stand token vault state. lastStandID tracks the stand that owns the
	// SessionMgr contents right now ("" before the first applyStand); vaultMu
	// serializes vault save/restore so concurrent applyStand calls can never
	// race on the snapshot files.
	vaultDir    string
	vaultMu     sync.Mutex
	lastStandID string

	// groups tracks in-flight grouped batches (regression/webhook): how many
	// runs are still pending per GroupID and the finished run snapshots, so a
	// single Telegram digest is sent when the whole group completes.
	groups groupTracker

	// Mobile Contract DTO Compatibility state
	mobileMu     sync.RWMutex
	mobileReport *mobilecontract.CompatibilityReport
	mobileApps   *mobilecontract.AppStore
}

// groupTracker accumulates finished runs of grouped batches and fires exactly
// once per GroupID — when its last pending run finishes. Registration happens
// in handleRuns before execution starts; finish is called from the
// OnRunFinished hook for every completed grouped run.
type groupTracker struct {
	mu      sync.Mutex
	pending map[string]int
	runs    map[string][]*runner.TestRun
}

// register reserves a fresh GroupID for an expected number of runs.
func (t *groupTracker) register(expected int) string {
	id := uuid.NewString()
	t.mu.Lock()
	if t.pending == nil {
		t.pending = make(map[string]int)
		t.runs = make(map[string][]*runner.TestRun)
	}
	t.pending[id] = expected
	t.runs[id] = make([]*runner.TestRun, 0, expected)
	t.mu.Unlock()
	return id
}

// discard forgets a registered group whose runs never executed (e.g. invalid
// suite keys rejected before any run started).
func (t *groupTracker) discard(groupID string) {
	t.mu.Lock()
	delete(t.pending, groupID)
	delete(t.runs, groupID)
	t.mu.Unlock()
}

// finish records a completed run of its group and returns snapshots of the
// entire group when it was the last pending one; nil otherwise. Unknown groups
// (e.g. engine restarted mid-batch) yield no digest.
func (t *groupTracker) finish(run *runner.TestRun) []*runner.TestRun {
	if run == nil || run.GroupID == "" {
		return nil
	}
	cp := *run
	t.mu.Lock()
	defer t.mu.Unlock()
	left, ok := t.pending[run.GroupID]
	if !ok {
		return nil
	}
	group := t.runs[run.GroupID]
	group = append(group, &cp)
	t.runs[run.GroupID] = group
	left--
	if left > 0 {
		t.pending[run.GroupID] = left
		return nil
	}
	delete(t.pending, run.GroupID)
	delete(t.runs, run.GroupID)
	return group
}

// NewServer initializes the platform server
func NewServer(cfg *config.Config, webDir string) (*Server, error) {
	// If BaseURL is empty or default, launch embedded mock backend
	var mock *testserver.MockBackend
	if cfg.BaseURL == "" || cfg.BaseURL == "http://localhost:3000" {
		mock = testserver.NewMockBackend()
		cfg.BaseURL = mock.URL()
		log.Printf("[SERVER] Started embedded Locali Mock Backend on %s", cfg.BaseURL)
	}

	engine, err := dsl.NewEngine(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to init engine: %w", err)
	}

	// Initialize default admin token in vault if present
	if cfg.AdminToken != "" {
		engine.SessionMgr.AddToken("admin", "Default Admin (Env)", cfg.AdminLogin, cfg.AdminToken, true)
	}

	orchestrator := runner.NewTestOrchestrator(engine)

	// Manual verification-code flow: on a real stand without a universal code,
	// fixtures pause mid-run on client login awaiting the operator. The
	// embedded mock accepts any code, so there the callback auto-satisfies the
	// request instead of surfacing a prompt (mock batches must never stop).
	engine.Fixtures.SetOnInputRequest(func(runID, phone string) {
		if mock != nil {
			orchestrator.InputBroker.Deliver(runID, "1234")
			return
		}
		orchestrator.Emit(&runner.ExecutionEvent{
			RunID:      runID,
			SuiteName:  "Manual Input",
			StepType:   "INPUT_REQUIRED",
			Level:      runner.LogWarn,
			Message:    fmt.Sprintf("Ожидание кода верификации для %s — введите код из Telegram", phone),
			InputPhone: phone,
			Timestamp:  time.Now(),
		})
	})

	// User-defined scenarios storage (JSON files under <DataDir>/scenarios)
	scenarioStore, err := scenario.NewStore(filepath.Join(cfg.DataDir, "scenarios"))
	if err != nil {
		return nil, fmt.Errorf("failed to init scenario store: %w", err)
	}
	orchestrator.SetCustomSource(scenarioStore)

	// Restore persisted run history from <DataDir>/runs (survives restarts).
	if err := orchestrator.LoadHistory(cfg.DataDir); err != nil {
		log.Printf("[SERVER] Run history restore skipped: %v", err)
	}

	// Multi-stand targets storage (<DataDir>/stands.json)
	var defaultStand stands.Stand
	if mock != nil {
		defaultStand = stands.Stand{Name: "Встроенный мок", BaseURL: cfg.BaseURL, IsMock: true}
	} else {
		defaultStand = stands.Stand{Name: "Основной стенд", BaseURL: cfg.BaseURL}
	}
	standsStore, err := stands.NewStore(cfg.DataDir, defaultStand)
	if err != nil {
		return nil, fmt.Errorf("failed to init stands store: %w", err)
	}

	// Imported OpenAPI/Swagger specification (<DataDir>/specs) and its
	// generated smoke scenarios.
	specStore, err := spec.NewStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to init spec store: %w", err)
	}

	// Mobile applications registry (<DataDir>/mobile_apps.json)
	mobileAppStore, err := mobilecontract.NewAppStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to init mobile apps store: %w", err)
	}

	srv := &Server{
		cfg:          cfg,
		engine:       engine,
		orchestrator: orchestrator,
		mockBackend:  mock,
		store:        scenarioStore,
		stands:       standsStore,
		specs:        specStore,
		webDir:       webDir,
		vaultDir:     stands.VaultsDir(cfg.DataDir),
		mobileApps:   mobileAppStore,
	}

	if act, ok := standsStore.Active(); ok {
		if act.IsMock && mock != nil && act.BaseURL != mock.URL() {
			act.BaseURL = mock.URL()
			_ = standsStore.ResyncMock(act.ID, act.BaseURL)
		}
		srv.applyStand(act)
	}

	// Telegram notifications: manual runs alert individually on failure,
	// grouped triggers (regression/webhook) produce exactly ONE combined
	// digest when the last run of the group finishes. Fire-and-forget, never
	// affects run results.
	orchestrator.OnRunFinished = func(run *runner.TestRun) {
		srv.notifyRunFinished(run)
	}

	return srv, nil
}

// notifyRunFinished decides between a single failure alert (manual or legacy
// untagged runs) and the grouped digest path (regression/webhook): the group
// tracker fires SendDigest once, after its last pending run completes.
func (s *Server) notifyRunFinished(run *runner.TestRun) {
	if run == nil {
		return
	}
	switch run.Trigger {
	case runner.TriggerRegression, runner.TriggerWebhook:
		groupRuns := s.groups.finish(run)
		if groupRuns == nil {
			return
		}
		baseURL := s.cfg.BaseURL
		go func() {
			report.SendDigest(context.Background(), s.cfg, report.DigestText(groupRuns, baseURL))
		}()
	default: // manual / empty (legacy runs)
		if run.Status != "FAILED" {
			return
		}
		go report.NotifyFailure(context.Background(), s.cfg, run)
	}
}

// Start starts listening on given port
func (s *Server) Start(port int) error {
	mux := http.NewServeMux()

	// System & Config
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)

	// Multi-Token Vault Endpoints
	mux.HandleFunc("/api/tokens/vault", s.handleTokenVault)
	mux.HandleFunc("/api/tokens/add", s.handleTokenAdd)
	mux.HandleFunc("/api/tokens/activate", s.handleTokenActivate)
	mux.HandleFunc("/api/tokens/delete", s.handleTokenDelete)
	mux.HandleFunc("/api/tokens/auth-login", s.handleTokenAuthLogin)
	mux.HandleFunc("/api/tokens/register", s.handleTokenRegister)
	mux.HandleFunc("/api/tokens/preset", s.handleTokenPreset)
	mux.HandleFunc("/api/fixtures/accounts", s.handleFixtureAccounts)

	// Sessions & Runners
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/generate", s.handleGenerateSession)
	mux.HandleFunc("/api/sessions/token", s.handleSetToken)
	mux.HandleFunc("/api/suites", s.handleSuites)
	mux.HandleFunc("/api/runs", s.handleRuns)
	// Export routes must be registered with longer (more specific) patterns:
	// ServeMux picks the most specific one over the "/api/runs/" prefix below.
	mux.HandleFunc("/api/runs/{id}/export/junit", s.handleExportJUnit)
	mux.HandleFunc("/api/runs/{id}/export/allure", s.handleExportAllure)
	mux.HandleFunc("POST /api/runs/{id}/input-code", s.handleRunInputCode)
	mux.HandleFunc("/api/runs/", s.handleRunByID)
	mux.HandleFunc("/api/events", s.handleEventsSSE)
	mux.HandleFunc("/api/events/recent", s.handleRecentEvents)
	mux.HandleFunc("/api/manual/action", s.handleManualAction)

	// User-defined custom scenarios
	mux.HandleFunc("/api/scenarios", s.handleScenarios)
	mux.HandleFunc("/api/scenarios/replenish", s.handleScenariosReplenish)
	mux.HandleFunc("/api/scenarios/", s.handleScenarioByID)

	// OpenAPI/Swagger import & generated smoke scenarios
	mux.HandleFunc("/api/spec/import", s.handleSpecImport)
	mux.HandleFunc("/api/spec/current", s.handleSpecCurrent)
	mux.HandleFunc("/api/spec/regenerate", s.handleSpecRegenerate)

	// Platform Analysis & Metrics
	mux.HandleFunc("/api/analysis/coverage", s.handleAnalysisCoverage)
	mux.HandleFunc("/api/analysis/metrics", s.handleAnalysisMetrics)
	mux.HandleFunc("/api/analysis/diagnostics/", s.handleAnalysisDiagnostics)
	mux.HandleFunc("/api/analysis/diagnostics", s.handleAnalysisDiagnostics)

	// Stand targets (multi-backend switching)
	mux.HandleFunc("/api/stands", s.handleStands)
	mux.HandleFunc("/api/stands/", s.handleStandByID)

	// Mobile Contract DTO Compatibility (Flutter, iOS Swift, Android Kotlin)
	mux.HandleFunc("/api/mobilecontract/scan", s.handleMobileContractScan)
	mux.HandleFunc("/api/mobilecontract/report", s.handleMobileContractReport)
	mux.HandleFunc("/api/mobilecontract/gitlab/branches", s.handleMobileGitLabBranches)
	mux.HandleFunc("/api/mobilecontract/gitlab/scan", s.handleMobileGitLabScan)
	mux.HandleFunc("/api/mobilecontract/gitlab/config", s.handleMobileGitLabConfig)
	mux.HandleFunc("/api/mobilecontract/apps/scan-all", s.handleMobileAppsScanAll)
	mux.HandleFunc("/api/mobilecontract/apps", s.handleMobileApps)
	mux.HandleFunc("/api/mobilecontract/apps/", s.handleMobileAppByID)

	// Static Web Admin UI
	fileServer := http.FileServer(http.Dir(s.webDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(s.webDir, r.URL.Path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(s.webDir, "index.html"))
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf(":%d", port)
	log.Printf("================================================================")
	log.Printf("🚀 Locali E2E Test Platform Admin UI: http://localhost:%d", port)
	log.Printf("🎯 Target Backend Base URL: %s", s.cfg.BaseURL)
	log.Printf("================================================================")

	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"status":     "online",
		"baseURL":    s.cfg.BaseURL,
		"adminLogin": s.cfg.AdminLogin,
		"hasAdmin":   s.engine.SessionMgr.GetAdminSession().Token != "",
		"hasClient":  s.engine.SessionMgr.GetClientSession().Token != "",
		"hasRest":    s.engine.SessionMgr.GetRestSession().Token != "",
		"hasCourier": s.engine.SessionMgr.GetCourierSession().Token != "",
		"isMockMode": s.mockBackend != nil,
	}
	if act, ok := s.stands.Active(); ok {
		resp["standId"] = act.ID
		resp["standName"] = act.Name
	} else {
		resp["standId"] = ""
		resp["standName"] = ""
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var req struct {
			BaseURL       string `json:"baseURL"`
			AdminLogin    string `json:"adminLogin"`
			AdminPassword string `json:"adminPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.BaseURL != "" {
			s.cfg.BaseURL = req.BaseURL
			s.engine.HTTPClient.SetBaseURL(req.BaseURL)
			if act, ok := s.stands.Active(); ok {
				_ = s.stands.Update(act.ID, act.Name, req.BaseURL, act.VerifyCode)
			}
		}
		if req.AdminLogin != "" {
			s.cfg.AdminLogin = req.AdminLogin
		}
		if req.AdminPassword != "" {
			s.cfg.AdminPassword = req.AdminPassword
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.cfg)
}

// Token Vault Handlers
func (s *Server) handleTokenVault(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.engine.SessionMgr.GetVault())
}

func (s *Server) handleTokenAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role       string `json:"role"`
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
		Token      string `json:"token"`
		SetActive  bool   `json:"setActive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Token == "" || req.Role == "" {
		http.Error(w, `{"error":"role and token are required"}`, http.StatusBadRequest)
		return
	}

	profile := s.engine.SessionMgr.AddToken(req.Role, req.Name, req.Identifier, req.Token, req.SetActive)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(profile)
}

func (s *Server) handleTokenActivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.engine.SessionMgr.SetActiveProfile(req.Role, req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleTokenDelete removes a stored profile. A profile bound to a role is
// unbound first, so a suite never runs against a token that is no longer in
// the vault.
func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	canonical := client.CanonicalRole(req.Role)
	if bound, ok := s.engine.SessionMgr.TestBindings()[canonical]; ok && bound.ID == req.ID {
		if err := s.bindFixtureAccount(canonical, ""); err != nil {
			log.Printf("[SERVER] Не удалось снять привязку роли %s: %v", canonical, err)
		}
	}

	s.engine.SessionMgr.DeleteProfile(req.Role, req.ID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleTokenAuthLogin(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	var req struct {
		Role        string `json:"role"` // client, rest, courier, admin
		Login       string `json:"login"`
		PhoneNumber string `json:"phoneNumber"`
		Password    string `json:"password"`
		Code        string `json:"code"`
		FirstName   string `json:"firstName"`
		LastName    string `json:"lastName"`
		ProfileName string `json:"profileName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var token string
	var identifier string
	var err error

	switch strings.ToLower(req.Role) {
	case "admin":
		identifier = req.Login
		if identifier == "" {
			identifier = s.cfg.AdminLogin
		}
		pwd := req.Password
		if pwd == "" {
			pwd = s.cfg.AdminPassword
		}
		token, err = s.engine.SessionMgr.LoginAdmin(ctx, identifier, pwd)

	case "rest", "restaurant":
		identifier = req.Login
		token, err = s.engine.RestAPI.Login(ctx, client.RestLoginRequest{
			Login:    req.Login,
			Password: req.Password,
		})

	case "courier":
		identifier = req.PhoneNumber
		if identifier == "" {
			identifier = req.Login
		}
		token, err = s.engine.CourierAPI.Login(ctx, client.CourierLoginRequest{
			PhoneNumber: identifier,
			Password:    req.Password,
		})

	case "client":
		identifier = req.PhoneNumber
		if identifier == "" {
			identifier = req.Login
		}
		if identifier == "" {
			http.Error(w, "phone number is required for client", http.StatusBadRequest)
			return
		}
		// A client signs in with a one-time code, so "log in as this phone"
		// means: request a code, then use it. On a DEBUG stand the code comes
		// back in the register response, which is what makes entering just a
		// phone number enough.
		code := req.Code
		if code == "" {
			regResp, regErr := s.engine.ClientAPI.Register(ctx, client.ClientRegisterRequest{
				PhoneNumber: identifier,
				CityKey:     s.cfg.FixtureCity,
			})
			switch {
			case regErr != nil && client.IsRateLimited(regErr):
				http.Error(w, fmt.Sprintf(
					"Код на %s уже отправлен, повторить можно через %d сек. Введите пришедший код вручную.",
					identifier, client.RetryAfter(regErr)), http.StatusTooManyRequests)
				return
			case regErr != nil:
				http.Error(w, fmt.Sprintf("Не удалось запросить код: %v", regErr), http.StatusBadGateway)
				return
			}
			code = regResp.DebugCode
		}
		if code == "" {
			code = s.cfg.VerificationCode
		}
		if code == "" {
			http.Error(w, "Стенд не вернул код (нужен DEBUG=true) — введите код вручную "+
				"или задайте код стенда в его настройках.", http.StatusBadRequest)
			return
		}
		token, err = s.engine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      identifier,
			VerificationCode: code,
			FirstName:        req.FirstName,
			LastName:         req.LastName,
		})
	default:
		http.Error(w, "invalid role (client, rest, courier, admin)", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Authentication failed: %v", err), http.StatusUnauthorized)
		return
	}

	profile := s.engine.SessionMgr.AddToken(req.Role, req.ProfileName, identifier, token, true)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(profile)
}

func (s *Server) handleTokenRegister(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	var req struct {
		Role           string `json:"role"` // client, rest, courier, admin
		ProfileName    string `json:"profileName"`
		Login          string `json:"login"`
		Password       string `json:"password"`
		RestName       string `json:"restName"`
		Email          string `json:"email"`
		PhoneNumber    string `json:"phoneNumber"`
		FirstName      string `json:"firstName"`
		LastName       string `json:"lastName"`
		DeliveryType   string `json:"deliveryType"`
		CityKey        string `json:"cityKey"`
		City           string `json:"city"`
		Address        string `json:"address"`
		DeliveryMethod string `json:"deliveryMethod"`
		LoginOnly      bool   `json:"loginOnly"`
		Code           string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var token string
	var identifier string
	var err error

	switch strings.ToLower(req.Role) {
	case "admin":
		identifier = req.Login
		if identifier == "" {
			identifier = s.cfg.AdminLogin
		}
		pwd := req.Password
		if pwd == "" {
			pwd = s.cfg.AdminPassword
		}
		token, err = s.engine.SessionMgr.LoginAdmin(ctx, identifier, pwd)

	case "rest", "restaurant":
		login := req.Login
		if login == "" {
			http.Error(w, "login is required for restaurant", http.StatusBadRequest)
			return
		}
		identifier = login
		if !req.LoginOnly {
			regReq := client.RestRegisterRequest{
				RestName:       req.RestName,
				Login:          login,
				Password:       req.Password,
				Email:          req.Email,
				PhoneNumber:    req.PhoneNumber,
				City:           req.City,
				DeliveryMethod: req.DeliveryMethod,
			}
			if regReq.RestName == "" {
				regReq.RestName = "Restaurant " + login
			}
			if regReq.City == "" {
				regReq.City = s.cfg.FixtureCity
			}
			if regReq.DeliveryMethod == "" {
				regReq.DeliveryMethod = "locali"
			}
			// Without these flags the point refuses every order with
			// RECEIVE_METHOD_DISABLED, which makes the account useless for a flow.
			regReq.DeliveryEnabled = true
			regReq.PickupEnabled = true
			regReq.Latitude = &s.cfg.FixtureLatitude
			regReq.Longitude = &s.cfg.FixtureLongitude
			if err := s.engine.RestAPI.Register(ctx, regReq); err != nil {
				log.Printf("[SERVER] Rest register note: %v (attempting login)", err)
			}
		}
		token, err = s.engine.RestAPI.Login(ctx, client.RestLoginRequest{
			Login:    login,
			Password: req.Password,
		})

	case "courier":
		phone := req.PhoneNumber
		if phone == "" {
			phone = req.Login
		}
		if phone == "" {
			http.Error(w, "phone number / login is required for courier", http.StatusBadRequest)
			return
		}
		identifier = phone
		regReq := client.CourierRegisterRequest{
			FirstName:    req.FirstName,
			Surname:      req.LastName,
			LastName:     req.LastName,
			PhoneNumber:  phone,
			Login:        phone,
			Password:     req.Password,
			DeliveryType: req.DeliveryType,
			CityKey:      req.CityKey,
		}
		if regReq.FirstName == "" {
			regReq.FirstName = "Courier"
		}
		if regReq.LastName == "" {
			regReq.LastName = "Custom"
		}
		// The backend stores the family name from `surname` (regCourier maps
		// surname -> lastName); `lastName` alone leaves the row nameless.
		regReq.Surname = regReq.LastName
		// deliveryType is written straight into the group_id foreign key, so
		// only an existing courier group UUID is accepted — the plain "car"
		// this used to send fails the constraint and the courier is never
		// created. Empty leaves the courier without a group, which the backend
		// accepts.
		if regReq.DeliveryType == "" {
			regReq.DeliveryType = s.cfg.FixtureCourierGroupID
		}
		if regReq.CityKey == "" {
			regReq.CityKey = s.cfg.FixtureCity
		}
		// The family name lands in the row through `surname`; `lastName` alone
		// leaves the courier nameless.
		regReq.Surname = regReq.LastName
		if err := s.engine.CourierAPI.Register(ctx, regReq); err != nil {
			log.Printf("[SERVER] Courier register note: %v (attempting login)", err)
		}
		token, err = s.engine.CourierAPI.Login(ctx, client.CourierLoginRequest{
			PhoneNumber: phone,
			Password:    req.Password,
		})

	case "client":
		phone := req.PhoneNumber
		if phone == "" {
			http.Error(w, "phone number is required for client", http.StatusBadRequest)
			return
		}
		identifier = phone
		regReq := client.ClientRegisterRequest{
			PhoneNumber: phone,
			Address:     req.Address,
			CityKey:     req.CityKey,
		}
		if regReq.Address == "" {
			regReq.Address = fmt.Sprintf("%s, ул. Тестовая, 10", s.cfg.FixtureCity)
		}
		if regReq.CityKey == "" {
			regReq.CityKey = s.cfg.FixtureCity
		}
		regResp, regErr := s.engine.ClientAPI.Register(ctx, regReq)
		if regErr != nil {
			log.Printf("[SERVER] Client register note: %v (attempting login)", regErr)
		}
		// Code priority: what the operator typed, then the debugCode the stand
		// returned (DEBUG=true), then the stand-wide verification code. The
		// middle step is what lets the operator add a client token without
		// hunting for the SMS.
		code := req.Code
		if code == "" && regResp != nil {
			code = regResp.DebugCode
		}
		if code == "" {
			code = s.cfg.VerificationCode
		}
		token, err = s.engine.ClientAPI.Login(ctx, client.ClientLoginRequest{
			PhoneNumber:      phone,
			VerificationCode: code,
			FirstName:        req.FirstName,
			LastName:         req.LastName,
		})

	default:
		http.Error(w, "invalid role (client, rest, courier, admin)", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Registration/Login failed: %v", err), http.StatusUnauthorized)
		return
	}

	profile := s.engine.SessionMgr.AddToken(req.Role, req.ProfileName, identifier, token, true)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(profile)
}

func (s *Server) handleTokenPreset(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	// 1. Create 2 Clients
	c1Phone, c1Token, _ := s.engine.Fixtures.CreateUniqueClient(ctx)
	s.engine.SessionMgr.AddToken("client", "Клиент 1 (VIP)", c1Phone, c1Token, true)
	c2Phone, c2Token, _ := s.engine.Fixtures.CreateUniqueClient(ctx)
	s.engine.SessionMgr.AddToken("client", "Клиент 2 (Стандарт)", c2Phone, c2Token, false)

	// 2. Create 2 Restaurants
	r1Login, r1Token, _ := s.engine.Fixtures.CreateUniqueRestaurant(ctx)
	s.engine.SessionMgr.AddToken("rest", "Ресторан 1 (Бургеры)", r1Login, r1Token, true)
	r2Login, r2Token, _ := s.engine.Fixtures.CreateUniqueRestaurant(ctx)
	s.engine.SessionMgr.AddToken("rest", "Ресторан 2 (Суши)", r2Login, r2Token, false)

	// 3. Create 2 Couriers
	cr1Phone, cr1Token, _ := s.engine.Fixtures.CreateUniqueCourier(ctx)
	s.engine.SessionMgr.AddToken("courier", "Курьер 1 (Авто)", cr1Phone, cr1Token, true)
	cr2Phone, cr2Token, _ := s.engine.Fixtures.CreateUniqueCourier(ctx)
	s.engine.SessionMgr.AddToken("courier", "Курьер 2 (Вело)", cr2Phone, cr2Token, false)

	// 4. Admin
	adminToken, _ := s.engine.SessionMgr.LoginAdmin(ctx, s.cfg.AdminLogin, s.cfg.AdminPassword)
	s.engine.SessionMgr.AddToken("admin", "Главный Диспетчер", s.cfg.AdminLogin, adminToken, true)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.engine.SessionMgr.GetVault())
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"client":  s.engine.SessionMgr.GetClientSession(),
		"rest":    s.engine.SessionMgr.GetRestSession(),
		"courier": s.engine.SessionMgr.GetCourierSession(),
		"admin":   s.engine.SessionMgr.GetAdminSession(),
	})
}

func (s *Server) handleGenerateSession(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	var req struct {
		Role string `json:"role"` // client, rest, courier, admin
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	switch strings.ToLower(req.Role) {
	case "client":
		phone, token, err := s.engine.Fixtures.CreateUniqueClient(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": phone, "token": token})
	case "rest":
		login, token, err := s.engine.Fixtures.CreateUniqueRestaurant(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": login, "token": token})
	case "courier":
		phone, token, err := s.engine.Fixtures.CreateUniqueCourier(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": phone, "token": token})
	case "admin":
		token, err := s.engine.SessionMgr.LoginAdmin(ctx, s.cfg.AdminLogin, s.cfg.AdminPassword)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": s.cfg.AdminLogin, "token": token})
	default:
		http.Error(w, "invalid role (use: client, rest, courier, admin)", http.StatusBadRequest)
	}
}

func (s *Server) handleSetToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role  string `json:"role"`
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.engine.SessionMgr.AddToken(req.Role, "Custom "+req.Role, req.ID, req.Token, true)

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleSuites(w http.ResponseWriter, r *http.Request) {
	suites := s.orchestrator.Registry()
	for _, sc := range s.store.List() {
		checks := make([]registry.CheckItem, 0, len(sc.Steps))
		for _, st := range sc.Steps {
			title := st.Title
			if title == "" {
				title = st.ID
			}
			checks = append(checks, registry.CheckItem{ID: st.ID, Title: title})
		}
		suites = append(suites, registry.Suite{
			Key:         sc.Key,
			Title:       sc.Title,
			Description: sc.Description,
			Category:    "custom",
			Tags:        sc.Tags,
			Checks:      checks,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"suites": suites,
	})
}

// handleScenarios serves GET /api/scenarios (list) and POST /api/scenarios (create).
func (s *Server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"scenarios": s.store.List()})
		return

	case http.MethodPost:
		var sc scenario.Scenario
		if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		sc.Category = "custom"
		if err := sc.Validate(); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if s.store.Exists(sc.Key) {
			http.Error(w, fmt.Sprintf(`{"error":"scenario %q already exists"}`, sc.Key), http.StatusConflict)
			return
		}
		if err := s.store.Save(&sc); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(sc)
		return

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleScenarioByID serves GET/PUT/DELETE /api/scenarios/{key}.
func (s *Server) handleScenarioByID(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/api/scenarios/")
	if key == "" || strings.Contains(key, "/") {
		http.Error(w, `{"error":"scenario key is required"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		sc, ok := s.store.Get(key)
		if !ok {
			http.Error(w, fmt.Sprintf(`{"error":"scenario %q not found"}`, key), http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(sc)

	case http.MethodPut:
		var sc scenario.Scenario
		if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		sc.Category = "custom"
		if sc.Key == "" {
			sc.Key = key
		} else if sc.Key != key {
			http.Error(w, `{"error":"key in body must match key in URL"}`, http.StatusBadRequest)
			return
		}
		if err := sc.Validate(); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		if err := s.store.Save(&sc); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(sc)

	case http.MethodDelete:
		if !s.store.Exists(key) {
			http.Error(w, fmt.Sprintf(`{"error":"scenario %q not found"}`, key), http.StatusNotFound)
			return
		}
		if err := s.store.Delete(key); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleScenariosReplenish serves POST /api/scenarios/replenish.
// Generates intelligent scenarios (smoke, crud, rbac, negative) from OpenAPI spec.
func (s *Server) handleScenariosReplenish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	meta, ok := s.specs.Meta()
	if !ok || meta == nil || len(meta.Endpoints) == 0 {
		http.Error(w, `{"error":"спецификация API не импортирована. Сначала импортируйте OpenAPI-спеку во вкладке Инструменты."}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Strategies    []string `json:"strategies"`
		Tags          []string `json:"tags"`
		UncoveredOnly bool     `json:"uncoveredOnly"`
		Preview       bool     `json:"preview"`
		Overwrite     bool     `json:"overwrite"`
		MaxScenarios  int      `json:"maxScenarios"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		http.Error(w, fmt.Sprintf(`{"error":"некорректный json: %v"}`, err), http.StatusBadRequest)
		return
	}

	opts := spec.ReplenishOptions{
		Strategies:    req.Strategies,
		Tags:          req.Tags,
		UncoveredOnly: req.UncoveredOnly,
		MaxScenarios:  req.MaxScenarios,
	}

	if opts.UncoveredOnly {
		cov := analysis.AnalyzeCoverage(meta, s.store.List())
		coveredKeys := make(map[string]bool)
		for _, covEp := range cov.Covered {
			coveredKeys[covEp.Method+" "+covEp.Path] = true
		}
		opts.CoveredKeys = coveredKeys
	}

	result := spec.ReplenishScenarios(meta, opts)

	if req.Preview {
		_ = json.NewEncoder(w).Encode(result)
		return
	}

	if req.Overwrite {
		s.deleteGeneratedScenarios()
	}

	savedCount := 0
	savedKeys := make([]string, 0, len(result.Scenarios))
	for _, sc := range result.Scenarios {
		if err := s.store.Save(sc); err == nil {
			savedCount++
			savedKeys = append(savedKeys, sc.Key)
		}
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "ok",
		"savedCount":       savedCount,
		"savedKeys":        savedKeys,
		"byStrategy":       result.ByStrategy,
		"coveredEndpoints": result.CoveredEndpoints,
		"scenarios":        result.Scenarios,
	})
}

// handleSpecImport serves POST /api/spec/import {"url", "autoReplenish", "strategies"}:
// downloads/reads an OpenAPI/Swagger document, parses it, persists the pair and
// generates scenarios. Parse/download failures are reported as plain-text 400 responses.
func (s *Server) handleSpecImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL           string   `json:"url"`
		AutoReplenish bool     `json:"autoReplenish"`
		Strategies    []string `json:"strategies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json body: %v", err), http.StatusBadRequest)
		return
	}

	adminTok := s.engine.SessionMgr.GetAdminSession().Token
	raw, err := spec.FetchWithAuth(r.Context(), req.URL, adminTok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	meta, err := spec.Parse(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	meta.SourceURL = strings.TrimSpace(req.URL)

	if err := s.specs.Save(meta, raw); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	var generated []string
	var byStrategy map[string]int

	if req.AutoReplenish || len(req.Strategies) > 0 {
		strats := req.Strategies
		if len(strats) == 0 {
			strats = []string{"smoke", "crud", "rbac", "negative"}
		}
		s.deleteGeneratedScenarios()
		res := spec.ReplenishScenarios(meta, spec.ReplenishOptions{
			Strategies:   strats,
			MaxScenarios: 100,
		})
		for _, sc := range res.Scenarios {
			if err := s.store.Save(sc); err == nil {
				generated = append(generated, sc.Key)
			}
		}
		byStrategy = res.ByStrategy
	} else {
		generated, err = s.replaceGeneratedScenarios(meta)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":       meta,
		"generated":  generated,
		"byStrategy": byStrategy,
	})
}

// handleSpecCurrent serves GET /api/spec/current (import summary) and
// DELETE /api/spec/current (drop the specification and its scenarios).
func (s *Server) handleSpecCurrent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		meta, ok := s.specs.Meta()
		if !ok {
			http.Error(w, `{"error":"спецификация не импортирована"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(meta)

	case http.MethodDelete:
		removed := s.deleteGeneratedScenarios()
		if err := s.specs.Delete(); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		log.Printf("[SERVER] Spec removed, %d generated scenarios deleted", removed)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "removedScenarios": removed})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleSpecRegenerate serves POST /api/spec/regenerate — rebuilds the
// spec_smoke_* scenarios from the stored import summary.
func (s *Server) handleSpecRegenerate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	meta, ok := s.specs.Meta()
	if !ok {
		http.Error(w, `{"error":"спецификация не импортирована"}`, http.StatusNotFound)
		return
	}

	generated, err := s.replaceGeneratedScenarios(meta)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":      meta,
		"generated": generated,
	})
}

// replaceGeneratedScenarios wipes every previously generated spec_smoke_*
// scenario and persists fresh ones; returns the keys of the saved scenarios.
func (s *Server) replaceGeneratedScenarios(meta *spec.Meta) ([]string, error) {
	s.deleteGeneratedScenarios()

	scenarios := spec.GenerateScenarios(meta)
	keys := make([]string, 0, len(scenarios))
	for _, sc := range scenarios {
		if err := s.store.Save(sc); err != nil {
			return keys, fmt.Errorf("сохранение сценария %s: %w", sc.Key, err)
		}
		keys = append(keys, sc.Key)
	}
	return keys, nil
}

// deleteGeneratedScenarios removes all scenarios owned by the spec importer.
func (s *Server) deleteGeneratedScenarios() int {
	removed := 0
	for _, sc := range s.store.List() {
		if !strings.HasPrefix(sc.Key, "spec_") {
			continue
		}
		if err := s.store.Delete(sc.Key); err == nil {
			removed++
		}
	}
	return removed
}

// handleAnalysisCoverage serves GET /api/analysis/coverage.
func (s *Server) handleAnalysisCoverage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	meta, _ := s.specs.Meta()
	report := analysis.AnalyzeCoverage(meta, s.store.List())
	_ = json.NewEncoder(w).Encode(report)
}

// handleAnalysisMetrics serves GET /api/analysis/metrics.
func (s *Server) handleAnalysisMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	runs := s.orchestrator.GetHistory()
	metrics := analysis.ComputeMetrics(runs)
	_ = json.NewEncoder(w).Encode(metrics)
}

// handleAnalysisDiagnostics serves GET /api/analysis/diagnostics or /api/analysis/diagnostics/{id}.
func (s *Server) handleAnalysisDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/analysis/diagnostics/")
	id = strings.TrimPrefix(id, "/api/analysis/diagnostics")
	id = strings.TrimSpace(id)

	var run *runner.TestRun
	if id != "" {
		run = s.orchestrator.GetRun(id)
	} else {
		// Pick most recent failed run, or latest run from history
		history := s.orchestrator.GetHistory()
		for _, h := range history {
			if h.Status == runner.RunFailed {
				run = s.orchestrator.GetRun(h.ID)
				break
			}
		}
		if run == nil && len(history) > 0 {
			run = s.orchestrator.GetRun(history[0].ID)
		}
	}

	diag := analysis.DiagnoseRun(run)
	_ = json.NewEncoder(w).Encode(diag)
}

func (s *Server) handleStands(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"stands": s.stands.ListWithActive()})

	case http.MethodPost:
		var req struct {
			Name       string `json:"name"`
			BaseURL    string `json:"baseURL"`
			VerifyCode string `json:"verifyCode"`
			SwaggerURL string `json:"swaggerURL"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		st, err := s.stands.Add(req.Name, req.BaseURL, req.VerifyCode, req.SwaggerURL)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(st)

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStandByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/stands/")
	if id := strings.TrimSuffix(rest, "/activate"); id != rest && !strings.Contains(id, "/") && id != "" {
		s.handleStandActivate(w, r, id)
		return
	}
	if id := strings.TrimSuffix(rest, "/sync-swagger"); id != rest && !strings.Contains(id, "/") && id != "" {
		s.handleStandSyncSwagger(w, r, id)
		return
	}

	id := rest
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, `{"error":"stand id is required"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodPut:
		var req struct {
			Name       string `json:"name"`
			BaseURL    string `json:"baseURL"`
			VerifyCode string `json:"verifyCode"`
			SwaggerURL string `json:"swaggerURL"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		cur, ok := s.stands.Get(id)
		if !ok {
			http.Error(w, fmt.Sprintf(`{"error":"stand %q not found"}`, id), http.StatusNotFound)
			return
		}
		if err := s.stands.Update(id, req.Name, req.BaseURL, req.VerifyCode, req.SwaggerURL); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		updated, _ := s.stands.Get(id)
		isActive := false
		if act, ok := s.stands.Active(); ok {
			isActive = act.ID == id
		}
		if isActive && (updated.BaseURL != cur.BaseURL || updated.VerifyCode != cur.VerifyCode) {
			s.applyStand(updated)
		}
		_ = json.NewEncoder(w).Encode(updated)

	case http.MethodDelete:
		if _, ok := s.stands.Get(id); !ok {
			http.Error(w, fmt.Sprintf(`{"error":"stand %q not found"}`, id), http.StatusNotFound)
			return
		}
		if err := s.stands.Delete(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusConflict)
			return
		}
		s.vaultMu.Lock()
		stands.RemoveVault(s.cfg.DataDir, id)
		s.vaultMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStandActivate(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	st, err := s.stands.Activate(id)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
		return
	}
	s.applyStand(st)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "stand": st})
}

func (s *Server) handleStandSyncSwagger(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	st, ok := s.stands.Get(id)
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"stand %q not found"}`, id), http.StatusNotFound)
		return
	}

	target := strings.TrimSpace(st.SwaggerURL)
	if target == "" {
		http.Error(w, `{"error":"У стенда не указан Swagger URL / путь к спецификации. Укажите его в настройках стенда."}`, http.StatusBadRequest)
		return
	}

	adminTok := s.engine.SessionMgr.GetAdminSession().Token
	raw, err := spec.FetchWithAuth(r.Context(), target, adminTok)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"Не удалось получить спецификацию: %v"}`, err), http.StatusBadRequest)
		return
	}

	meta, err := spec.Parse(raw)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"Ошибка разбора спецификации: %v"}`, err), http.StatusBadRequest)
		return
	}
	meta.SourceURL = target

	if err := s.specs.Save(meta, raw); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Auto-generate test scenarios across all smart strategies
	s.deleteGeneratedScenarios()
	res := spec.ReplenishScenarios(meta, spec.ReplenishOptions{
		Strategies:   []string{"smoke", "crud", "rbac", "negative"},
		MaxScenarios: 100,
	})

	savedCount := 0
	savedKeys := make([]string, 0, len(res.Scenarios))
	for _, sc := range res.Scenarios {
		if err := s.store.Save(sc); err == nil {
			savedCount++
			savedKeys = append(savedKeys, sc.Key)
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "ok",
		"standId":          st.ID,
		"standName":        st.Name,
		"meta":             meta,
		"savedCount":       savedCount,
		"savedKeys":        savedKeys,
		"byStrategy":       res.ByStrategy,
		"coveredEndpoints": res.CoveredEndpoints,
	})
}

func resolveMobilePath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && raw != "." {
		return raw
	}
	candidates := []string{
		"/locali_director",
		"/Users/yusuff84/Documents/all project/flutter-director/locali_director",
		"../flutter-director/locali_director",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return "."
}

// handleMobileContractScan scans a mobile codebase directory (Flutter Dart, iOS Swift, Android Kotlin)
// and runs contract compatibility analysis against current API endpoints.
func (s *Server) handleMobileContractScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}

	targetPath := resolveMobilePath(req.Path)
	models, err := mobilecontract.ScanDirectory(targetPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	meta, _ := s.specs.Meta()
	report := mobilecontract.AnalyzeCompatibility(models, meta, nil)

	s.mobileMu.Lock()
	s.mobileReport = report
	s.mobileMu.Unlock()

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(report)
}

// handleMobileContractReport retrieves compatibility analysis for a query path.
func (s *Server) handleMobileContractReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		s.mobileMu.RLock()
		cached := s.mobileReport
		s.mobileMu.RUnlock()
		if cached != nil {
			_ = json.NewEncoder(w).Encode(cached)
			return
		}
	}

	targetPath := resolveMobilePath(path)
	models, err := mobilecontract.ScanDirectory(targetPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	meta, _ := s.specs.Meta()
	report := mobilecontract.AnalyzeCompatibility(models, meta, nil)

	s.mobileMu.Lock()
	s.mobileReport = report
	s.mobileMu.Unlock()

	_ = json.NewEncoder(w).Encode(report)
}

type gitlabConfigData struct {
	RepoURL    string `json:"repoUrl"`
	Token      string `json:"token"`
	LastBranch string `json:"lastBranch"`
}

func (s *Server) gitlabConfigPath() string {
	return filepath.Join(s.cfg.DataDir, "gitlab_settings.json")
}

func (s *Server) loadGitLabConfig() gitlabConfigData {
	var cfg gitlabConfigData
	data, err := os.ReadFile(s.gitlabConfigPath())
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	return cfg
}

func (s *Server) saveGitLabConfig(cfg gitlabConfigData) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err == nil {
		_ = os.WriteFile(s.gitlabConfigPath(), data, 0o600)
	}
}

func (s *Server) handleMobileGitLabConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		cfg := s.loadGitLabConfig()
		_ = json.NewEncoder(w).Encode(cfg)
	case http.MethodPost:
		var req gitlabConfigData
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		s.saveGitLabConfig(req)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMobileGitLabBranches(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RepoURL string `json:"repoUrl"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}

	repoURL := strings.TrimSpace(req.RepoURL)
	token := strings.TrimSpace(req.Token)
	saved := s.loadGitLabConfig()
	if token == "" {
		token = saved.Token
	}
	if repoURL == "" {
		repoURL = saved.RepoURL
	}
	if repoURL == "" {
		repoURL = "https://lokaligitlabru.ru/app/locali-director-flutter.git"
	}

	serverURL, projectPath, err := mobilecontract.ParseGitLabRepoURL(repoURL, "https://lokaligitlabru.ru")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	branches, err := mobilecontract.FetchGitLabBranches(r.Context(), serverURL, projectPath, token)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Save settings
	s.saveGitLabConfig(gitlabConfigData{
		RepoURL:    repoURL,
		Token:      token,
		LastBranch: saved.LastBranch,
	})

	defaultBranch := ""
	for _, b := range branches {
		if b.Default {
			defaultBranch = b.Name
			break
		}
	}
	if defaultBranch == "" && len(branches) > 0 {
		defaultBranch = branches[0].Name
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"serverUrl":     serverURL,
		"projectPath":   projectPath,
		"branches":      branches,
		"defaultBranch": defaultBranch,
	})
}

func (s *Server) handleMobileGitLabScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RepoURL string `json:"repoUrl"`
		Token   string `json:"token"`
		Branch  string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
		return
	}

	repoURL := strings.TrimSpace(req.RepoURL)
	token := strings.TrimSpace(req.Token)
	branch := strings.TrimSpace(req.Branch)

	saved := s.loadGitLabConfig()
	if token == "" {
		token = saved.Token
	}
	if repoURL == "" {
		repoURL = saved.RepoURL
	}
	if branch == "" {
		branch = saved.LastBranch
	}
	if branch == "" {
		branch = "main"
	}

	serverURL, projectPath, err := mobilecontract.ParseGitLabRepoURL(repoURL, "https://lokaligitlabru.ru")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	models, err := mobilecontract.DownloadAndScanGitLabBranch(r.Context(), serverURL, projectPath, branch, token, s.cfg.DataDir)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	meta, _ := s.specs.Meta()
	report := mobilecontract.AnalyzeCompatibility(models, meta, nil)

	s.mobileMu.Lock()
	s.mobileReport = report
	s.mobileMu.Unlock()

	// Update saved branch
	s.saveGitLabConfig(gitlabConfigData{
		RepoURL:    repoURL,
		Token:      token,
		LastBranch: branch,
	})

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(report)
}

func (s *Server) handleMobileApps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"apps": s.mobileApps.List()})
	case http.MethodPost:
		var app mobilecontract.MobileApp
		if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		saved, err := s.mobileApps.Add(app)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(saved)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMobileAppByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/mobilecontract/apps/")
	if id := strings.TrimSuffix(rest, "/scan"); id != rest && !strings.Contains(id, "/") && id != "" {
		s.handleMobileAppScan(w, r, id)
		return
	}

	id := rest
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, `{"error":"app id is required"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		app, ok := s.mobileApps.Get(id)
		if !ok {
			http.Error(w, fmt.Sprintf(`{"error":"app %q not found"}`, id), http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(app)
	case http.MethodPut:
		var app mobilecontract.MobileApp
		if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		if err := s.mobileApps.Update(id, app); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		updated, _ := s.mobileApps.Get(id)
		_ = json.NewEncoder(w).Encode(updated)
	case http.MethodDelete:
		if err := s.mobileApps.Delete(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) scanMobileApp(r *http.Request, app mobilecontract.MobileApp) (*mobilecontract.CompatibilityReport, error) {
	var models []mobilecontract.MobileModel
	var err error

	token := app.Token
	if token == "" {
		token = s.loadGitLabConfig().Token
	}

	if app.SourceType == "gitlab" && app.RepoURL != "" && token != "" {
		srvURL, projPath, perr := mobilecontract.ParseGitLabRepoURL(app.RepoURL, "https://lokaligitlabru.ru")
		if perr == nil {
			branch := app.Branch
			if branch == "" {
				branch = "main"
			}
			models, err = mobilecontract.DownloadAndScanGitLabBranch(r.Context(), srvURL, projPath, branch, token, s.cfg.DataDir)
		}
	}

	// Fallback to local path if gitlab scan didn't run or if local path is configured
	if len(models) == 0 && app.LocalPath != "" {
		targetPath := resolveMobilePath(app.LocalPath)
		models, err = mobilecontract.ScanDirectory(targetPath)
	}

	if err != nil && len(models) == 0 {
		return nil, err
	}

	meta, _ := s.specs.Meta()
	report := mobilecontract.AnalyzeCompatibility(models, meta, nil, app.Name)
	_ = s.mobileApps.SetReport(app.ID, report)
	return report, nil
}

func (s *Server) handleMobileAppScan(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	app, ok := s.mobileApps.Get(id)
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"app %q not found"}`, id), http.StatusNotFound)
		return
	}

	report, err := s.scanMobileApp(r, app)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	s.mobileMu.Lock()
	s.mobileReport = report
	s.mobileMu.Unlock()

	_ = json.NewEncoder(w).Encode(report)
}

func (s *Server) handleMobileAppsScanAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	apps := s.mobileApps.List()
	combined := &mobilecontract.CompatibilityReport{
		Timestamp:  time.Now().UTC(),
		ByPlatform: make(map[mobilecontract.Platform]int),
		ByApp:      make(map[string]mobilecontract.AppSummary),
		Results:    make([]mobilecontract.ModelCompatibility, 0),
	}

	for _, app := range apps {
		rep, err := s.scanMobileApp(r, app)
		if err != nil {
			log.Printf("[MOBILE] scan app %s error: %v", app.Name, err)
			continue
		}
		combined.TotalModels += rep.TotalModels
		combined.Compatible += rep.Compatible
		combined.Crashes += rep.Crashes
		combined.Warnings += rep.Warnings
		combined.Results = append(combined.Results, rep.Results...)
		for p, cnt := range rep.ByPlatform {
			combined.ByPlatform[p] += cnt
		}
		for aName, sum := range rep.ByApp {
			sum.AppID = app.ID
			combined.ByApp[aName] = sum
		}
	}

	if combined.TotalModels > 0 {
		combined.PassPercentage = float64(combined.Compatible) / float64(combined.TotalModels) * 100.0
	}

	s.mobileMu.Lock()
	s.mobileReport = combined
	s.mobileMu.Unlock()

	_ = json.NewEncoder(w).Encode(combined)
}

// applyStand points the engine at the given stand: swaps the base URL and,
// when the stand defines one, its verification code. The token vault is
// swapped too: the outgoing stand's vault is persisted to disk, then the
// incoming stand's vault (if a snapshot exists) is restored into the very
// same SessionManager instance — role APIs hold that pointer, so it is never
// replaced, only refilled.
func (s *Server) applyStand(st stands.Stand) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()

	s.applyTargetLocked(st)

	// Same stand re-applied (e.g. verify-code edit of the active stand):
	// keep the in-memory vault untouched.
	if st.ID == s.lastStandID {
		return
	}

	firstLoad := s.lastStandID == ""

	if !firstLoad {
		s.saveVaultLocked(s.lastStandID)
	}

	snap, ok, err := stands.LoadVault(s.cfg.DataDir, st.ID)
	if err != nil {
		log.Printf("[SERVER] Vault load for stand %q failed, starting empty: %v", st.ID, err)
		ok = false
	}

	switch {
	case ok:
		s.engine.SessionMgr.Restore(snap)
	case firstLoad:
		// Nothing persisted yet for the startup stand: keep whatever the
		// engine seeded (env preset tokens) — it belongs to this stand.
	default:
		s.engine.SessionMgr.Restore(nil)
	}

	if firstLoad {
		s.seedEnvTokens()
	}

	// Bindings live in the vault, so they follow the stand: switching stands
	// must not leave the suites running as an account from the previous one.
	s.restoreFixtureBindings()

	s.lastStandID = st.ID
}

// applyTargetLocked repoints the engine at the given stand without touching
// the token vault.
func (s *Server) applyTargetLocked(st stands.Stand) {
	s.cfg.BaseURL = st.BaseURL
	s.engine.HTTPClient.SetBaseURL(st.BaseURL)
	if st.VerifyCode != "" {
		s.cfg.VerificationCode = st.VerifyCode
		s.engine.Fixtures.SetVerificationCode(st.VerifyCode)
	}
	log.Printf("[SERVER] Switched to stand %q (%s): %s", st.Name, st.ID, st.BaseURL)
}

// saveVaultLocked persists the current vault contents under the given stand id.
func (s *Server) saveVaultLocked(standID string) {
	if err := stands.SaveVault(s.cfg.DataDir, standID, s.engine.SessionMgr.Snapshot()); err != nil {
		log.Printf("[SERVER] Vault save for stand %q failed: %v", standID, err)
	}
}

// seedEnvTokens re-registers env-provided preset tokens (ADMIN_TOKEN and
// friends) in the freshly loaded startup vault, mirroring the engine bootstrap
// behaviour. Tokens already present are not duplicated.
func (s *Server) seedEnvTokens() {
	presets := []struct{ role, name, identifier, token string }{
		{"client", "Preset (env)", "", s.cfg.ClientToken},
		{"rest", "Preset (env)", "", s.cfg.RestToken},
		{"courier", "Preset (env)", "", s.cfg.CourierToken},
		{"admin", "Default Admin (Env)", s.cfg.AdminLogin, s.cfg.AdminToken},
	}
	for _, p := range presets {
		if p.token == "" || s.engine.SessionMgr.HasToken(p.token) {
			continue
		}
		s.engine.SessionMgr.AddToken(p.role, p.name, p.identifier, p.token, true)
	}
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var req struct {
			Suite   string   `json:"suite"`
			Suites  []string `json:"suites"`
			Trigger string   `json:"trigger"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		if len(req.Suites) > 0 {
			var (
				runs []*runner.TestRun
				err  error
			)
			if trigger := normalizeTrigger(req.Trigger); trigger == runner.TriggerManual {
				runs, err = s.orchestrator.RunSuites(req.Suites)
			} else {
				// Register the group before execution starts: runs execute
				// synchronously, so every OnRunFinished fires inside the call.
				groupID := s.groups.register(len(req.Suites))
				runs, err = s.orchestrator.RunSuitesGroup(req.Suites, trigger, groupID)
				if err != nil {
					s.groups.discard(groupID)
				}
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(runs)
			return
		}

		if req.Suite == "" {
			req.Suite = "flow_a"
		}

		run, err := s.orchestrator.RunSuite(req.Suite)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.orchestrator.GetHistory())
}

// normalizeTrigger whitelists grouped triggers; unknown or empty values fall
// back to a plain manual run.
func normalizeTrigger(raw string) string {
	switch raw {
	case runner.TriggerRegression, runner.TriggerWebhook:
		return raw
	default:
		return runner.TriggerManual
	}
}

// handleRunInputCode serves POST /api/runs/{id}/input-code — the operator
// submits the Telegram verification code for a run paused on client login.
func (s *Server) handleRunInputCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid json body"}`))
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"поле code обязательно"}`))
		return
	}

	if !s.orchestrator.InputBroker.Deliver(r.PathValue("id"), code) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"нет активного ожидания кода для этого прогона"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleRunByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	run := s.orchestrator.GetRun(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

// handleExportJUnit serves GET /api/runs/{id}/export/junit — the run as a
// downloadable JUnit XML report.
func (s *Server) handleExportJUnit(w http.ResponseWriter, r *http.Request) {
	s.serveRunExport(w, r, "xml", "application/xml", func(run *runner.TestRun) ([]byte, error) {
		return report.JUnitXML(run), nil
	})
}

// handleExportAllure serves GET /api/runs/{id}/export/allure — the run as an
// Allure results ZIP archive.
func (s *Server) handleExportAllure(w http.ResponseWriter, r *http.Request) {
	s.serveRunExport(w, r, "zip", "application/zip", report.AllureResultsZip)
}

func (s *Server) serveRunExport(w http.ResponseWriter, r *http.Request, ext, contentType string, render func(*runner.TestRun) ([]byte, error)) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	run := s.orchestrator.GetRun(r.PathValue("id"))
	if run == nil {
		http.NotFound(w, r)
		return
	}
	data, err := render(run)
	if err != nil {
		log.Printf("[SERVER] export %s for run %s: %v", ext, run.ID, err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="run-%s-%s.%s"`, exportSuiteSlug(run), shortID(run.ID), ext))
	_, _ = w.Write(data)
}

// exportSuiteSlug returns a filesystem-safe suite key for export file names.
func exportSuiteSlug(run *runner.TestRun) string {
	key := run.SuiteKey
	if key == "" {
		key = run.SuiteName
	}
	if key == "" {
		key = "run"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, key)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	eventsChan := s.orchestrator.Broadcaster()

	// Initial ping
	fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-eventsChan:
			data, err := json.Marshal(event)
			if err == nil {
				fmt.Fprintf(w, "event: test_event\ndata: %s\n\n", string(data))
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleRecentEvents(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > runner.MaxRecentEvents {
		limit = runner.MaxRecentEvents
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"events": s.orchestrator.RecentEvents(limit),
	})
}

func (s *Server) handleManualAction(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	var req struct {
		Action    string `json:"action"` // create_order, assign_courier, change_rest_status, courier_status
		OrderID   string `json:"orderId"`
		RestID    string `json:"restId"`
		CourierID string `json:"courierId"`
		Status    string `json:"status"`
		// AddressID and ReceiveMethod belong to create_order: the order is
		// assembled from the client's cart and refers to a saved address.
		AddressID     string `json:"addressId"`
		ReceiveMethod string `json:"receiveMethod"`
		PaymentType   string `json:"paymentType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var result interface{}
	var err error

	switch req.Action {
	case "create_order":
		receiveMethod := req.ReceiveMethod
		if receiveMethod == "" {
			receiveMethod = client.ReceiveDelivery
		}
		paymentType := req.PaymentType
		if paymentType == "" {
			paymentType = client.PaymentCash
		}
		// The cart must already hold something: this endpoint places an order,
		// it does not assemble one.
		result, err = s.engine.ClientAPI.CreateRestaurantOrder(ctx, client.CreateRestaurantOrderRequest{
			RestID:        req.RestID,
			ReceiveMethod: receiveMethod,
			AddressID:     req.AddressID,
			Payment:       client.OrderPayment{Type: paymentType},
			Source:        client.SourceLokaliEda,
		})
	case "assign_courier":
		result, err = s.engine.AdminAPI.AssignCourier(ctx, req.OrderID, req.CourierID)
	case "change_rest_status":
		result, err = s.engine.RestAPI.ChangeOrderStatus(ctx, req.OrderID, req.Status)
	case "courier_status":
		result, err = s.engine.CourierAPI.ChangeStatus(ctx, req.OrderID, req.Status, false)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleFixtureAccounts reads and writes which stored accounts the test suites
// run as.
//
// By default every suite generates a fresh identity per role, which keeps runs
// isolated. Binding an account trades that isolation for a known identity —
// useful when a stand has hand-prepared data (a restaurant with a real menu, a
// courier already in the right group) that a generated fixture cannot
// reproduce.
func (s *Server) handleFixtureAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"accounts": s.fixtureAccountsView()})

	case http.MethodPost, http.MethodPut:
		// Every role present in the body is applied; a role sent as "" is
		// unbound and goes back to generating an identity per run.
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		applied := make([]string, 0, len(req))
		for role, profileID := range req {
			canonical := client.CanonicalRole(role)
			if canonical == "" {
				http.Error(w, fmt.Sprintf(`{"error":"неизвестная роль: %s"}`, role), http.StatusBadRequest)
				return
			}
			if err := s.bindFixtureAccount(canonical, profileID); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
			applied = append(applied, canonical)
		}

		sort.Strings(applied)
		s.vaultMu.Lock()
		s.saveVaultLocked(s.lastStandID)
		s.vaultMu.Unlock()

		log.Printf("[SERVER] Аккаунты для тестов обновлены: %s", strings.Join(applied, ", "))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"accounts": s.fixtureAccountsView()})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// bindFixtureAccount points one role at a stored profile (or clears it) and
// pushes the decision into the fixture manager, which is what the suites read.
func (s *Server) bindFixtureAccount(canonical, profileID string) error {
	profile, err := s.engine.SessionMgr.SetTestBinding(canonical, profileID)
	if err != nil {
		return err
	}

	if profile == nil {
		return s.engine.Fixtures.BindAccount(canonical, "", "", "")
	}

	if err := s.engine.Fixtures.BindAccount(canonical, profile.Name, profile.Identifier, profile.Token); err != nil {
		// The binding is rejected as a whole: leaving the vault pointing at an
		// account the suites cannot use would be a lie.
		_, _ = s.engine.SessionMgr.SetTestBinding(canonical, "")
		return err
	}
	return nil
}

// fixtureAccountsView renders the current binding per role for the UI.
func (s *Server) fixtureAccountsView() map[string]interface{} {
	bound := s.engine.SessionMgr.TestBindings()
	out := make(map[string]interface{}, 4)

	for _, role := range []string{"client", "rest", "courier", "admin"} {
		entry := map[string]interface{}{"bound": false}
		if p, ok := bound[role]; ok {
			label, identifier, entityID, isBound := s.engine.Fixtures.BoundAccount(role)
			entry = map[string]interface{}{
				"bound":      isBound,
				"profileId":  p.ID,
				"name":       p.Name,
				"identifier": identifier,
				"entityId":   entityID,
				"label":      label,
			}
		}
		out[role] = entry
	}
	return out
}

// restoreFixtureBindings re-applies the vault's bindings to the fixture
// manager. Called after a vault is loaded for a stand, so a restart or a stand
// switch does not silently drop back to generated identities.
func (s *Server) restoreFixtureBindings() {
	for role := range map[string]struct{}{"client": {}, "rest": {}, "courier": {}, "admin": {}} {
		_ = s.engine.Fixtures.BindAccount(role, "", "", "")
	}
	for role, profile := range s.engine.SessionMgr.TestBindings() {
		if err := s.engine.Fixtures.BindAccount(role, profile.Name, profile.Identifier, profile.Token); err != nil {
			log.Printf("[SERVER] Привязка аккаунта роли %s снята: %v", role, err)
			_, _ = s.engine.SessionMgr.SetTestBinding(role, "")
		}
	}
}
