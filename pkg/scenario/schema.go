package scenario

// GetJSONSchema returns a standard draft-07 JSON Schema describing custom scenarios.
func GetJSONSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"title":       "LocaliE2EScenario",
		"description": "Specification for Locali E2E test scenarios, fully compatible with LLM code generation (Claude, Codex, GPT-4)",
		"type":        "object",
		"required":    []string{"key", "title", "steps"},
		"properties": map[string]interface{}{
			"key": map[string]interface{}{
				"type":        "string",
				"pattern":     "^[a-z][a-z0-9_]{2,39}$",
				"description": "Unique scenario key in snake_case (^[a-z][a-z0-9_]{2,39}$)",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Human readable title of the test scenario",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Detailed explanation of what the test scenario verifies",
			},
			"tags": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
				"description": "Tags for categorization and filtering, e.g. ['smoke', 'order', 'client']",
			},
			"vars": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": map[string]interface{}{"type": "string"},
				"description":          "Initial variables map. Supports {{uuid}} and {{today}}, e.g. {'phone_suffix': '{{uuid}}'}",
			},
			"dependsOn": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
				"description": "Keys of prerequisite suites that must succeed before this scenario",
			},
			"steps": map[string]interface{}{
				"type":        "array",
				"minItems":    1,
				"maxItems":    50,
				"description": "Sequential steps executed in order",
				"items": map[string]interface{}{
					"type":     "object",
					"required": []string{"id", "title", "type"},
					"properties": map[string]interface{}{
						"id": map[string]interface{}{
							"type":        "string",
							"description": "Unique step identifier within the scenario",
						},
						"title": map[string]interface{}{
							"type":        "string",
							"description": "Human readable step title",
						},
						"type": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"http", "delay", "assert", "condition"},
							"description": "Step action type: 'http', 'delay', 'assert', or 'condition'",
						},
						"next": map[string]interface{}{
							"type":        "string",
							"description": "ID of next step to jump to (enables non-linear graph branching)",
						},
						"onFailure": map[string]interface{}{
							"type":        "string",
							"description": "ID of fallback step to jump to if this step encounters an error",
						},
						"condition": map[string]interface{}{
							"type":        "object",
							"description": "Branching condition for type=condition",
							"properties": map[string]interface{}{
								"left":  map[string]interface{}{"type": "string", "description": "Left operand with {{var}}"},
								"op":    map[string]interface{}{"type": "string", "enum": []string{"notEmpty", "eq", "neq", "contains"}},
								"value": map[string]interface{}{"description": "Expected value for condition"},
								"then":  map[string]interface{}{"type": "string", "description": "Target step ID if condition is true"},
								"else":  map[string]interface{}{"type": "string", "description": "Target step ID if condition is false"},
							},
						},
						"role": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"client", "rest", "courier", "admin", "none"},
							"description": "Actor role for Bearer auth token: 'client', 'rest', 'courier', 'admin', or 'none'",
						},
						"method": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
							"description": "HTTP request method",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "API endpoint path with {{var}} interpolation, e.g. /api/clients/register",
						},
						"body": map[string]interface{}{
							"description": "JSON object or JSON string for request body. Inside string values, {{var}} will be interpolated",
						},
						"headers": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": map[string]interface{}{"type": "string"},
							"description":          "Custom HTTP headers, e.g. {'Idempotency-Key': '{{uuid}}'}",
						},
						"extract": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": map[string]interface{}{"type": "string"},
							"description":          "Map of variableName -> JSONPath to extract from response for downstream steps, e.g. {'orderId': '$.data.id'}",
						},
						"expectStatus": map[string]interface{}{
							"description": "Expected HTTP status code: integer (200) or class string ('2xx', '4xx', '!5xx')",
						},
						"asserts": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type":     "object",
								"required": []string{"op"},
								"properties": map[string]interface{}{
									"path":  map[string]interface{}{"type": "string", "description": "JSONPath in response body, e.g. $.status"},
									"op":    map[string]interface{}{"type": "string", "enum": []string{"eq", "neq", "contains", "exists"}},
									"value": map[string]interface{}{"description": "Expected value for comparison"},
								},
							},
						},
						"ms": map[string]interface{}{
							"type":        "integer",
							"description": "Delay duration in milliseconds (for type=delay)",
						},
						"left": map[string]interface{}{
							"type":        "string",
							"description": "Left operand with {{var}} for type=assert, e.g. '{{orderId}}'",
						},
						"check": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"op":    map[string]interface{}{"type": "string", "enum": []string{"notEmpty", "eq", "neq", "contains"}},
								"value": map[string]interface{}{"description": "Right operand value"},
							},
						},
					},
				},
			},
		},
	}
}
