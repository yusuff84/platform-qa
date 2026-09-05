package analysis

import (
	"sort"

	"locali-e2e-engine/pkg/runner"
)

// FlakySuiteInfo details a suite with unstable outcomes.
type FlakySuiteInfo struct {
	SuiteKey       string  `json:"suiteKey"`
	SuiteName      string  `json:"suiteName"`
	Total          int     `json:"total"`
	Passed         int     `json:"passed"`
	Failed         int     `json:"failed"`
	FlakinessRatio float64 `json:"flakinessRatio"` // (min(passed, failed) / total) * 100
}

// StepLatencyInfo details the execution duration of a specific check/step.
type StepLatencyInfo struct {
	CheckID       string `json:"checkId"`
	SuiteKey      string `json:"suiteKey"`
	AvgDurationMs int64  `json:"avgDurationMs"`
	MaxDurationMs int64  `json:"maxDurationMs"`
	Executions    int    `json:"executions"`
}

// QualityMetrics contains aggregate health and performance figures.
type QualityMetrics struct {
	TotalRuns         int               `json:"totalRuns"`
	PassedRuns        int               `json:"passedRuns"`
	FailedRuns        int               `json:"failedRuns"`
	SkippedRuns       int               `json:"skippedRuns"`
	PassRate          float64           `json:"passRate"`
	AvgDurationMs     int64             `json:"avgDurationMs"`
	P50DurationMs     int64             `json:"p50DurationMs"`
	P95DurationMs     int64             `json:"p95DurationMs"`
	StatusBreakdown   map[string]int    `json:"statusBreakdown"`
	FailureCategories map[string]int    `json:"failureCategories"`
	FlakySuites       []FlakySuiteInfo  `json:"flakySuites"`
	SlowestSteps      []StepLatencyInfo `json:"slowestSteps"`
}

// ComputeMetrics aggregates metrics and flakiness data across test runs.
func ComputeMetrics(runs []*runner.TestRun) *QualityMetrics {
	m := &QualityMetrics{
		StatusBreakdown:   make(map[string]int),
		FailureCategories: make(map[string]int),
		FlakySuites:       make([]FlakySuiteInfo, 0),
		SlowestSteps:      make([]StepLatencyInfo, 0),
	}

	if len(runs) == 0 {
		return m
	}

	m.TotalRuns = len(runs)
	durations := make([]int64, 0, len(runs))
	var totalDuration int64

	// Tracking for flakiness: suiteKey -> {passed, failed, total, name}
	type suiteStats struct {
		name   string
		passed int
		failed int
		total  int
	}
	suiteMap := make(map[string]*suiteStats)

	// Tracking for step latencies: checkID -> {totalDuration, maxDuration, count, suiteKey}
	type stepStat struct {
		suiteKey string
		totalDur int64
		maxDur   int64
		count    int
	}
	stepMap := make(map[string]*stepStat)

	for _, run := range runs {
		if run == nil {
			continue
		}

		m.StatusBreakdown[run.Status]++
		durations = append(durations, run.DurationMs)
		totalDuration += run.DurationMs

		switch run.Status {
		case runner.RunPassed:
			m.PassedRuns++
		case runner.RunFailed:
			m.FailedRuns++
			diag := DiagnoseRun(run)
			if diag != nil && diag.Category != "" {
				m.FailureCategories[diag.Category]++
			}
		case runner.RunSkipped:
			m.SkippedRuns++
		}

		// Suite tracking
		sKey := run.SuiteKey
		if sKey != "" {
			st, ok := suiteMap[sKey]
			if !ok {
				st = &suiteStats{name: run.SuiteName}
				suiteMap[sKey] = st
			}
			st.total++
			if run.Status == runner.RunPassed {
				st.passed++
			} else if run.Status == runner.RunFailed {
				st.failed++
			}
		}

		// Check results latency tracking
		for checkID, res := range run.Results {
			if res.DurationMs <= 0 {
				continue
			}
			st, ok := stepMap[checkID]
			if !ok {
				st = &stepStat{suiteKey: run.SuiteKey}
				stepMap[checkID] = st
			}
			st.count++
			st.totalDur += res.DurationMs
			if res.DurationMs > st.maxDur {
				st.maxDur = res.DurationMs
			}
		}
	}

	if m.TotalRuns > 0 {
		m.PassRate = float64(m.PassedRuns) / float64(m.TotalRuns) * 100.0
		m.AvgDurationMs = totalDuration / int64(m.TotalRuns)

		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		p50Idx := int(float64(len(durations)) * 0.50)
		p95Idx := int(float64(len(durations)) * 0.95)
		if p50Idx >= len(durations) {
			p50Idx = len(durations) - 1
		}
		if p95Idx >= len(durations) {
			p95Idx = len(durations) - 1
		}
		m.P50DurationMs = durations[p50Idx]
		m.P95DurationMs = durations[p95Idx]
	}

	// Calculate Flaky suites (both passed and failed, min total >= 2)
	for sKey, st := range suiteMap {
		if st.total >= 2 && st.passed > 0 && st.failed > 0 {
			minCount := st.passed
			if st.failed < minCount {
				minCount = st.failed
			}
			flakiness := (float64(minCount*2) / float64(st.total)) * 100.0
			m.FlakySuites = append(m.FlakySuites, FlakySuiteInfo{
				SuiteKey:       sKey,
				SuiteName:      st.name,
				Total:          st.total,
				Passed:         st.passed,
				Failed:         st.failed,
				FlakinessRatio: flakiness,
			})
		}
	}
	sort.Slice(m.FlakySuites, func(i, j int) bool {
		return m.FlakySuites[i].FlakinessRatio > m.FlakySuites[j].FlakinessRatio
	})

	// Calculate Slowest steps
	for checkID, st := range stepMap {
		if st.count > 0 {
			avg := st.totalDur / int64(st.count)
			m.SlowestSteps = append(m.SlowestSteps, StepLatencyInfo{
				CheckID:       checkID,
				SuiteKey:      st.suiteKey,
				AvgDurationMs: avg,
				MaxDurationMs: st.maxDur,
				Executions:    st.count,
			})
		}
	}
	sort.Slice(m.SlowestSteps, func(i, j int) bool {
		return m.SlowestSteps[i].AvgDurationMs > m.SlowestSteps[j].AvgDurationMs
	})
	if len(m.SlowestSteps) > 10 {
		m.SlowestSteps = m.SlowestSteps[:10]
	}

	return m
}
