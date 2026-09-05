// Package scenario defines the user-defined custom scenario model and its
// JSON-file backed storage. Scenarios are interpreted by pkg/runner at runtime.
package scenario

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	keyRe       = regexp.MustCompile(`^[a-z][a-z0-9_]{2,39}$`)
	stepIDRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	classRe     = regexp.MustCompile(`^[2-5]xx$`)
	negClassRe  = regexp.MustCompile(`^![2-5]xx$`)

	allowedRoles   = map[string]bool{"client": true, "rest": true, "courier": true, "admin": true, "none": true, "": true}
	allowedTypes   = map[string]bool{"http": true, "delay": true, "assert": true, "condition": true}
	httpAssertOps  = map[string]bool{"eq": true, "neq": true, "contains": true, "exists": true}
	checkAssertOps = map[string]bool{"notEmpty": true, "eq": true, "neq": true, "contains": true}
	allowedMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}
)

// Assert is a verifiable condition against an HTTP response body (JSONPath).
type Assert struct {
	Path  string      `json:"path,omitempty"` // JSONPath from the response, e.g. $.status
	Op    string      `json:"op"`             // eq | neq | contains | exists
	Value interface{} `json:"value,omitempty"`
}

// StepCondition defines branching logic for non-linear graph scenarios.
type StepCondition struct {
	Left  string      `json:"left"`            // e.g. "{{status}}" or "{{orderId}}"
	Op    string      `json:"op"`              // notEmpty | eq | neq | contains
	Value interface{} `json:"value,omitempty"`
	Then  string      `json:"then"`            // Target step ID if condition is true
	Else  string      `json:"else,omitempty"`  // Target step ID if condition is false
}

// Step is a single executable action of a custom scenario.
type Step struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Type         string            `json:"type"` // http | delay | assert | condition
	Role         string            `json:"role,omitempty"` // client|rest|courier|admin|none
	Method       string            `json:"method,omitempty"`
	Path         string            `json:"path,omitempty"`
	Body         json.RawMessage   `json:"body,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Extract      map[string]string `json:"extract,omitempty"` // varName -> JSONPath
	ExpectStatus interface{}       `json:"expectStatus,omitempty"` // float64 (int) or "2xx"/"4xx"/"5xx"/"!4xx"/"!5xx"; empty => any <400
	Asserts      []Assert          `json:"asserts,omitempty"`
	MS           int               `json:"ms,omitempty"`    // delay duration in ms
	Check        *Assert           `json:"check,omitempty"` // type=assert: left={{var}}, op: notEmpty|eq|neq|contains
	Left         string            `json:"left,omitempty"`
	Next         string            `json:"next,omitempty"`      // explicit next step ID (non-linear jump)
	OnFailure    string            `json:"onFailure,omitempty"` // fallback step ID if this step fails
	Condition    *StepCondition    `json:"condition,omitempty"` // branch condition definition
}

// Scenario is a user-defined test scenario persisted as <DataDir>/scenarios/<key>.json.
type Scenario struct {
	Key         string            `json:"key"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Category    string            `json:"category"` // always "custom", set by the server
	DependsOn   []string          `json:"dependsOn,omitempty"`
	Vars        map[string]string `json:"vars,omitempty"`
	Steps       []Step            `json:"steps"`
}

