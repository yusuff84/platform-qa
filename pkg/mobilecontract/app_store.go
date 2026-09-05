package mobilecontract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// AppStore persists configured mobile applications as <DataDir>/mobile_apps.json.
type AppStore struct {
	mu       sync.RWMutex
	filePath string
	apps     []MobileApp
}

// NewAppStore initializes the storage and seeds a default application if empty.
func NewAppStore(dataDir string) (*AppStore, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	_ = os.MkdirAll(dataDir, 0o755)

	filePath := filepath.Join(dataDir, "mobile_apps.json")
	store := &AppStore{filePath: filePath}

	data, err := os.ReadFile(filePath)
	if err == nil {
		var list []MobileApp
		if uerr := json.Unmarshal(data, &list); uerr == nil {
			store.apps = list
		}
	}

	if len(store.apps) == 0 {
		// Seed default Locali Director mobile application
		defaultApp := MobileApp{
			ID:         "locali-director",
			Name:       "Locali Director",
			Platform:   PlatformFlutter,
			SourceType: "gitlab",
			RepoURL:    "https://lokaligitlabru.ru/app/locali-director-flutter.git",
			Branch:     "main",
			LocalPath:  "/locali_director",
		}
		store.apps = []MobileApp{defaultApp}
		_ = store.saveLocked()
	}

	return store, nil
}

func (s *AppStore) List() []MobileApp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MobileApp, len(s.apps))
	copy(out, s.apps)
	return out
}

func (s *AppStore) Get(id string) (MobileApp, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.apps {
		if a.ID == id {
			return a, true
		}
	}
	return MobileApp{}, false
}

func (s *AppStore) Add(app MobileApp) (MobileApp, error) {
	name := strings.TrimSpace(app.Name)
	if name == "" {
		return MobileApp{}, fmt.Errorf("название приложения обязательно")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := app.ID
	if id == "" {
		id = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
		if id == "" {
			id = fmt.Sprintf("app-%d", time.Now().Unix())
		}
	}

	// Ensure unique ID
	baseID := id
	counter := 1
	for {
		exists := false
		for _, a := range s.apps {
			if a.ID == id {
				exists = true
				break
			}
		}
		if !exists {
			break
		}
		counter++
		id = fmt.Sprintf("%s-%d", baseID, counter)
	}

	app.ID = id
	app.Name = name
	if app.SourceType == "" {
		if app.RepoURL != "" {
			app.SourceType = "gitlab"
		} else {
			app.SourceType = "local"
		}
	}
	if app.Platform == "" {
		app.Platform = PlatformFlutter
	}

	s.apps = append(s.apps, app)
	if err := s.saveLocked(); err != nil {
		s.apps = s.apps[:len(s.apps)-1]
		return MobileApp{}, err
	}

	return app, nil
}

func (s *AppStore) Update(id string, app MobileApp) error {
	name := strings.TrimSpace(app.Name)
	if name == "" {
		return fmt.Errorf("название приложения обязательно")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, a := range s.apps {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("приложение %q не найдено", id)
	}

	prev := s.apps[idx]
	app.ID = id
	app.Name = name
	if app.Token == "" {
		app.Token = prev.Token // keep existing token if empty
	}
	if app.LastReport == nil {
		app.LastReport = prev.LastReport
	}
	if app.LastScanned == nil {
		app.LastScanned = prev.LastScanned
	}

	s.apps[idx] = app
	if err := s.saveLocked(); err != nil {
		s.apps[idx] = prev
		return err
	}
	return nil
}

func (s *AppStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, a := range s.apps {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("приложение %q не найдено", id)
	}

	prev := s.apps[idx]
	s.apps = append(s.apps[:idx], s.apps[idx+1:]...)
	if err := s.saveLocked(); err != nil {
		s.apps = append(s.apps[:idx], append([]MobileApp{prev}, s.apps[idx:]...)...)
		return err
	}
	return nil
}

func (s *AppStore) SetReport(id string, report *CompatibilityReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.apps {
		if s.apps[i].ID == id {
			now := time.Now().UTC()
			s.apps[i].LastScanned = &now
			s.apps[i].LastReport = report
			return s.saveLocked()
		}
	}
	return fmt.Errorf("приложение %q не найдено", id)
}

func (s *AppStore) saveLocked() error {
	data, err := json.MarshalIndent(s.apps, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mobile apps: %w", err)
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}
