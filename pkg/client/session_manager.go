package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TokenProfile represents a saved identity token with metadata
type TokenProfile struct {
	ID         string                 `json:"id"`
	Role       string                 `json:"role"` // client, rest, courier, admin
	Name       string                 `json:"name"`
	Identifier string                 `json:"identifier"` // Phone, Login, or Email
	Token      string                 `json:"token"`
	IsActive   bool                   `json:"isActive"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	CreatedAt  time.Time              `json:"createdAt"`
}

// SessionManager manages active authorization tokens and pools across all 4 roles
type SessionManager struct {
	mu          sync.RWMutex
	httpClient  *HTTPClient
	
	// Pools of profiles per role
	clientPool  map[string]*TokenProfile
	restPool    map[string]*TokenProfile
	courierPool map[string]*TokenProfile
	adminPool   map[string]*TokenProfile

	activeClientID  string
	activeRestID    string
	activeCourierID string
	activeAdminID   string

	// testBindings maps a canonical role to the profile the operator picked
	// for test runs. A role without a binding keeps generating a fresh
	// identity per run, which is the isolated default.
	testBindings map[string]string
}

// NewSessionManager creates a new multi-role multi-profile session manager
func NewSessionManager(httpClient *HTTPClient) *SessionManager {
	sm := &SessionManager{
		httpClient:  httpClient,
		clientPool:  make(map[string]*TokenProfile),
		restPool:    make(map[string]*TokenProfile),
		courierPool:  make(map[string]*TokenProfile),
		adminPool:    make(map[string]*TokenProfile),
		testBindings: make(map[string]string),
	}

	return sm
}

// HTTPClient returns the underlying HTTP client
func (sm *SessionManager) HTTPClient() *HTTPClient {
	return sm.httpClient
}

// AddToken adds a new token profile to the pool
func (sm *SessionManager) AddToken(role, name, identifier, token string, setActive bool) *TokenProfile {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := uuid.New().String()
	payload := parseJWTPayload(token)

	if name == "" {
		name = fmt.Sprintf("%s (%s)", strings.ToUpper(role), identifier)
	}

	profile := &TokenProfile{
		ID:         id,
		Role:       strings.ToLower(role),
		Name:       name,
		Identifier: identifier,
		Token:      token,
		IsActive:   false,
		Payload:    payload,
		CreatedAt:  time.Now(),
	}

	switch strings.ToLower(role) {
	case "client":
		sm.clientPool[id] = profile
		if setActive || len(sm.clientPool) == 1 {
			sm.setActiveClientLocked(id)
		}
	case "rest", "restaurant":
		profile.Role = "rest"
		sm.restPool[id] = profile
		if setActive || len(sm.restPool) == 1 {
			sm.setActiveRestLocked(id)
		}
	case "courier":
		sm.courierPool[id] = profile
		if setActive || len(sm.courierPool) == 1 {
			sm.setActiveCourierLocked(id)
		}
	case "admin", "director":
		profile.Role = "admin"
		sm.adminPool[id] = profile
		if setActive || len(sm.adminPool) == 1 {
			sm.setActiveAdminLocked(id)
		}
	}

	return profile
}

// SetActiveProfile switches active profile for a role
func (sm *SessionManager) SetActiveProfile(role, id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch strings.ToLower(role) {
	case "client":
		if _, exists := sm.clientPool[id]; !exists {
			return fmt.Errorf("client profile %s not found", id)
		}
		sm.setActiveClientLocked(id)
	case "rest", "restaurant":
		if _, exists := sm.restPool[id]; !exists {
			return fmt.Errorf("restaurant profile %s not found", id)
		}
		sm.setActiveRestLocked(id)
	case "courier":
		if _, exists := sm.courierPool[id]; !exists {
			return fmt.Errorf("courier profile %s not found", id)
		}
		sm.setActiveCourierLocked(id)
	case "admin", "director":
		if _, exists := sm.adminPool[id]; !exists {
			return fmt.Errorf("admin profile %s not found", id)
		}
		sm.setActiveAdminLocked(id)
	default:
		return fmt.Errorf("unknown role: %s", role)
	}

	return nil
}

// DeleteProfile removes a token profile
func (sm *SessionManager) DeleteProfile(role, id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch strings.ToLower(role) {
	case "client":
		delete(sm.clientPool, id)
		if sm.activeClientID == id {
			sm.activeClientID = ""
			for k := range sm.clientPool {
				sm.setActiveClientLocked(k)
				break
			}
		}
	case "rest", "restaurant":
		delete(sm.restPool, id)
		if sm.activeRestID == id {
			sm.activeRestID = ""
			for k := range sm.restPool {
				sm.setActiveRestLocked(k)
				break
			}
		}
	case "courier":
		delete(sm.courierPool, id)
		if sm.activeCourierID == id {
			sm.activeCourierID = ""
			for k := range sm.courierPool {
				sm.setActiveCourierLocked(k)
				break
			}
		}
	case "admin", "director":
		delete(sm.adminPool, id)
		if sm.activeAdminID == id {
			sm.activeAdminID = ""
			for k := range sm.adminPool {
				sm.setActiveAdminLocked(k)
				break
			}
		}
	}
}

// GetVault returns all pools and active selection
func (sm *SessionManager) GetVault() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	clients := make([]*TokenProfile, 0, len(sm.clientPool))
	for _, p := range sm.clientPool {
		clients = append(clients, p)
	}

	rests := make([]*TokenProfile, 0, len(sm.restPool))
	for _, p := range sm.restPool {
		rests = append(rests, p)
	}

	couriers := make([]*TokenProfile, 0, len(sm.courierPool))
	for _, p := range sm.courierPool {
		couriers = append(couriers, p)
	}

	admins := make([]*TokenProfile, 0, len(sm.adminPool))
	for _, p := range sm.adminPool {
		admins = append(admins, p)
	}

	bindings := make(map[string]string, len(sm.testBindings))
	for role, id := range sm.testBindings {
		bindings[role] = id
	}

	return map[string]interface{}{
		"testAccounts":    bindings,
		"clients":         clients,
		"rests":           rests,
		"couriers":        couriers,
		"admins":          admins,
		"activeClientId":  sm.activeClientID,
		"activeRestId":    sm.activeRestID,
		"activeCourierId": sm.activeCourierID,
		"activeAdminId":   sm.activeAdminID,
	}
}

// VaultSnapshot is a portable deep copy of the whole token vault: every
// profile across all role pools plus the per-role active profile pointers.
type VaultSnapshot struct {
	Profiles []*TokenProfile   `json:"profiles"`
	Active   map[string]string `json:"active"` // canonical role -> profile ID
	// TestBindings records which profile each role uses in test runs. A role
	// absent here generates a fresh identity per run.
	TestBindings map[string]string `json:"testBindings,omitempty"`
}

// Snapshot returns a deep copy of all pools and active pointers. The copy is
// fully detached from the live manager and safe to marshal or store.
func (sm *SessionManager) Snapshot() *VaultSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	profiles := make([]*TokenProfile, 0, len(sm.clientPool)+len(sm.restPool)+len(sm.courierPool)+len(sm.adminPool))
	for _, pool := range []*map[string]*TokenProfile{&sm.clientPool, &sm.restPool, &sm.courierPool, &sm.adminPool} {
		for _, p := range *pool {
			cp := *p
			cp.Payload = clonePayload(p.Payload)
			profiles = append(profiles, &cp)
		}
	}

	bindings := make(map[string]string, len(sm.testBindings))
	for role, id := range sm.testBindings {
		bindings[role] = id
	}

	return &VaultSnapshot{
		Profiles: profiles,
		Active: map[string]string{
			"client":  sm.activeClientID,
			"rest":    sm.activeRestID,
			"courier": sm.activeCourierID,
			"admin":   sm.activeAdminID,
		},
		TestBindings: bindings,
	}
}

// Restore fully replaces the vault contents with the snapshot: everything is
// cleared first, then the snapshot profiles are copied in. A nil or empty
// snapshot yields an empty storage. Active pointers referencing unknown
// profiles are dropped, leaving that role without an active profile.
func (sm *SessionManager) Restore(snap *VaultSnapshot) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.clientPool = make(map[string]*TokenProfile)
	sm.restPool = make(map[string]*TokenProfile)
	sm.courierPool = make(map[string]*TokenProfile)
	sm.adminPool = make(map[string]*TokenProfile)
	sm.activeClientID = ""
	sm.activeRestID = ""
	sm.activeCourierID = ""
	sm.activeAdminID = ""
	sm.testBindings = make(map[string]string)

	if snap == nil {
		return
	}

	taken := make(map[string]bool)
	for _, p := range snap.Profiles {
		if p == nil || strings.TrimSpace(p.Token) == "" {
			continue
		}

		np := *p
		switch strings.ToLower(np.Role) {
		case "client":
			np.Role = "client"
		case "rest", "restaurant":
			np.Role = "rest"
		case "courier":
			np.Role = "courier"
		case "admin", "director":
			np.Role = "admin"
		default:
			continue
		}
		if np.ID == "" || taken[np.ID] {
			np.ID = uuid.New().String()
		}
		if np.CreatedAt.IsZero() {
			np.CreatedAt = time.Now()
		}
		np.Payload = clonePayload(np.Payload)

		taken[np.ID] = true
		switch np.Role {
		case "client":
			sm.clientPool[np.ID] = &np
		case "rest":
			sm.restPool[np.ID] = &np
		case "courier":
			sm.courierPool[np.ID] = &np
		case "admin":
			sm.adminPool[np.ID] = &np
		}
	}

	// Bindings referencing profiles that did not survive the snapshot are
	// dropped: a role pointing at a missing account must fall back to
	// generating one, not fail every run.
	for role, id := range snap.TestBindings {
		canonical := CanonicalRole(role)
		if canonical == "" || id == "" {
			continue
		}
		if sm.findProfileLocked(canonical, id) != nil {
			sm.testBindings[canonical] = id
		}
	}

	for role, id := range snap.Active {
		if id == "" {
			continue
		}
		switch strings.ToLower(role) {
		case "client":
			if _, ok := sm.clientPool[id]; ok {
				sm.setActiveClientLocked(id)
			}
		case "rest", "restaurant":
			if _, ok := sm.restPool[id]; ok {
				sm.setActiveRestLocked(id)
			}
		case "courier":
			if _, ok := sm.courierPool[id]; ok {
				sm.setActiveCourierLocked(id)
			}
		case "admin", "director":
			if _, ok := sm.adminPool[id]; ok {
				sm.setActiveAdminLocked(id)
			}
		}
	}
}

// HasToken reports whether any profile in any role pool carries the exact token.
func (sm *SessionManager) HasToken(token string) bool {
	if token == "" {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, pool := range []*map[string]*TokenProfile{&sm.clientPool, &sm.restPool, &sm.courierPool, &sm.adminPool} {
		for _, p := range *pool {
			if p.Token == token {
				return true
			}
		}
	}
	return false
}

func clonePayload(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = cloneValue(v)
	}
	return dst
}

func cloneValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return clonePayload(t)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, item := range t {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return v
	}
}

// Active session getters
func (sm *SessionManager) GetClientSession() *RoleSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.activeClientID != "" {
		if p, ok := sm.clientPool[sm.activeClientID]; ok {
			return &RoleSession{ID: p.ID, Token: p.Token, Identifier: p.Identifier, Name: p.Name}
		}
	}
	return &RoleSession{}
}

func (sm *SessionManager) GetRestSession() *RoleSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.activeRestID != "" {
		if p, ok := sm.restPool[sm.activeRestID]; ok {
			return &RoleSession{ID: p.ID, Token: p.Token, Identifier: p.Identifier, Name: p.Name}
		}
	}
	return &RoleSession{}
}

func (sm *SessionManager) GetCourierSession() *RoleSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.activeCourierID != "" {
		if p, ok := sm.courierPool[sm.activeCourierID]; ok {
			return &RoleSession{ID: p.ID, Token: p.Token, Identifier: p.Identifier, Name: p.Name}
		}
	}
	return &RoleSession{}
}

func (sm *SessionManager) GetAdminSession() *RoleSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.activeAdminID != "" {
		if p, ok := sm.adminPool[sm.activeAdminID]; ok {
			return &RoleSession{ID: p.ID, Token: p.Token, Identifier: p.Identifier, Name: p.Name}
		}
	}
	return &RoleSession{}
}

// Compatibility setters
func (sm *SessionManager) SetClientSession(id, token, phone, name string) {
	sm.AddToken("client", name, phone, token, true)
}

func (sm *SessionManager) SetRestSession(id, token, login, name string) {
	sm.AddToken("rest", name, login, token, true)
}

func (sm *SessionManager) SetCourierSession(id, token, login, name string) {
	sm.AddToken("courier", name, login, token, true)
}

func (sm *SessionManager) SetAdminSession(id, token, login string) {
	sm.AddToken("admin", "Director Admin", login, token, true)
}

func (sm *SessionManager) LoginAdmin(ctx context.Context, login, password string) (string, error) {
	req := AdminLoginRequest{
		Login:    login,
		Password: password,
	}

	var token string
	_, err := sm.httpClient.Request(ctx, "POST", "/api/admin/login", "", req, &token)
	if err != nil {
		return "", fmt.Errorf("admin login failed: %w", err)
	}

	sm.AddToken("admin", "Super Admin", login, token, true)
	return token, nil
}

func (sm *SessionManager) setActiveClientLocked(id string) {
	sm.activeClientID = id
	for _, p := range sm.clientPool {
		p.IsActive = (p.ID == id)
	}
}

func (sm *SessionManager) setActiveRestLocked(id string) {
	sm.activeRestID = id
	for _, p := range sm.restPool {
		p.IsActive = (p.ID == id)
	}
}

func (sm *SessionManager) setActiveCourierLocked(id string) {
	sm.activeCourierID = id
	for _, p := range sm.courierPool {
		p.IsActive = (p.ID == id)
	}
}

func (sm *SessionManager) setActiveAdminLocked(id string) {
	sm.activeAdminID = id
	for _, p := range sm.adminPool {
		p.IsActive = (p.ID == id)
	}
}

// RoleSession stores active credentials
type RoleSession struct {
	ID         string
	Token      string
	Identifier string
	Name       string
	Metadata   map[string]interface{}
}

// Helper to decode JWT payload without secret
func parseJWTPayload(jwtToken string) map[string]interface{} {
	parts := strings.Split(jwtToken, ".")
	if len(parts) < 2 {
		return nil
	}

	seg := parts[1]
	// Pad base64 if needed
	switch len(seg) % 4 {
	case 2:
		seg += "=="
	case 3:
		seg += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil
	}

	return payload
}

// CanonicalRole normalizes the role aliases used across the engine and the UI.
// It returns "" for anything unrecognised.
func CanonicalRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "client":
		return "client"
	case "rest", "restaurant":
		return "rest"
	case "courier":
		return "courier"
	case "admin", "director":
		return "admin"
	}
	return ""
}

func (sm *SessionManager) poolLocked(canonical string) map[string]*TokenProfile {
	switch canonical {
	case "client":
		return sm.clientPool
	case "rest":
		return sm.restPool
	case "courier":
		return sm.courierPool
	case "admin":
		return sm.adminPool
	}
	return nil
}

func (sm *SessionManager) findProfileLocked(canonical, id string) *TokenProfile {
	pool := sm.poolLocked(canonical)
	if pool == nil {
		return nil
	}
	return pool[id]
}

// Profile returns a copy of one stored profile.
func (sm *SessionManager) Profile(role, id string) (*TokenProfile, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	p := sm.findProfileLocked(CanonicalRole(role), id)
	if p == nil {
		return nil, false
	}
	cp := *p
	cp.Payload = clonePayload(p.Payload)
	return &cp, true
}

// SetTestBinding designates which stored profile a role uses in test runs.
// An empty id clears the binding, returning that role to generating a fresh
// identity per run.
func (sm *SessionManager) SetTestBinding(role, profileID string) (*TokenProfile, error) {
	canonical := CanonicalRole(role)
	if canonical == "" {
		return nil, fmt.Errorf("неизвестная роль: %s", role)
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if profileID == "" {
		delete(sm.testBindings, canonical)
		return nil, nil
	}

	p := sm.findProfileLocked(canonical, profileID)
	if p == nil {
		return nil, fmt.Errorf("профиль %s для роли %s не найден", profileID, canonical)
	}
	sm.testBindings[canonical] = profileID

	cp := *p
	cp.Payload = clonePayload(p.Payload)
	return &cp, nil
}

// TestBindings returns the bound profile per role, resolved to live profiles.
// Roles whose binding no longer resolves are omitted.
func (sm *SessionManager) TestBindings() map[string]*TokenProfile {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := make(map[string]*TokenProfile, len(sm.testBindings))
	for role, id := range sm.testBindings {
		if p := sm.findProfileLocked(role, id); p != nil {
			cp := *p
			cp.Payload = clonePayload(p.Payload)
			out[role] = &cp
		}
	}
	return out
}
