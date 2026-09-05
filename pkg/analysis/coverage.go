package analysis

import (
	"regexp"
	"sort"
	"strings"

	"locali-e2e-engine/pkg/registry"
	"locali-e2e-engine/pkg/scenario"
	"locali-e2e-engine/pkg/spec"
)

var (
	pathParamRegex = regexp.MustCompile(`\{[^{}]+\}`)
	tmplVarRegex   = regexp.MustCompile(`\{\{[^{}]+\}\}`)
)

// CoverageBreakdown provides item counts and percentage for a dimension.
type CoverageBreakdown struct {
	Total      int     `json:"total"`
	Covered    int     `json:"covered"`
	Percentage float64 `json:"percentage"`
}

// CoveredEndpoint represents an endpoint verified by tests.
type CoveredEndpoint struct {
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	Tag      string   `json:"tag"`
	Role     string   `json:"role"`
	Summary  string   `json:"summary,omitempty"`
	TestedBy []string `json:"testedBy"`
}

// UncoveredEndpoint represents an endpoint lacking test coverage.
type UncoveredEndpoint struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Tag     string `json:"tag"`
	Role    string `json:"role"`
	Summary string `json:"summary,omitempty"`
}

// CoverageReport is the comprehensive API coverage report.
type CoverageReport struct {
	TotalEndpoints    int                          `json:"totalEndpoints"`
	CoveredEndpoints  int                          `json:"coveredEndpoints"`
	CoveragePercent   float64                      `json:"coveragePercent"`
	ByMethod          map[string]CoverageBreakdown `json:"byMethod"`
	ByTag             map[string]CoverageBreakdown `json:"byTag"`
	ByRole            map[string]CoverageBreakdown `json:"byRole"`
	Covered           []CoveredEndpoint            `json:"covered"`
	Uncovered         []UncoveredEndpoint          `json:"uncovered"`
	TotalSuitesCount  int                          `json:"totalSuitesCount"`
	TotalCustomCount  int                          `json:"totalCustomCount"`
}

// Built-in suites endpoint coverage map: SuiteKey -> []{Method, Path}
var builtinSuitesCoverage = map[string][]struct{ Method, Path string }{
	"api_menu": {
		{"POST", "/rests/categories"},
		{"GET", "/rests/categories"},
		{"POST", "/rests/dishes"},
		{"GET", "/rests/dishes/{id}"},
		{"PATCH", "/rests/dishes/{id}"},
		{"PATCH", "/rests/dishes/{id}/hide"},
		{"PUT", "/rests/stop-list"},
		{"DELETE", "/rests/dish"},
	},
	"api_modifiers": {
		{"POST", "/rests/modificators"},
		{"GET", "/rests/modificators"},
		{"PUT", "/rests/modificators/{id}"},
		{"PATCH", "/rests/modificators/{id}/activity"},
		{"DELETE", "/rests/modificators/{id}"},
		{"POST", "/rests/group-modifiers"},
		{"GET", "/rests/group-modifiers"},
		{"PUT", "/rests/group-modifiers/{id}"},
		{"DELETE", "/rests/group-modifiers/{id}"},
	},
	"api_cart": {
		{"POST", "/cart/items"},
		{"GET", "/cart"},
		{"DELETE", "/cart/groups/{rest}"},
		{"POST", "/clients/create-order"},
	},
	"api_wallet": {
		{"GET", "/couriers/wallet"},
		{"GET", "/couriers/wallet/operations"},
		{"POST", "/couriers/wallet/topup"},
		{"GET", "/rests/wallet"},
		{"GET", "/rests/wallet/operations"},
	},
	"api_order_status": {
		{"POST", "/couriers/take-order"},
		{"POST", "/rests/order-status"},
		{"POST", "/clients/orders/{id}/cancel"},
		{"POST", "/admin/give-order-to-courier"},
	},
	"api_guards": {
		{"GET", "/admin/users"},
		{"POST", "/couriers/active-order"},
		{"GET", "/rests/categories"},
	},
	"flow_a": {
		{"POST", "/clients/register"},
		{"POST", "/clients/verify"},
		{"POST", "/clients/addresses"},
		{"POST", "/clients/create-order"},
		{"POST", "/admin/give-order-to-courier"},
		{"POST", "/rests/order-status"},
		{"POST", "/couriers/change-status"},
		{"GET", "/clients/orders/{id}"},
	},
	"flow_b": {
		{"POST", "/clients/register"},
		{"POST", "/clients/verify"},
		{"POST", "/clients/independent-order"},
		{"POST", "/admin/give-order-to-courier"},
		{"POST", "/couriers/change-status"},
		{"GET", "/clients/orders/{id}"},
	},
	"auth_otp": {
		{"POST", "/clients/register"},
		{"POST", "/clients/verify"},
		{"POST", "/couriers/login"},
		{"POST", "/rests/login"},
		{"POST", "/admin/login"},
	},
	"cancellation": {
		{"POST", "/clients/create-order"},
		{"POST", "/clients/orders/{id}/cancel"},
	},
	"idempotency": {
		{"POST", "/clients/create-order"},
	},
	"security_rbac": {
		{"GET", "/admin/users"},
		{"POST", "/rests/categories"},
	},
	"negative_sm": {
		{"POST", "/couriers/change-status"},
		{"POST", "/admin/give-order-to-courier"},
	},
}

