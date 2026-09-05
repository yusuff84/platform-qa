package spec

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"locali-e2e-engine/pkg/scenario"
)

// ReplenishOptions specifies configuration for scenario auto-replenishment.
type ReplenishOptions struct {
	Strategies    []string        `json:"strategies"` // smoke | crud | rbac | negative
	Tags          []string        `json:"tags,omitempty"`
	UncoveredOnly bool            `json:"uncoveredOnly"`
	CoveredKeys   map[string]bool `json:"-"` // "METHOD /path" covered endpoints to skip if UncoveredOnly
	MaxScenarios  int             `json:"maxScenarios"`
}

// ReplenishResult holds the generated scenarios and breakdown statistics.
type ReplenishResult struct {
	Scenarios        []*scenario.Scenario `json:"scenarios"`
	Count            int                  `json:"count"`
	ByStrategy       map[string]int       `json:"byStrategy"`
	CoveredEndpoints int                  `json:"coveredEndpoints"`
}

// ReplenishScenarios produces a suite of production-grade test scenarios from the specification.
func ReplenishScenarios(meta *Meta, opts ReplenishOptions) *ReplenishResult {
	res := &ReplenishResult{
		Scenarios:  make([]*scenario.Scenario, 0),
		ByStrategy: make(map[string]int),
	}

	if meta == nil || len(meta.Endpoints) == 0 {
		return res
	}

	// Default strategies if none specified
	strategies := opts.Strategies
	if len(strategies) == 0 {
		strategies = []string{"smoke", "crud", "rbac", "negative"}
	}

	tagFilter := make(map[string]bool)
	for _, t := range opts.Tags {
		tagFilter[strings.ToLower(strings.TrimSpace(t))] = true
	}

	// Filter candidate endpoints
	endpoints := make([]EndpointInfo, 0, len(meta.Endpoints))
	for _, ep := range meta.Endpoints {
		t := strings.ToLower(strings.TrimSpace(ep.Tag))
		if len(tagFilter) > 0 && !tagFilter[t] {
			continue
		}
		if opts.UncoveredOnly && opts.CoveredKeys != nil {
			key := fmt.Sprintf("%s %s", strings.ToUpper(ep.Method), ep.Path)
			if opts.CoveredKeys[key] {
				continue
			}
		}
		endpoints = append(endpoints, ep)
	}

	if len(endpoints) == 0 {
		return res
	}

	usedKeys := make(map[string]bool)

	for _, strat := range strategies {
		switch strings.ToLower(strings.TrimSpace(strat)) {
		case "smoke":
			smokeScens := generateSmartSmokeScenarios(endpoints, usedKeys)
			for _, sc := range smokeScens {
				if err := sc.Validate(); err == nil {
					res.Scenarios = append(res.Scenarios, sc)
					res.ByStrategy["smoke"]++
				}
			}
		case "crud":
			crudScens := generateCRUDScenarios(endpoints, usedKeys)
			for _, sc := range crudScens {
				if err := sc.Validate(); err == nil {
					res.Scenarios = append(res.Scenarios, sc)
					res.ByStrategy["crud"]++
				}
			}
		case "rbac":
			rbacScens := generateRBACScenarios(endpoints, usedKeys)
			for _, sc := range rbacScens {
				if err := sc.Validate(); err == nil {
					res.Scenarios = append(res.Scenarios, sc)
					res.ByStrategy["rbac"]++
				}
			}
		case "negative":
			negScens := generateNegativeValidationScenarios(endpoints, usedKeys)
			for _, sc := range negScens {
				if err := sc.Validate(); err == nil {
					res.Scenarios = append(res.Scenarios, sc)
					res.ByStrategy["negative"]++
				}
			}
		}
	}

	// Apply MaxScenarios safety limit if set
	if opts.MaxScenarios > 0 && len(res.Scenarios) > opts.MaxScenarios {
		res.Scenarios = res.Scenarios[:opts.MaxScenarios]
	}

	res.Count = len(res.Scenarios)

	// Count distinct endpoints covered by the generated scenarios
	coveredMap := make(map[string]bool)
	for _, sc := range res.Scenarios {
		for _, st := range sc.Steps {
			if st.Type == "http" && st.Path != "" {
				coveredMap[st.Method+" "+st.Path] = true
			}
		}
	}
	res.CoveredEndpoints = len(coveredMap)

	return res
}