// Validate checks the scenario against the platform contract.
func (s *Scenario) Validate() error {
	if !keyRe.MatchString(s.Key) {
		return fmt.Errorf("scenario.key: должен соответствовать ^[a-z][a-z0-9_]{2,39}$, получено %q", s.Key)
	}
	if s.Title == "" {
		return fmt.Errorf("scenario.title: обязателен")
	}
	if len(s.Steps) < 1 || len(s.Steps) > 50 {
		return fmt.Errorf("scenario.steps: требуется от 1 до 50 шагов, получено %d", len(s.Steps))
	}

	seen := map[string]bool{}
	for i, st := range s.Steps {
		prefix := fmt.Sprintf("steps[%d]", i)
		if st.ID == "" {
			return fmt.Errorf("%s.id: обязателен", prefix)
		}
		if !stepIDRe.MatchString(st.ID) {
			return fmt.Errorf("%s.id: должен быть snake_case ^[a-z][a-z0-9_]*$, получено %q", prefix, st.ID)
		}
		if seen[st.ID] {
			return fmt.Errorf("%s.id: дубликат id %q", prefix, st.ID)
		}
		seen[st.ID] = true

		if !allowedTypes[st.Type] {
			return fmt.Errorf("%s.type: допустимы http|delay|assert, получено %q", prefix, st.Type)
		}
		switch st.Type {
		case "http":
			if !allowedRoles[st.Role] {
				return fmt.Errorf("%s.role: допустимы client|rest|courier|admin|none, получено %q", prefix, st.Role)
			}
			if !allowedMethods[strings.ToUpper(st.Method)] {
				return fmt.Errorf("%s.method: некорректный HTTP метод %q", prefix, st.Method)
			}
			if !strings.HasPrefix(st.Path, "/") {
				return fmt.Errorf("%s.path: должен начинаться с /, получено %q", prefix, st.Path)
			}
			for name, jp := range st.Extract {
				if jp == "" {
					return fmt.Errorf("%s.extract[%s]: JSONPath не может быть пустым", prefix, name)
				}
			}
			if err := validateExpectStatus(st.ExpectStatus); err != nil {
				return fmt.Errorf("%s.expectStatus: %v", prefix, err)
			}
			for j, a := range st.Asserts {
				if err := validateHTTPAssert(a); err != nil {
					return fmt.Errorf("%s.asserts[%d]: %v", prefix, j, err)
				}
			}
		case "assert":
			if st.Check == nil {
				return fmt.Errorf("%s.check: обязателен для шага типа assert", prefix)
			}
			if !checkAssertOps[st.Check.Op] {
				return fmt.Errorf("%s.check.op: допустимы notEmpty|eq|neq|contains, получено %q", prefix, st.Check.Op)
			}
		case "condition":
			if st.Condition == nil {
				return fmt.Errorf("%s.condition: обязателен для шага типа condition", prefix)
			}
			if !checkAssertOps[st.Condition.Op] {
				return fmt.Errorf("%s.condition.op: допустимы notEmpty|eq|neq|contains, получено %q", prefix, st.Condition.Op)
			}
			if st.Condition.Then == "" {
				return fmt.Errorf("%s.condition.then: обязателен целевой шаг при истинном условии", prefix)
			}
		case "delay":
			if st.MS < 0 || st.MS > 30000 {
				return fmt.Errorf("%s.ms: должно быть в диапазоне 0..30000, получено %d", prefix, st.MS)
			}
		}
	}

	for i, st := range s.Steps {
		prefix := fmt.Sprintf("steps[%d]", i)
		if st.Next != "" && !seen[st.Next] {
			return fmt.Errorf("%s.next: целевой шаг %q не найден в сценарии", prefix, st.Next)
		}
		if st.OnFailure != "" && !seen[st.OnFailure] {
			return fmt.Errorf("%s.onFailure: целевой шаг %q не найден в сценарии", prefix, st.OnFailure)
		}
		if st.Condition != nil {
			if st.Condition.Then != "" && !seen[st.Condition.Then] {
				return fmt.Errorf("%s.condition.then: целевой шаг %q не найден в сценарии", prefix, st.Condition.Then)
			}
			if st.Condition.Else != "" && !seen[st.Condition.Else] {
				return fmt.Errorf("%s.condition.else: целевой шаг %q не найден в сценарии", prefix, st.Condition.Else)
			}
		}
	}

	for _, dep := range s.DependsOn {
		if dep == s.Key {
			return fmt.Errorf("scenario.dependsOn: не может содержать собственный key %q", s.Key)
		}
	}

	return nil
}

func validateExpectStatus(v interface{}) error {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		if t != float64(int(t)) || int(t) < 100 || int(t) > 599 {
			return fmt.Errorf("числовой статус должен быть целым числом 100..599, получено %v", t)
		}
		return nil
	case int:
		if t < 100 || t > 599 {
			return fmt.Errorf("числовой статус должен быть целым числом 100..599, получено %d", t)
		}
		return nil
	case string:
		if classRe.MatchString(t) || negClassRe.MatchString(t) {
			return nil
		}
		return fmt.Errorf("строковый статус должен быть классом вида \"2xx\"/\"4xx\"/\"5xx\" или отрицанием \"!4xx\"/\"!5xx\", получено %q", t)
	default:
		return fmt.Errorf("ожидается число 100..599 или строка класса ответа, получен тип %T", v)
	}
}

func validateHTTPAssert(a Assert) error {
	if !httpAssertOps[a.Op] {
		return fmt.Errorf("op: допустимы eq|neq|contains|exists, получено %q", a.Op)
	}
	if a.Path == "" {
		return fmt.Errorf("path: обязателен")
	}
	if !strings.HasPrefix(a.Path, "$") {
		return fmt.Errorf("path: должен быть JSONPath начиная с $, получено %q", a.Path)
	}
	return nil
}
