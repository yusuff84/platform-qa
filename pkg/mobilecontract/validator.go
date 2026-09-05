package mobilecontract

import (
	"fmt"
	"strings"
)

// ValidateModelAgainstJSON checks if a mobile DTO can be safely decoded from a backend JSON object.
func ValidateModelAgainstJSON(model MobileModel, doc map[string]interface{}) ModelCompatibility {
	res := ModelCompatibility{
		Model:  model,
		Status: "COMPATIBLE",
		Issues: make([]ContractIssue, 0),
	}

	if doc == nil || len(doc) == 0 {
		res.Status = "INCOMPATIBLE_CRASH"
		res.Issues = append(res.Issues, ContractIssue{
			Severity:       SeverityCrash,
			ModelName:      model.Name,
			Platform:       model.Platform,
			Description:    "Ответ сервера пуст или не является JSON-объектом",
			Recommendation: "Проверьте тело ответа эндпоинта",
		})
		return res
	}

	// Unwrap envelopes like {"item": {...}} or {"data": {...}} or {"order": {...}}
	unwrapped := doc
	for _, envKey := range []string{"item", "data", strings.ToLower(model.Name)} {
		if sub, ok := doc[envKey].(map[string]interface{}); ok {
			unwrapped = sub
			break
		}
	}

	satisfiedFields := 0

	for _, field := range model.Fields {
		var matchedKey string
		var actualVal interface{}
		found := false

		// Try all candidate JSON keys
		for _, key := range field.JSONKeys {
			if val, ok := unwrapped[key]; ok {
				matchedKey = key
				actualVal = val
				found = true
				break
			}
			// Also check top-level if unwrapped
			if val, ok := doc[key]; ok {
				matchedKey = key
				actualVal = val
				found = true
				break
			}
		}

		if !found {
			if field.IsRequired && !field.IsNullable {
				res.Issues = append(res.Issues, ContractIssue{
					Severity:       SeverityCrash,
					ModelName:      model.Name,
					FieldName:      field.Name,
					Platform:       model.Platform,
					ExpectedType:   field.Type + " (non-nullable)",
					ActualType:     "MISSING",
					Description:    fmt.Sprintf("Обязательное поле %q отсутствует в ответе бэкенда! Приведёт к крашу декодирования на клиенте.", field.Name),
					Recommendation: fmt.Sprintf("Добавьте поле %s в ответ бэкенда или сделайте поле nullable (%s?) в коде мобилки.", field.JSONKeys[0], field.Type),
					SourceFile:     model.FilePath,
					LineNumber:     field.LineNumber,
				})
			} else {
				res.Issues = append(res.Issues, ContractIssue{
					Severity:       SeverityWarning,
					ModelName:      model.Name,
					FieldName:      field.Name,
					Platform:       model.Platform,
					ExpectedType:   field.Type + "? (optional)",
					ActualType:     "MISSING",
					Description:    fmt.Sprintf("Поле %q отсутствует в ответе сервера (клиент подставит null или дефолтное значение).", field.Name),
					Recommendation: fmt.Sprintf("Убедитесь, что эндпоинт отдаёт ключ %q, если эти данные нужны на экране.", field.JSONKeys[0]),
					SourceFile:     model.FilePath,
					LineNumber:     field.LineNumber,
				})
			}
			continue
		}

		// Field was found
		if actualVal == nil {
			if field.IsRequired && !field.IsNullable {
				res.Issues = append(res.Issues, ContractIssue{
					Severity:       SeverityCrash,
					ModelName:      model.Name,
					FieldName:      field.Name,
					Platform:       model.Platform,
					ExpectedType:   field.Type + " (non-nullable)",
					ActualType:     "null",
					TestedKey:      matchedKey,
					Description:    fmt.Sprintf("Бэкенд вернул null для обязательного поля %q (%s)! Это вызовет TypeError / DecodingError.", field.Name, matchedKey),
					Recommendation: fmt.Sprintf("Бэкенд обязан возвращать не-null значение, либо в модели мобилки нужно объявить тип как nullable (%s?).", field.Type),
					SourceFile:     model.FilePath,
					LineNumber:     field.LineNumber,
				})
			} else {
				satisfiedFields++
			}
			continue
		}

		// Check type compatibility
		typeErr := checkTypeCompatibility(field.Type, actualVal)
		if typeErr != nil {
			sev := SeverityCrash
			// Soft conversions (e.g. numeric strings when helper converts them)
			if isSoftCoercible(field.Type, actualVal) {
				sev = SeverityWarning
			}

			res.Issues = append(res.Issues, ContractIssue{
				Severity:       sev,
				ModelName:      model.Name,
				FieldName:      field.Name,
				Platform:       model.Platform,
				ExpectedType:   field.Type,
				ActualType:     describeActualType(actualVal),
				TestedKey:      matchedKey,
				Description:    fmt.Sprintf("Несовпадение типа поля %q (%s): %v", field.Name, matchedKey, typeErr),
				Recommendation: fmt.Sprintf("Приведите тип поля к %s на стороне бэкенда.", field.Type),
				SourceFile:     model.FilePath,
				LineNumber:     field.LineNumber,
			})
			if sev == SeverityWarning {
				satisfiedFields++
			}
			continue
		}

		// Check naming consistency (if fallback alias was used)
		if len(field.JSONKeys) > 0 && matchedKey != field.JSONKeys[0] {
			res.Issues = append(res.Issues, ContractIssue{
				Severity:       SeverityInfo,
				ModelName:      model.Name,
				FieldName:      field.Name,
				Platform:       model.Platform,
				TestedKey:      matchedKey,
				Description:    fmt.Sprintf("Значение прочитано через запасной алиас %q вместо основного %q.", matchedKey, field.JSONKeys[0]),
				Recommendation: fmt.Sprintf("Унифицируйте именование: используйте единый ключ %q на бэке и в мобилке.", field.JSONKeys[0]),
				SourceFile:     model.FilePath,
				LineNumber:     field.LineNumber,
			})
		}

		satisfiedFields++
	}

	if len(model.Fields) > 0 {
		res.MatchRatio = float64(satisfiedFields) / float64(len(model.Fields)) * 100.0
	}

	// Calculate overall status
	for _, iss := range res.Issues {
		if iss.Severity == SeverityCrash {
			res.Status = "INCOMPATIBLE_CRASH"
			break
		}
		if iss.Severity == SeverityWarning && res.Status != "INCOMPATIBLE_CRASH" {
			res.Status = "WARNINGS"
		}
	}

	return res
}