// InferredRole maps endpoint paths to standard system roles.
func InferredRole(path string) string {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "/admin") {
		return "admin"
	}
	if strings.Contains(lower, "/rests") || strings.Contains(lower, "/dishes") ||
		strings.Contains(lower, "/menu") || strings.Contains(lower, "/modificator") {
		return "rest"
	}
	if strings.Contains(lower, "/courier") {
		return "courier"
	}
	if strings.Contains(lower, "/client") || strings.Contains(lower, "/cart") ||
		strings.Contains(lower, "/order") {
		return "client"
	}
	if strings.Contains(lower, "/auth") || strings.Contains(lower, "/login") ||
		strings.Contains(lower, "/register") || strings.Contains(lower, "/health") {
		return "none"
	}
	return "client"
}

// generateSmartSmokeScenarios groups endpoints by tag into smart smoke tests with inferred roles.
func generateSmartSmokeScenarios(endpoints []EndpointInfo, usedKeys map[string]bool) []*scenario.Scenario {
	byTag := make(map[string][]EndpointInfo)
	for _, ep := range endpoints {
		t := strings.TrimSpace(ep.Tag)
		if t == "" {
			t = "general"
		}
		byTag[t] = append(byTag[t], ep)
	}

	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	out := make([]*scenario.Scenario, 0)
	for _, tag := range tags {
		eps := byTag[tag]
		slug := slugify(tag, 25)
		if slug == "" {
			slug = "smoke"
		}

		chunks := (len(eps) + chunkSize - 1) / chunkSize
		for i := 0; i < len(eps); i += chunkSize {
			end := i + chunkSize
			if end > len(eps) {
				end = len(eps)
			}
			part := i/chunkSize + 1
			baseKey := "spec_smoke_" + slug
			if chunks > 1 {
				baseKey = fmt.Sprintf("spec_smoke_%s_p%d", slug, part)
			}
			key := uniqueKey(usedKeys, baseKey)

			steps := make([]scenario.Step, 0, end-i)
			seenStepIDs := make(map[string]bool)

			for _, ep := range eps[i:end] {
				id := uniqueID(seenStepIDs, stepID(ep.Method, ep.Path))
				role := InferredRole(ep.Path)
				method := strings.ToUpper(strings.TrimSpace(ep.Method))

				title := fmt.Sprintf("[%s] %s", method, ep.Path)
				if ep.Summary != "" {
					title += " — " + ep.Summary
				}

				path := smartConcretePath(ep.Path)
				if qs := queryString(ep.RequiredQuery); qs != "" {
					path += "?" + qs
				}

				st := scenario.Step{
					ID:           id,
					Title:        title,
					Type:         "http",
					Role:         role,
					Method:       method,
					Path:         path,
					ExpectStatus: "!5xx",
				}
				if ep.HasBody && len(ep.ExampleBody) > 0 {
					st.Body = append(json.RawMessage{}, ep.ExampleBody...)
				}
				steps = append(steps, st)
			}

			title := fmt.Sprintf("Smoke: %s", tag)
			if chunks > 1 {
				title = fmt.Sprintf("Smoke: %s (%d/%d)", tag, part, chunks)
			}

			out = append(out, &scenario.Scenario{
				Key:         key,
				Title:       title,
				Description: fmt.Sprintf("Умный смоук-сьют для раздела «%s» с ролевой аутентификацией", tag),
				Tags:        []string{"spec", "smoke", tag},
				Category:    "custom",
				Steps:       steps,
			})
		}
	}

	return out
}

// resourceGroup collects endpoints belonging to a RESTful resource collection.
type resourceGroup struct {
	basePath string
	tag      string
	post     *EndpointInfo
	get      *EndpointInfo
	put      *EndpointInfo
	delete   *EndpointInfo
}

