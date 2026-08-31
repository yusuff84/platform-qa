package registry

import "testing"

func TestCatalogOrderAndKeys(t *testing.T) {
	want := []string{
		"flow_a", "flow_b", "cancellation", "idempotency", "security_rbac", "auth_otp",
		"api_menu", "api_modifiers", "api_cart", "api_wallet", "api_order_status", "api_guards",
		"negative_sm",
	}
	keys := Keys()
	if len(keys) != len(want) {
		t.Fatalf("Keys() = %v, want %v", keys, want)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Fatalf("Keys()[%d] = %q, want %q", i, keys[i], k)
		}
	}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("All() length = %d, want %d", len(all), len(want))
	}
}

func TestGet(t *testing.T) {
	s, ok := Get("flow_a")
	if !ok || s.Key != "flow_a" || s.Category != "flow" || len(s.Checks) != 7 {
		t.Fatalf("Get(flow_a) unexpected: %+v ok=%v", s, ok)
	}
	if _, ok := Get("all"); ok {
		t.Fatal("virtual key 'all' must not be in the catalog")
	}
	if _, ok := Get("nope"); ok {
		t.Fatal("unknown key must not be found")
	}
}

func TestCheckIDsStableAndUnique(t *testing.T) {
	for _, s := range All() {
		if len(s.Checks) == 0 {
			t.Fatalf("suite %s has no checks", s.Key)
		}
		seen := map[string]bool{}
		for _, c := range s.Checks {
			if seen[c.ID] {
				t.Fatalf("duplicate check id %s in suite %s", c.ID, s.Key)
			}
			seen[c.ID] = true
			for _, r := range c.ID {
				if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
					t.Fatalf("check id %q is not snake_case", c.ID)
				}
			}
		}
	}
}

func TestAPISuitesAreGroupedAndLabelled(t *testing.T) {
	// The UI splits the catalog into scenarios and endpoint checks by
	// category, so an api_* suite left in another category silently lands in
	// the wrong section.
	for _, s := range All() {
		isAPIKey := len(s.Key) > 4 && s.Key[:4] == "api_"
		if isAPIKey && s.Category != "api" {
			t.Errorf("сьют %s начинается с api_, но категория %q", s.Key, s.Category)
		}
		if !isAPIKey && s.Category == "api" {
			t.Errorf("сьют %s в категории api, но ключ не api_*", s.Key)
		}
	}
}

func TestEveryCheckHasATitle(t *testing.T) {
	for _, s := range All() {
		for _, c := range s.Checks {
			if c.Title == "" {
				t.Errorf("проверка %s.%s без заголовка — в UI будет пустая строка", s.Key, c.ID)
			}
		}
	}
}
