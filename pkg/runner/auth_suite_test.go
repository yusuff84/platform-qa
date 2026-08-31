package runner

import (
	"testing"
	"time"

	"locali-e2e-engine/config"
	"locali-e2e-engine/pkg/dsl"
	"locali-e2e-engine/pkg/registry"
	"locali-e2e-engine/testserver"
)

// newMockOrchestrator wires a real orchestrator to the embedded mock backend,
// which mirrors the stand's authentication contract closely enough that a pass
// here means the suite is executable, not merely compilable.
func newMockOrchestrator(t *testing.T) (*TestOrchestrator, func()) {
	t.Helper()

	mock := testserver.NewMockBackend()

	cfg := config.LoadFromEnv()
	cfg.BaseURL = mock.URL()
	// No stand-wide code: force the suite down the debugCode path, the one
	// that has to work unattended.
	cfg.VerificationCode = ""
	cfg.ClientToken, cfg.RestToken, cfg.CourierToken, cfg.AdminToken = "", "", "", ""
	cfg.FixtureClientPhone, cfg.UseTestPhone = "", false

	engine, err := dsl.NewEngine(cfg)
	if err != nil {
		mock.Close()
		t.Fatalf("engine: %v", err)
	}

	return NewTestOrchestrator(engine), mock.Close
}

func TestAuthOTPSuite_PassesAgainstMock(t *testing.T) {
	o, cleanup := newMockOrchestrator(t)
	defer cleanup()

	runs, err := o.RunSuitesGroup([]string{"auth_otp"}, TriggerManual, "")
	if err != nil {
		t.Fatalf("RunSuitesGroup: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}

	run := runs[0]
	if run.Status != "PASSED" {
		t.Fatalf("suite failed: %s\nerror: %s", run.Status, run.Error)
	}

	// Every check declared in the catalog must have actually executed: a suite
	// that silently skips half its checks still reports PASSED.
	suite, ok := registry.Get("auth_otp")
	if !ok {
		t.Fatal("auth_otp missing from the catalog")
	}
	for _, check := range suite.Checks {
		result, exists := run.Results[check.ID]
		if !exists {
			t.Errorf("check %q never ran", check.ID)
			continue
		}
		if result.Status != CheckPassed {
			t.Errorf("check %q: %s — %s", check.ID, result.Status, result.Message)
		}
	}
	if run.FailedChecks != 0 {
		t.Errorf("failed checks: %d", run.FailedChecks)
	}
	if run.PassedChecks != len(suite.Checks) {
		t.Errorf("passed checks: %d, want %d", run.PassedChecks, len(suite.Checks))
	}
}

func TestSuiteCatalogIsFullyDispatchable(t *testing.T) {
	// A key present in the catalog but missing from executeSuite's switch
	// surfaces in the UI and fails with "unknown suite key" only when clicked.
	o, cleanup := newMockOrchestrator(t)
	defer cleanup()

	for _, suite := range registry.All() {
		if !o.isValidSuiteKey(suite.Key) {
			t.Errorf("suite %q is in the catalog but not runnable", suite.Key)
		}
	}
	if !o.isValidSuiteKey("all") {
		t.Error(`the virtual key "all" must stay runnable`)
	}
}

// TestOrderSuites_PassAgainstMock covers the suites rewritten for the
// cart-based order contract. They drive the orchestrator the same way the UI
// does, so a broken fixture chain or a stale payload fails here rather than on
// a stand.
func TestOrderSuites_PassAgainstMock(t *testing.T) {
	for _, key := range []string{"flow_a", "flow_b", "cancellation", "idempotency"} {
		t.Run(key, func(t *testing.T) {
			o, cleanup := newMockOrchestrator(t)
			defer cleanup()

			runs, err := o.RunSuitesGroup([]string{key}, TriggerManual, "")
			if err != nil {
				t.Fatalf("RunSuitesGroup(%s): %v", key, err)
			}

			run := runs[0]
			if run.Status != "PASSED" {
				t.Fatalf("suite %s failed: %s", key, run.Error)
			}

			suite, ok := registry.Get(key)
			if !ok {
				t.Fatalf("suite %s missing from the catalog", key)
			}
			for _, check := range suite.Checks {
				result, exists := run.Results[check.ID]
				if !exists {
					t.Errorf("check %q never ran", check.ID)
					continue
				}
				// SKIPPED is a legitimate outcome (a closed parcel window);
				// FAILED is not.
				if result.Status == CheckFailed {
					t.Errorf("check %q failed: %s", check.ID, result.Message)
				}
			}
		})
	}
}

// TestRunStatusReflectsCheckOutcomes guards the accounting the UI renders: a
// run that skipped or failed checks must not report itself as passed.
func TestRunStatusReflectsCheckOutcomes(t *testing.T) {
	o, cleanup := newMockOrchestrator(t)
	defer cleanup()

	run := o.newRun("flow_a")
	run.Results = map[string]CheckResult{}

	o.checkStart(run, "flow_a", "setup")
	o.checkDone(run, "flow_a", "setup", true, "ок", time.Now())
	o.checkSkip(run, "flow_a", "create_order", "нет предусловия")

	if run.SkippedChecks != 1 {
		t.Fatalf("пропуск не посчитан: %d", run.SkippedChecks)
	}
	// Повторный пропуск того же чека не должен удваивать счётчик.
	o.checkSkip(run, "flow_a", "create_order", "нет предусловия")
	if run.SkippedChecks != 1 {
		t.Fatalf("повторный пропуск удвоил счётчик: %d", run.SkippedChecks)
	}
	if run.PassedChecks != 1 {
		t.Fatalf("пройденная проверка не посчитана: %d", run.PassedChecks)
	}

	o.checkStart(run, "flow_a", "assign_courier")
	o.checkDone(run, "flow_a", "assign_courier", false, "сломалось", time.Now())
	if run.FailedChecks != 1 {
		t.Fatalf("провал не посчитан: %d", run.FailedChecks)
	}
}

func TestAPISuitesRunEveryCheckIndependently(t *testing.T) {
	// Сценарий обрывается на первом провале, а API-набор обязан выполнить все
	// проверки: одна сломанная ручка не должна скрывать состояние остальных.
	o, cleanup := newMockOrchestrator(t)
	defer cleanup()

	for _, key := range apiSuiteKeys() {
		suite, ok := registry.Get(key)
		if !ok {
			t.Fatalf("сьют %s отсутствует в каталоге", key)
		}

		runs, err := o.RunSuitesGroup([]string{key}, TriggerManual, "")
		if err != nil {
			t.Fatalf("RunSuitesGroup(%s): %v", key, err)
		}
		run := runs[0]

		for _, check := range suite.Checks {
			if _, exists := run.Results[check.ID]; !exists {
				t.Errorf("%s: проверка %q не выполнилась", key, check.ID)
			}
		}
		if got := len(run.Results); got != len(suite.Checks) {
			t.Errorf("%s: выполнено %d проверок из %d", key, got, len(suite.Checks))
		}
	}
}
