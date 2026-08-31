package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"locali-e2e-engine/pkg/client"
	"locali-e2e-engine/pkg/registry"
	"locali-e2e-engine/pkg/scenario"
)

const maxDelayMs = 30000

var varPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// runUserScenario interprets a user-defined custom scenario.
// Every step produces exactly one check; the first failing step stops the run
// (remaining checks are finalized as SKIPPED by executeSuite/finalizeChecks).
func (o *TestOrchestrator) runUserScenario(ctx context.Context, run *TestRun, key string) error {
	scen := o.customScenario(key)
	if scen == nil {
		return fmt.Errorf("unknown suite key: %s", key)
	}

	suiteName := o.suiteTitle(key)
	o.Emit(&ExecutionEvent{
		RunID:     run.ID,
		SuiteName: suiteName,
		SuiteKey:  key,
		StepType:  "SUITE_START",
		Level:     LogInfo,
		Message:   fmt.Sprintf("Запуск пользовательского сценария «%s» (%d шагов)", scen.Title, len(scen.Steps)),
		Timestamp: time.Now(),
	})

	// dependsOn is never auto-executed: only verify that referenced keys exist somewhere.
	for _, dep := range scen.DependsOn {
		if _, ok := registry.Get(dep); !ok && !o.hasCustom(dep) {
			o.Emit(&ExecutionEvent{
				RunID:     run.ID,
				SuiteName: suiteName,
				SuiteKey:  key,
				StepType:  "INFO",
				Level:     LogWarn,
				Message:   fmt.Sprintf("⚠ Зависимость %q не найдена ни во встроенном каталоге, ни среди пользовательских сценариев", dep),
				Timestamp: time.Now(),
			})
		}
	}

	vars := make(map[string]string, len(scen.Vars)+4)
	for k, v := range scen.Vars {
		vars[k] = v
	}

	for i := range scen.Steps {
		step := scen.Steps[i]
		title := step.Title
		if title == "" {
			title = step.ID
		}

		started := time.Now()
		o.checkStartTitled(run, key, step.ID, title)

		msg, stepErr := o.execUserStep(ctx, &step, vars)
		if stepErr != nil {
			o.checkDone(run, key, step.ID, false, stepErr.Error(), started)
			return fmt.Errorf("шаг %d/%d (%s): %w", i+1, len(scen.Steps), title, stepErr)
		}

		run.PassedSteps++
		o.checkDone(run, key, step.ID, true, msg, started)
	}

	return nil
}

func (o *TestOrchestrator) execUserStep(ctx context.Context, step *scenario.Step, vars map[string]string) (string, error) {
	switch step.Type {
	case "http":
		return o.execUserHTTPStep(ctx, step, vars)
	case "delay":
		return execUserDelayStep(step)
	case "assert":
		return execUserAssertStep(step, vars)
	default:
		return "", fmt.Errorf("неизвестный тип шага %q", step.Type)
	}
}

// execUserHTTPStep performs an HTTP call and evaluates expectStatus/asserts/extract.
// A non-2xx response is NOT automatically a failure: it is matched against expectStatus.
func (o *TestOrchestrator) execUserHTTPStep(ctx context.Context, step *scenario.Step, vars map[string]string) (string, error) {
	token := o.roleToken(step.Role)
	path := expandVars(step.Path, vars)

	headers := make(map[string]string, len(step.Headers))
	for k, v := range step.Headers {
		headers[expandVars(k, vars)] = expandVars(v, vars)
	}

	var reqBody interface{}
	if len(step.Body) > 0 {
		reqBody = substituteJSON(step.Body, vars)
	}

	var rawResp string
	resp, err := o.engine.HTTPClient.RequestWithHeaders(ctx, step.Method, path, token, headers, reqBody, &rawResp)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	if err != nil && status == 0 {
		return "", fmt.Errorf("запрос %s %s не выполнен: %v", step.Method, path, unwrapAPIError(err))
	}

	if !statusMatches(status, step.ExpectStatus) {
		return "", fmt.Errorf("%s %s: ожидался статус %s, получен %d", step.Method, path, describeExpectStatus(step.ExpectStatus), status)
	}

	parsed := parseLooseJSON(rawResp)
	if err != nil && status >= 400 {
		parsed = parseLooseJSON(errorBodyOf(err))
	}

	extracted := make([]string, 0, len(step.Extract))
	for name, jp := range step.Extract {
		if v, ok := resolveJSONPath(parsed, jp); ok {
			vars[name] = stringifyValue(v)
			extracted = append(extracted, name)
		} else {
			return "", fmt.Errorf("extract[%s]: путь %s не найден в ответе", name, jp)
		}
	}
	sortStrings(extracted)

	for j, a := range step.Asserts {
		if err := evalHTTPAssert(parsed, a); err != nil {
			return "", fmt.Errorf("assert #%d (%s): %w", j+1, a.Op, err)
		}
	}

	msg := fmt.Sprintf("%s %s → %d", step.Method, path, status)
	if len(extracted) > 0 {
		msg += fmt.Sprintf("; извлечено переменных: %s", strings.Join(extracted, ", "))
	}
	return msg, nil
}