// NormalizeRoute normalizes a path by removing query strings and stripping leading /api.
func NormalizeRoute(p string) string {
	if idx := strings.Index(p, "?"); idx != -1 {
		p = p[:idx]
	}
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Strip /api prefix if present for uniform comparison
	if strings.HasPrefix(p, "/api/") {
		p = strings.TrimPrefix(p, "/api")
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/"
	}
	return p
}

// InferRole returns the logical user role based on endpoint path and tag.
func InferRole(path, tag string) string {
	norm := strings.ToLower(path)
	t := strings.ToLower(tag)

	if strings.Contains(norm, "/admin") || strings.Contains(t, "admin") {
		return "admin"
	}
	if strings.Contains(norm, "/rests") || strings.Contains(norm, "/dishes") ||
		strings.Contains(norm, "/menu") || strings.Contains(norm, "/modificator") ||
		strings.Contains(t, "rest") || strings.Contains(t, "menu") {
		return "rest"
	}
	if strings.Contains(norm, "/courier") || strings.Contains(t, "courier") {
		return "courier"
	}
	if strings.Contains(norm, "/client") || strings.Contains(norm, "/cart") ||
		strings.Contains(norm, "/order") || strings.Contains(t, "client") ||
		strings.Contains(t, "order") || strings.Contains(t, "cart") {
		return "client"
	}
	if strings.Contains(norm, "/auth") || strings.Contains(norm, "/health") ||
		strings.Contains(norm, "/login") || strings.Contains(norm, "/register") {
		return "none"
	}
	return "client"
}

// matchEndpointRoute checks whether a test call (testMethod, testPath) covers a spec endpoint (specMethod, specPath).
func matchEndpointRoute(testMethod, testPath, specMethod, specPath string) bool {
	if !strings.EqualFold(strings.TrimSpace(testMethod), strings.TrimSpace(specMethod)) {
		return false
	}

	normTest := NormalizeRoute(testPath)
	normSpec := NormalizeRoute(specPath)

	if normTest == normSpec {
		return true
	}

	// Replace {placeholder} with temporary token, quote meta, then replace token with [^/]+
	tempSpec := pathParamRegex.ReplaceAllString(normSpec, "PARAMPLACEHOLDER")
	specRegexStr := "^" + strings.ReplaceAll(regexp.QuoteMeta(tempSpec), "PARAMPLACEHOLDER", `[^/]+`) + "$"

	// Normalize test path: replace {{tmpl}} with 1
	concreteTest := tmplVarRegex.ReplaceAllString(normTest, "1")

	matched, err := regexp.MatchString(specRegexStr, concreteTest)
	return err == nil && matched
}

