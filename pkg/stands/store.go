package stands

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type Stand struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	BaseURL    string `json:"baseURL"`
	VerifyCode string `json:"verifyCode,omitempty"` // код верификации этого стенда
	SwaggerURL string `json:"swaggerURL,omitempty"` // URL или путь к OpenAPI/Swagger спецификации
	IsMock     bool   `json:"isMock"`
}

// StandView is a Stand annotated with the current activation flag.
type StandView struct {
	Stand
	IsActive bool `json:"isActive"`
}

const (
	maxNameLen  = 60
	fileName    = "stands.json"
	fallbackURL = "http://localhost:3000"
)

type persisted struct {
	ActiveID string  `json:"activeID"`
	Stands   []Stand `json:"stands"`
}

// Store is a thread-safe registry of stand targets persisted
// atomically as a single JSON file <dataDir>/stands.json.
type Store struct {
	mu       sync.RWMutex
	filePath string
	activeID string
	items    []Stand
}

// NewStore loads stands from <dataDir>/stands.json; when the file does not
// exist (or is unreadable) it is created and seeded with defaults.
func NewStore(dataDir string, defaults ...Stand) (*Store, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("stands store: data dir is required")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("stands store: mkdir %s: %w", dataDir, err)
	}
	s := &Store{filePath: filepath.Join(dataDir, fileName)}
	seeded := false

	data, err := os.ReadFile(s.filePath)
	switch {
	case err == nil:
		var p persisted
		if uErr := json.Unmarshal(data, &p); uErr != nil {
			log.Printf("[STANDS] corrupt %s, recreating defaults: %v", s.filePath, uErr)
			s.seed(defaults...)
			seeded = true
		} else {
			for _, std := range p.Stands {
				if std.ID == "" {
					continue
				}
				s.items = append(s.items, std)
			}
			s.activeID = p.ActiveID
		}
	case os.IsNotExist(err):
		s.seed(defaults...)
		seeded = true
	default:
		return nil, fmt.Errorf("stands store: read %s: %w", s.filePath, err)
	}

	if len(s.items) == 0 {
		s.seed()
		seeded = true
	}
	s.normalizeActive()
	if seeded {
		if err := s.saveLocked(); err != nil {
			return nil, fmt.Errorf("stands store: seed %s: %w", s.filePath, err)
		}
	}
	return s, nil
}

// List returns all stands in insertion order.
func (s *Store) List() []Stand {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Stand, len(s.items))
	copy(out, s.items)
	return out
}

// ListWithActive returns all stands with the isActive flag resolved.
func (s *Store) ListWithActive() []StandView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	activeIdx := s.resolveActiveLocked()
	out := make([]StandView, len(s.items))
	for i, std := range s.items {
		out[i] = StandView{Stand: std, IsActive: i == activeIdx}
	}
	return out
}

// Get returns a copy of the stand by id.
func (s *Store) Get(id string) (Stand, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.indexOfLocked(id)
	if idx < 0 {
		return Stand{}, false
	}
	return s.items[idx], true
}

// Active returns the currently activated stand.
func (s *Store) Active() (Stand, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.resolveActiveLocked()
	if idx < 0 {
		return Stand{}, false
	}
	return s.items[idx], true
}