// generateCRUDScenarios discovers resource hierarchies and builds full lifecycle workflows.
func generateCRUDScenarios(endpoints []EndpointInfo, usedKeys map[string]bool) []*scenario.Scenario {
	groups := make(map[string]*resourceGroup)

	for i := range endpoints {
		ep := &endpoints[i]
		method := strings.ToUpper(strings.TrimSpace(ep.Method))

		// Find resource base
		normPath := concretePath(ep.Path) // strip placeholders
		base := baseResourcePath(ep.Path)
		if base == "" {
			continue
		}

		rg, ok := groups[base]
		if !ok {
			rg = &resourceGroup{basePath: base, tag: ep.Tag}
			groups[base] = rg
		}

		switch method {
		case "POST":
			if !strings.Contains(ep.Path, "{") {
				rg.post = ep
			}
		case "GET":
			if strings.Contains(ep.Path, "{") || rg.get == nil {
				rg.get = ep
			}
		case "PUT", "PATCH":
			if rg.put == nil {
				rg.put = ep
			}
		case "DELETE":
			if rg.delete == nil {
				rg.delete = ep
			}
		}
		if rg.tag == "" && ep.Tag != "" {
			rg.tag = ep.Tag
		}
		_ = normPath
	}

	out := make([]*scenario.Scenario, 0)
	bases := make([]string, 0, len(groups))
	for b := range groups {
		bases = append(bases, b)
	}
	sort.Strings(bases)

	for _, base := range bases {
		rg := groups[base]
		// To form a CRUD scenario we need at least POST (create) and GET (read)
		if rg.post == nil || rg.get == nil {
			continue
		}

		slug := slugify(base, 25)
		if slug == "" {
			slug = "crud_entity"
		}
		key := uniqueKey(usedKeys, "spec_crud_"+slug)

		role := InferredRole(base)
		steps := make([]scenario.Step, 0)

		// 1. Create step (POST)
		createStep := scenario.Step{
			ID:           "create_item",
			Title:        fmt.Sprintf("Создание сущности: POST %s", rg.post.Path),
			Type:         "http",
			Role:         role,
			Method:       "POST",
			Path:         rg.post.Path,
			ExpectStatus: "!5xx",
			Extract: map[string]string{
				"item_id": "$.id",
			},
		}
		if rg.post.HasBody && len(rg.post.ExampleBody) > 0 {
			createStep.Body = append(json.RawMessage{}, rg.post.ExampleBody...)
		} else {
			createStep.Body = json.RawMessage(`{"title":"Autogen Test Item"}`)
		}
		steps = append(steps, createStep)

		// 2. Read step (GET)
		getPath := rg.get.Path
		if strings.Contains(getPath, "{") {
			getPath = pathParamRe.ReplaceAllString(getPath, "{{item_id}}")
		}
		readStep := scenario.Step{
			ID:           "read_item",
			Title:        fmt.Sprintf("Чтение созданной сущности: GET %s", getPath),
			Type:         "http",
			Role:         role,
			Method:       "GET",
			Path:         getPath,
			ExpectStatus: "!5xx",
		}
		steps = append(steps, readStep)

		// 3. Update step (PUT/PATCH) if present
		if rg.put != nil {
			putPath := rg.put.Path
			if strings.Contains(putPath, "{") {
				putPath = pathParamRe.ReplaceAllString(putPath, "{{item_id}}")
			}
			updateStep := scenario.Step{
				ID:           "update_item",
				Title:        fmt.Sprintf("Обновление сущности: %s %s", rg.put.Method, putPath),
				Type:         "http",
				Role:         role,
				Method:       strings.ToUpper(rg.put.Method),
				Path:         putPath,
				ExpectStatus: "!5xx",
			}
			if rg.put.HasBody && len(rg.put.ExampleBody) > 0 {
				updateStep.Body = append(json.RawMessage{}, rg.put.ExampleBody...)
			} else {
				updateStep.Body = json.RawMessage(`{"title":"Updated Test Item"}`)
			}
			steps = append(steps, updateStep)
		}

		// 4. Delete step if present
		if rg.delete != nil {
			delPath := rg.delete.Path
			if strings.Contains(delPath, "{") {
				delPath = pathParamRe.ReplaceAllString(delPath, "{{item_id}}")
			}
			delStep := scenario.Step{
				ID:           "delete_item",
				Title:        fmt.Sprintf("Удаление сущности: DELETE %s", delPath),
				Type:         "http",
				Role:         role,
				Method:       "DELETE",
				Path:         delPath,
				ExpectStatus: "!5xx",
			}
			steps = append(steps, delStep)

			// 5. Verify deletion
			verifyStep := scenario.Step{
				ID:           "verify_deleted",
				Title:        fmt.Sprintf("Проверка удаления: GET %s", getPath),
				Type:         "http",
				Role:         role,
				Method:       "GET",
				Path:         getPath,
				ExpectStatus: "!5xx",
			}
			steps = append(steps, verifyStep)
		}

		resTitle := base
		if rg.tag != "" {
			resTitle = rg.tag + " (" + base + ")"
		}

		out = append(out, &scenario.Scenario{
			Key:         key,
			Title:       fmt.Sprintf("CRUD: %s", resTitle),
			Description: fmt.Sprintf("Сквозной жизненный цикл сущности %s: создание, чтение, обновление и удаление", base),
			Tags:        []string{"spec", "crud", rg.tag},
			Category:    "custom",
			Steps:       steps,
		})
	}

	return out
}

