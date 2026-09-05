package analysis_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"locali-e2e-engine/pkg/analysis"
	"locali-e2e-engine/pkg/runner"
	"locali-e2e-engine/pkg/scenario"
	"locali-e2e-engine/pkg/spec"
)

func TestCoverageAnalysis(t *testing.T) {
	meta := &spec.Meta{
		Key:   "test_api",
		Title: "Test API",
		Endpoints: []spec.EndpointInfo{
			{Method: "POST", Path: "/rests/categories", Tag: "menu"},
			{Method: "GET", Path: "/rests/categories", Tag: "menu"},
			{Method: "GET", Path: "/admin/users", Tag: "admin"},
			{Method: "POST", Path: "/custom/action", Tag: "custom"},
			{Method: "GET", Path: "/uncovered/endpoint", Tag: "unknown"},
		},
	}

	custom := []*scenario.Scenario{
		{
			Key: "custom_scen_1",
			Steps: []scenario.Step{
				{
					ID:     "step_1",
					Type:   "http",
					Method: "POST",
					Path:   "/api/custom/action",
				},
			},
		},
	}

	report := analysis.AnalyzeCoverage(meta, custom)
	require.NotNil(t, report)

	assert.Equal(t, 5, report.TotalEndpoints)
	// /rests/categories (POST and GET) covered by api_menu
	// /admin/users covered by api_guards
	// /custom/action covered by custom_scen_1
	// /uncovered/endpoint is uncovered
	assert.Equal(t, 4, report.CoveredEndpoints)
	assert.Equal(t, 1, len(report.Uncovered))
	assert.Equal(t, "/uncovered/endpoint", report.Uncovered[0].Path)
	assert.InDelta(t, 80.0, report.CoveragePercent, 0.1)

	// Check ByRole breakdown
	assert.Contains(t, report.ByRole, "admin")
	assert.Contains(t, report.ByRole, "rest")
}

func TestDiagnoseRun(t *testing.T) {
	t.Run("passed run", func(t *testing.T) {
		run := &runner.TestRun{
			ID:           "run-1",
			SuiteKey:     "flow_a",
			SuiteName:    "Flow A",
			Status:       runner.RunPassed,
			PassedChecks: 7,
			TotalChecks:  7,
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatHealthy, diag.Category)
		assert.Equal(t, analysis.SeverityNone, diag.Severity)
	})

	t.Run("rate limit run", func(t *testing.T) {
		run := &runner.TestRun{
			ID:        "run-2",
			SuiteKey:  "auth_otp",
			SuiteName: "Auth OTP",
			Status:    runner.RunFailed,
			Error:     "POST /api/clients/register: HTTP 429 Too Many Requests, retry-after: 60s",
			Results: map[string]runner.CheckResult{
				"otp_issued": {Status: runner.CheckFailed, Message: "429 Too Many Requests"},
			},
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatRateLimit, diag.Category)
		assert.Equal(t, analysis.SeverityHigh, diag.Severity)
		assert.Contains(t, diag.Recommendation, "60 секунд")
	})

	t.Run("401 unauthorized", func(t *testing.T) {
		run := &runner.TestRun{
			ID:        "run-3",
			SuiteKey:  "api_menu",
			SuiteName: "Menu",
			Status:    runner.RunFailed,
			Error:     "POST /api/rests/dishes: 401 NO_TOKEN_INCLUDED",
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatAuthExpired, diag.Category)
		assert.Contains(t, diag.Recommendation, "Токены и доступ")
	})

	t.Run("403 forbidden rbac", func(t *testing.T) {
		run := &runner.TestRun{
			ID:        "run-4",
			SuiteKey:  "api_guards",
			SuiteName: "Guards",
			Status:    runner.RunFailed,
			Error:     "GET /api/admin/users: 403 COURIER_ONLY ролевой гейт",
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatRBACForbidden, diag.Category)
	})

	t.Run("409 state conflict", func(t *testing.T) {
		run := &runner.TestRun{
			ID:        "run-5",
			SuiteKey:  "cancellation",
			SuiteName: "Cancel",
			Status:    runner.RunFailed,
			Error:     "POST /api/clients/orders/1/cancel: 409 CANCEL_NOT_ALLOWED",
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatStateConflict, diag.Category)
		assert.Contains(t, diag.Recommendation, "PREPARING")
	})

	t.Run("outside working hours", func(t *testing.T) {
		run := &runner.TestRun{
			ID:        "run-6",
			SuiteKey:  "flow_b",
			SuiteName: "Parcel",
			Status:    runner.RunFailed,
			Error:     "POST /api/clients/independent-order: 422 OUTSIDE_WORKING_HOURS",
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatOutsideHours, diag.Category)
	})

	t.Run("server 500 panic", func(t *testing.T) {
		run := &runner.TestRun{
			ID:        "run-7",
			SuiteKey:  "flow_a",
			SuiteName: "Flow A",
			Status:    runner.RunFailed,
			Error:     "POST /api/clients/create-order: 500 Internal Server Error panic",
		}
		diag := analysis.DiagnoseRun(run)
		assert.Equal(t, analysis.CatServerPanic, diag.Category)
		assert.Equal(t, analysis.SeverityCritical, diag.Severity)
	})
}

func TestComputeMetrics(t *testing.T) {
	now := time.Now()
	runs := []*runner.TestRun{
		{
			ID:         "r1",
			SuiteKey:   "flow_a",
			SuiteName:  "Flow A",
			Status:     runner.RunPassed,
			DurationMs: 100,
			StartTime:  now.Add(-3 * time.Minute),
			Results: map[string]runner.CheckResult{
				"step1": {Status: runner.CheckPassed, DurationMs: 50},
			},
		},
		{
			ID:         "r2",
			SuiteKey:   "flow_a",
			SuiteName:  "Flow A",
			Status:     runner.RunFailed,
			DurationMs: 200,
			StartTime:  now.Add(-2 * time.Minute),
			Error:      "429 rate limit",
			Results: map[string]runner.CheckResult{
				"step1": {Status: runner.CheckFailed, DurationMs: 150},
			},
		},
		{
			ID:         "r3",
			SuiteKey:   "api_menu",
			SuiteName:  "Menu",
			Status:     runner.RunPassed,
			DurationMs: 300,
			StartTime:  now.Add(-1 * time.Minute),
			Results: map[string]runner.CheckResult{
				"dish_create": {Status: runner.CheckPassed, DurationMs: 250},
			},
		},
	}

	metrics := analysis.ComputeMetrics(runs)
	require.NotNil(t, metrics)

	assert.Equal(t, 3, metrics.TotalRuns)
	assert.Equal(t, 2, metrics.PassedRuns)
	assert.Equal(t, 1, metrics.FailedRuns)
	assert.InDelta(t, 66.66, metrics.PassRate, 0.1)
	assert.Equal(t, int64(200), metrics.AvgDurationMs)

	// Check failure categories
	assert.Equal(t, 1, metrics.FailureCategories[analysis.CatRateLimit])

	// Check flakiness: flow_a had 1 pass and 1 fail
	require.Len(t, metrics.FlakySuites, 1)
	assert.Equal(t, "flow_a", metrics.FlakySuites[0].SuiteKey)

	// Check slowest steps: dish_create had 250ms avg
	require.NotEmpty(t, metrics.SlowestSteps)
	assert.Equal(t, "dish_create", metrics.SlowestSteps[0].CheckID)
}