// Add validates inputs, derives a slug id from the name (with a numeric
// suffix on collision) and persists the new stand.
func (s *Store) Add(name, baseURL, verifyCode string, swaggerURL ...string) (Stand, error) {
	name, baseURL = strings.TrimSpace(name), strings.TrimSpace(baseURL)
	if err := validate(name, baseURL); err != nil {
		return Stand{}, err
	}

	swURL := ""
	if len(swaggerURL) > 0 {
		swURL = strings.TrimSpace(swaggerURL[0])
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	taken := make(map[string]bool, len(s.items))
	for _, it := range s.items {
		taken[it.ID] = true
	}
	ns := Stand{
		ID:         uniqueSlug(slugify(name), taken),
		Name:       name,
		BaseURL:    baseURL,
		VerifyCode: verifyCode,
		SwaggerURL: swURL,
	}
	s.items = append(s.items, ns)
	if err := s.saveLocked(); err != nil {
		s.items = s.items[:len(s.items)-1]
		return Stand{}, err
	}
	return ns, nil
}

// Update replaces name/baseURL/verifyCode/swaggerURL of the stand. The baseURL of the
// embedded mock stand is immutable.
func (s *Store) Update(id, name, baseURL, verifyCode string, swaggerURL ...string) error {
	name, baseURL = strings.TrimSpace(name), strings.TrimSpace(baseURL)
	if err := validate(name, baseURL); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexOfLocked(id)
	if idx < 0 {
		return fmt.Errorf("stand %q not found", id)
	}
	if s.items[idx].IsMock && baseURL != s.items[idx].BaseURL {
		return fmt.Errorf("cannot change baseURL of embedded mock stand")
	}
	prev := s.items[idx]
	s.items[idx].Name = name
	s.items[idx].BaseURL = baseURL
	s.items[idx].VerifyCode = verifyCode
	if len(swaggerURL) > 0 {
		s.items[idx].SwaggerURL = strings.TrimSpace(swaggerURL[0])
	}
	if err := s.saveLocked(); err != nil {
		s.items[idx] = prev
		return err
	}
	return nil
}

// Delete removes the stand. The currently active stand and the embedded
// mock stand cannot be deleted.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexOfLocked(id)
	if idx < 0 {
		return fmt.Errorf("stand %q not found", id)
	}
	if s.items[idx].IsMock {
		return fmt.Errorf("cannot delete embedded mock stand")
	}
	if s.items[idx].ID == s.activeID {
		return fmt.Errorf("cannot delete active stand %q, activate another stand first", s.items[idx].Name)
	}
	prev := s.items[idx]
	s.items = append(s.items[:idx], s.items[idx+1:]...)
	if err := s.saveLocked(); err != nil {
		s.items = append(s.items[:idx], append([]Stand{prev}, s.items[idx:]...)...)
		return err
	}
	return nil
}

// Activate switches the active stand and persists the choice.
func (s *Store) Activate(id string) (Stand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexOfLocked(id)
	if idx < 0 {
		return Stand{}, fmt.Errorf("stand %q not found", id)
	}
	prev := s.activeID
	s.activeID = id
	if err := s.saveLocked(); err != nil {
		s.activeID = prev
		return Stand{}, err
	}
	return s.items[idx], nil
}

// ResyncMock refreshes the persisted baseURL of the embedded mock stand,
// whose ephemeral port changes between server restarts.
func (s *Store) ResyncMock(id, liveURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexOfLocked(id)
	if idx < 0 || !s.items[idx].IsMock {
		return fmt.Errorf("stand %q is not the embedded mock", id)
	}
	prev := s.items[idx].BaseURL
	s.items[idx].BaseURL = liveURL
	if err := s.saveLocked(); err != nil {
		s.items[idx].BaseURL = prev
		return err
	}
	return nil
}

func (s *Store) seed(defs ...Stand) {
	if len(defs) == 0 {
		defs = []Stand{{Name: "Основной стенд", BaseURL: fallbackURL}}
	}
	seen := make(map[string]bool, len(defs))
	for _, d := range defs {
		d.ID = uniqueSlug(slugify(d.Name), seen)
		seen[d.ID] = true
		s.items = append(s.items, d)
	}
	s.activeID = s.items[0].ID
}

func (s *Store) normalizeActive() {
	if s.resolveActiveLocked() >= 0 {
		return
	}
	if len(s.items) > 0 {
		s.activeID = s.items[0].ID
	}
}

func (s *Store) indexOfLocked(id string) int {
	for i := range s.items {
		if s.items[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) resolveActiveLocked() int {
	idx := s.indexOfLocked(s.activeID)
	if idx < 0 && len(s.items) > 0 {
		return 0
	}
	return idx
}

func (s *Store) saveLocked() error {
	p := persisted{ActiveID: s.activeID, Stands: s.items}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("stands store marshal: %w", err)
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("stands store write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("stands store rename: %w", err)
	}
	return nil
}

func validate(name, baseURL string) error {
	if name == "" {
		return fmt.Errorf("stand name is required")
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return fmt.Errorf("stand name must be at most %d characters", maxNameLen)
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("stand baseURL must be a valid http(s) URL, got %q", baseURL)
	}
	return nil
}

var cyrToLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if lat, ok := cyrToLat[r]; ok {
			b.WriteString(lat)
			continue
		}
		b.WriteByte('-')
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "stand"
	}
	return slug
}

func uniqueSlug(base string, taken map[string]bool) string {
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}