func execUserDelayStep(step *scenario.Step) (string, error) {
	ms := step.MS
	if ms <= 0 {
		ms = 0
	}
	if ms > maxDelayMs {
		ms = maxDelayMs
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return fmt.Sprintf("пауза %d мс выполнена", ms), nil
}

func execUserAssertStep(step *scenario.Step, vars map[string]string) (string, error) {
	if step.Check == nil {
		return "", fmt.Errorf("шаг типа assert без поля check")
	}
	left := expandVars(step.Left, vars)
	c := *step.Check

	switch c.Op {
	case "notEmpty":
		if strings.TrimSpace(left) == "" {
			return "", fmt.Errorf("значение %q пусто, ожидалось непустое", step.Left)
		}
		return fmt.Sprintf("значение %q = %q — не пусто", step.Left, left), nil
	case "eq":
		want := stringifyValue(c.Value)
		if left != want {
			return "", fmt.Errorf("значение %q: ожидалось %q, получено %q", step.Left, want, left)
		}
		return fmt.Sprintf("значение %q = %q", step.Left, left), nil
	case "neq":
		want := stringifyValue(c.Value)
		if left == want {
			return "", fmt.Errorf("значение %q (%q): ожидалось что угодно кроме %q, получено оно", step.Left, left, want)
		}
		return fmt.Sprintf("значение %q = %q отличается от %q", step.Left, left, want), nil
	case "contains":
		substr := stringifyValue(c.Value)
		if !strings.Contains(left, substr) {
			return "", fmt.Errorf("значение %q (%q) не содержит %q", step.Left, left, substr)
		}
		return fmt.Sprintf("значение %q (%q) содержит %q", step.Left, left, substr), nil
	default:
		return "", fmt.Errorf("неподдерживаемая операция проверки %q", c.Op)
	}
}

// roleToken resolves the active bearer token for the requested role.
// Role "none" (or empty) sends the request without Authorization.
func (o *TestOrchestrator) roleToken(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "none":
		return ""
	case "client":
		return o.engine.SessionMgr.GetClientSession().Token
	case "rest", "restaurant":
		return o.engine.SessionMgr.GetRestSession().Token
	case "courier":
		return o.engine.SessionMgr.GetCourierSession().Token
	case "admin":
		return o.engine.SessionMgr.GetAdminSession().Token
	default:
		return ""
	}
}

// expandVars replaces {{name}} placeholders in a string value.
// {{uuid}} yields a fresh UUID per occurrence, {{today}} the current date;
// other names are looked up in the scenario variables; unknown names stay untouched.
func expandVars(s string, vars map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	return varPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := varPattern.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		switch name {
		case "uuid":
			return uuid.New().String()
		case "today":
			return time.Now().Format("2006-01-02")
		}
		return m
	})
}

// substituteJSON deep-walks a raw JSON document replacing template strings,
// leaving numbers/booleans/null untouched.
func substituteJSON(raw json.RawMessage, vars map[string]string) interface{} {
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return expandVars(string(raw), vars)
	}
	out := substValue(doc, vars)
	return out
}