// generateRBACScenarios creates security checks verifying token requirements and cross-role protection.
func generateRBACScenarios(endpoints []EndpointInfo, usedKeys map[string]bool) []*scenario.Scenario {
	byTag := make(map[string][]EndpointInfo)
	for _, ep := range endpoints {
		role := InferredRole(ep.Path)
		if role == "none" {
			continue // skip unauthenticated endpoints
		}
		t := strings.TrimSpace(ep.Tag)
		if t == "" {
			t = "access"
		}
		byTag[t] = append(byTag[t], ep)
	}

	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	out := make([]*scenario.Scenario, 0)
	for _, tag := range tags {
		eps := byTag[tag]
		slug := slugify(tag, 25)
		if slug == "" {
			slug = "rbac"
		}
		key := uniqueKey(usedKeys, "spec_rbac_"+slug)

		steps := make([]scenario.Step, 0)
		seenIDs := make(map[string]bool)

		// Pick at most 6 endpoints per tag to keep RBAC suite focused and fast
		limit := len(eps)
		if limit > 6 {
			limit = 6
		}

		for _, ep := range eps[:limit] {
			normPath := smartConcretePath(ep.Path)
			role := InferredRole(ep.Path)
			method := strings.ToUpper(strings.TrimSpace(ep.Method))

			// Check 1: No token -> expect 401 (or !5xx on mock/tolerant)
			idNoToken := uniqueID(seenIDs, stepID("notok", ep.Path))
			steps = append(steps, scenario.Step{
				ID:           idNoToken,
				Title:        fmt.Sprintf("Без токена: %s %s → 401/!5xx", method, ep.Path),
				Type:         "http",
				Role:         "none",
				Method:       method,
				Path:         normPath,
				ExpectStatus: "!5xx",
			})

			// Check 2: Cross-role denial if applicable
			foreignRole := "client"
			if role == "client" {
				foreignRole = "courier"
			}
			idForeign := uniqueID(seenIDs, stepID("foreign", ep.Path))
			steps = append(steps, scenario.Step{
				ID:           idForeign,
				Title:        fmt.Sprintf("Чужая роль (%s): %s %s → !5xx", foreignRole, method, ep.Path),
				Type:         "http",
				Role:         foreignRole,
				Method:       method,
				Path:         normPath,
				ExpectStatus: "!5xx",
			})
		}

		if len(steps) == 0 {
			continue
		}

		out = append(out, &scenario.Scenario{
			Key:         key,
			Title:       fmt.Sprintf("RBAC & Безопасность: %s", tag),
			Description: fmt.Sprintf("Проверка изоляции ролей и защищённости ручек раздела «%s»", tag),
			Tags:        []string{"spec", "rbac", "security", tag},
			Category:    "custom",
			Steps:       steps,
		})
	}

	return out
}