func checkTypeCompatibility(expected string, actual interface{}) error {
	switch expected {
	case "string":
		if _, ok := actual.(string); !ok {
			return fmt.Errorf("ожидалась строка, получен %T", actual)
		}
	case "int":
		switch v := actual.(type) {
		case float64:
			if v != float64(int64(v)) {
				return fmt.Errorf("ожидалось целое число, получено дробное %v", v)
			}
		case int, int64:
			// ok
		default:
			return fmt.Errorf("ожидалось число int, получен %T", actual)
		}
	case "double":
		switch actual.(type) {
		case float64, int, int64:
			// ok
		default:
			return fmt.Errorf("ожидалось число double, получен %T", actual)
		}
	case "bool":
		if _, ok := actual.(bool); !ok {
			return fmt.Errorf("ожидался boolean, получен %T", actual)
		}
	case "array":
		if _, ok := actual.([]interface{}); !ok {
			return fmt.Errorf("ожидался массив [], получен %T", actual)
		}
	case "object":
		if _, ok := actual.(map[string]interface{}); !ok {
			return fmt.Errorf("ожидался объект {}, получен %T", actual)
		}
	}
	return nil
}

func isSoftCoercible(expected string, actual interface{}) bool {
	// e.g. "123" -> int/double, or 1/0 -> bool
	if str, ok := actual.(string); ok {
		if expected == "int" || expected == "double" {
			return len(strings.TrimSpace(str)) > 0
		}
	}
	if num, ok := actual.(float64); ok {
		if expected == "bool" && (num == 0 || num == 1) {
			return true
		}
	}
	return false
}

func describeActualType(v interface{}) string {
	if v == nil {
		return "null"
	}
	switch t := v.(type) {
	case string:
		return "string"
	case float64:
		if t == float64(int64(t)) {
			return "int"
		}
		return "double"
	case bool:
		return "bool"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}