func substValue(v interface{}, vars map[string]string) interface{} {
	switch t := v.(type) {
	case string:
		return expandVars(t, vars)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, item := range t {
			out[i] = substValue(item, vars)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, item := range t {
			out[k] = substValue(item, vars)
		}
		return out
	default:
		return v
	}
}

// statusMatches validates the HTTP status against expectStatus:
// empty => any status below 400; number => exact match; "Nxx" => class match;
// "!Nxx" => success when the status is NOT in that class (e.g. "!5xx" treats
// 401/403 as contract-compliant while any 5xx is a violation).
func statusMatches(actual int, expect interface{}) bool {
	if expect == nil {
		return actual > 0 && actual < 400
	}
	switch t := expect.(type) {
	case float64:
		return actual == int(t)
	case int:
		return actual == t
	case string:
		class := strings.TrimSpace(t)
		negated := strings.HasPrefix(class, "!")
		if negated {
			class = strings.TrimSpace(class[1:])
		}
		if len(class) == 3 && class[1:] == "xx" {
			want, err := strconv.Atoi(string(class[0]))
			if err != nil || want < 1 || want > 9 {
				return false
			}
			inClass := actual >= want*100 && actual < (want+1)*100
			return inClass != negated
		}
		return false
	default:
		return false
	}
}

func describeExpectStatus(expect interface{}) string {
	if expect == nil {
		return "<400"
	}
	switch t := expect.(type) {
	case float64:
		return strconv.Itoa(int(t))
	case int:
		return strconv.Itoa(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

// evalHTTPAssert evaluates one assertion against a parsed response document.
func evalHTTPAssert(doc interface{}, a scenario.Assert) error {
	val, found := resolveJSONPath(doc, a.Path)
	switch a.Op {
	case "exists":
		if !found {
			return fmt.Errorf("путь %s отсутствует в ответе", a.Path)
		}
		return nil
	case "eq":
		if !found {
			return fmt.Errorf("путь %s отсутствует в ответе", a.Path)
		}
		if !valuesEqual(val, a.Value) {
			return fmt.Errorf("путь %s: ожидалось %s, получено %s", a.Path, stringifyValue(a.Value), stringifyValue(val))
		}
		return nil
	case "neq":
		if found && valuesEqual(val, a.Value) {
			return fmt.Errorf("путь %s: ожидалось любое значение кроме %s, получено оно", a.Path, stringifyValue(a.Value))
		}
		return nil
	case "contains":
		if !found {
			return fmt.Errorf("путь %s отсутствует в ответе", a.Path)
		}
		if !valueContains(val, a.Value) {
			return fmt.Errorf("путь %s: значение %s не содержит %s", a.Path, stringifyValue(val), stringifyValue(a.Value))
		}
		return nil
	default:
		return fmt.Errorf("неподдерживаемая операция %q", a.Op)
	}
}

// resolveJSONPath is a minimal dot-notation JSONPath resolver supporting
// $.a.b[0].c style paths over map/slice documents.
func resolveJSONPath(doc interface{}, path string) (interface{}, bool) {
	p := strings.TrimSpace(path)
	if p == "" || p[0] != '$' {
		return nil, false
	}
	cur := doc
	rest := p[1:]
	for len(rest) > 0 {
		switch {
		case rest[0] == '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			var name string
			if end < 0 {
				name, rest = rest, ""
			} else {
				name, rest = rest[:end], rest[end:]
			}
			if name == "" {
				return nil, false
			}
			m, ok := cur.(map[string]interface{})
			if !ok {
				return nil, false
			}
			cur, ok = m[name]
			if !ok {
				return nil, false
			}
		case rest[0] == '[':
			end := strings.Index(rest, "]")
			if end < 0 {
				return nil, false
			}
			idx, err := strconv.Atoi(strings.TrimSpace(rest[1:end]))
			if err != nil {
				return nil, false
			}
			arr, ok := cur.([]interface{})
			if !ok {
				return nil, false
			}
			if idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			rest = rest[end+1:]
		default:
			return nil, false
		}
	}
	return cur, true
}

func valuesEqual(a, b interface{}) bool {
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af == bf
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return stringifyValue(a) == stringifyValue(b)
}

func valueContains(container, needle interface{}) bool {
	switch t := container.(type) {
	case string:
		return strings.Contains(t, stringifyValue(needle))
	case []interface{}:
		for _, item := range t {
			if valuesEqual(item, needle) {
				return true
			}
		}
		return false
	default:
		return stringifyValue(container) != "" && strings.Contains(stringifyValue(container), stringifyValue(needle))
	}
}

func asFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// stringifyValue renders scalar values for comparison and human-readable messages.
func stringifyValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}

func parseLooseJSON(s string) interface{} {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	var doc interface{}
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil
	}
	return doc
}

// errorBodyOf returns the raw response body the backend sent with a >= 400
// status, so scenario asserts can inspect an error payload the same way they
// inspect a success one.
func errorBodyOf(err error) string {
	if apiErr, ok := client.AsAPIError(err); ok {
		return apiErr.Body
	}
	return ""
}

// unwrapAPIError renders the failure for the operator: the backend's own
// status and message for an API error, the transport error otherwise.
func unwrapAPIError(err error) string {
	if apiErr, ok := client.AsAPIError(err); ok {
		return apiErr.Error()
	}
	return err.Error()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