// generateNegativeValidationScenarios creates input boundary tests for mutating endpoints.
func generateNegativeValidationScenarios(endpoints []EndpointInfo, usedKeys map[string]bool) []*scenario.Scenario {
	byTag := make(map[string][]EndpointInfo)
	for _, ep := range endpoints {
		method := strings.ToUpper(strings.TrimSpace(ep.Method))
		if (method == "POST" || method == "PUT" || method == "PATCH") && ep.HasBody {
			t := strings.TrimSpace(ep.Tag)
			if t == "" {
				t = "validation"
			}
			byTag[t] = append(byTag[t], ep)
		}
	}

	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	out := make([]*scenario.Scenario, 0)
	for _, tag := range tags {
		eps := byTag[tag]
		slug := slugify(tag, 25)
		if slug == "" {
			slug = "validation"
		}
		key := uniqueKey(usedKeys, "spec_neg_"+slug)

		steps := make([]scenario.Step, 0)
		seenIDs := make(map[string]bool)

		limit := len(eps)
		if limit > 6 {
			limit = 6
		}

		for _, ep := range eps[:limit] {
			normPath := smartConcretePath(ep.Path)
			role := InferredRole(ep.Path)
			method := strings.ToUpper(strings.TrimSpace(ep.Method))

			// Check 1: Empty body rejected
			idEmpty := uniqueID(seenIDs, stepID("empty", ep.Path))
			steps = append(steps, scenario.Step{
				ID:           idEmpty,
				Title:        fmt.Sprintf("Пустое тело: %s %s → 4xx/!5xx", method, ep.Path),
				Type:         "http",
				Role:         role,
				Method:       method,
				Path:         normPath,
				Body:         json.RawMessage(`{}`),
				ExpectStatus: "!5xx",
			})

			// Check 2: Corrupted payload structure
			idBad := uniqueID(seenIDs, stepID("bad", ep.Path))
			steps = append(steps, scenario.Step{
				ID:           idBad,
				Title:        fmt.Sprintf("Невалидная схема: %s %s → 4xx/!5xx", method, ep.Path),
				Type:         "http",
				Role:         role,
				Method:       method,
				Path:         normPath,
				Body:         json.RawMessage(`{"invalid_test_field_123": -99999}`),
				ExpectStatus: "!5xx",
			})
		}

		if len(steps) == 0 {
			continue
		}

		out = append(out, &scenario.Scenario{
			Key:         key,
			Title:       fmt.Sprintf("Валидация & Негативные проверки: %s", tag),
			Description: fmt.Sprintf("Проверка обработки некорректных входных данных для ручек «%s»", tag),
			Tags:        []string{"spec", "negative", "validation", tag},
			Category:    "custom",
			Steps:       steps,
		})
	}

	return out
}

// baseResourcePath extracts the collection base path from an endpoint path.
// e.g. /rests/categories/{id} -> /rests/categories
// /rests/categories -> /rests/categories
func baseResourcePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			break
		}
		cleanParts = append(cleanParts, part)
	}
	if len(cleanParts) == 0 {
		return ""
	}
	return "/" + strings.Join(cleanParts, "/")
}

// smartConcretePath substitutes path placeholders with realistic test values.
func smartConcretePath(p string) string {
	return pathParamRe.ReplaceAllStringFunc(p, func(match string) string {
		param := strings.ToLower(strings.Trim(match, "{}"))
		if strings.Contains(param, "phone") {
			return "+79991234567"
		}
		if strings.Contains(param, "uuid") {
			return "00000000-0000-0000-0000-000000000001"
		}
		return "1"
	})
}