// AnalyzeCoverage calculates API coverage across registered suites and custom scenarios.
func AnalyzeCoverage(meta *spec.Meta, customScenarios []*scenario.Scenario) *CoverageReport {
	report := &CoverageReport{
		ByMethod:         make(map[string]CoverageBreakdown),
		ByTag:            make(map[string]CoverageBreakdown),
		ByRole:           make(map[string]CoverageBreakdown),
		Covered:          make([]CoveredEndpoint, 0),
		Uncovered:        make([]UncoveredEndpoint, 0),
		TotalSuitesCount: len(registry.All()),
		TotalCustomCount: len(customScenarios),
	}

	if meta == nil || len(meta.Endpoints) == 0 {
		return report
	}

	report.TotalEndpoints = len(meta.Endpoints)

	// Collect test references for each spec endpoint
	testedByMap := make(map[int][]string)

	// 1. Check built-in suites
	for sKey, endpoints := range builtinSuitesCoverage {
		for _, bEp := range endpoints {
			for idx, sEp := range meta.Endpoints {
				if matchEndpointRoute(bEp.Method, bEp.Path, sEp.Method, sEp.Path) {
					addTestedBy(testedByMap, idx, sKey)
				}
			}
		}
	}

	// 2. Check custom scenarios
	for _, sc := range customScenarios {
		if sc == nil {
			continue
		}
		for _, step := range sc.Steps {
			if step.Type != "http" {
				continue
			}
			for idx, sEp := range meta.Endpoints {
				if matchEndpointRoute(step.Method, step.Path, sEp.Method, sEp.Path) {
					addTestedBy(testedByMap, idx, sc.Key)
				}
			}
		}
	}

	// Summarize
	for idx, ep := range meta.Endpoints {
		method := strings.ToUpper(strings.TrimSpace(ep.Method))
		tag := ep.Tag
		if strings.TrimSpace(tag) == "" {
			tag = "untagged"
		}
		role := InferRole(ep.Path, tag)

		testers := testedByMap[idx]
		isCovered := len(testers) > 0

		if isCovered {
			sort.Strings(testers)
			testers = uniqueStrings(testers)
			report.Covered = append(report.Covered, CoveredEndpoint{
				Method:   method,
				Path:     ep.Path,
				Tag:      tag,
				Role:     role,
				Summary:  ep.Summary,
				TestedBy: testers,
			})
			report.CoveredEndpoints++
		} else {
			report.Uncovered = append(report.Uncovered, UncoveredEndpoint{
				Method:  method,
				Path:    ep.Path,
				Tag:     tag,
				Role:    role,
				Summary: ep.Summary,
			})
		}

		// Update breakdowns
		updateBreakdown(report.ByMethod, method, isCovered)
		updateBreakdown(report.ByTag, tag, isCovered)
		updateBreakdown(report.ByRole, role, isCovered)
	}

	if report.TotalEndpoints > 0 {
		report.CoveragePercent = float64(report.CoveredEndpoints) / float64(report.TotalEndpoints) * 100.0
	}

	return report
}

func addTestedBy(m map[int][]string, idx int, name string) {
	for _, existing := range m[idx] {
		if existing == name {
			return
		}
	}
	m[idx] = append(m[idx], name)
}

func uniqueStrings(slice []string) []string {
	keys := make(map[string]bool)
	list := []string{}
	for _, entry := range slice {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

func updateBreakdown(m map[string]CoverageBreakdown, key string, covered bool) {
	b := m[key]
	b.Total++
	if covered {
		b.Covered++
	}
	if b.Total > 0 {
		b.Percentage = float64(b.Covered) / float64(b.Total) * 100.0
	}
	m[key] = b
}
