package mobilecontract

import (
	"bufio"
	"regexp"
	"strings"
)

var (
	dartClassRegex       = regexp.MustCompile(`(?:class|enum)\s+([A-Za-z0-9_]+)`)
	dartFieldRegex       = regexp.MustCompile(`^\s*(?:final|var|late\s+final)\s+([A-Za-z0-9_<>?]+)\s+([A-Za-z0-9_]+)\s*;`)
	dartRequiredRegex    = regexp.MustCompile(`required\s+this\.([A-Za-z0-9_]+)`)
	dartJsonKeyRegex     = regexp.MustCompile(`@JsonKey\s*\(\s*name\s*:\s*['"]([^'"]+)['"]`)
	dartReadHelperRegex  = regexp.MustCompile(`read(?:String|Int|Double|Bool|Address)\s*\(\s*[A-Za-z0-9_.]+\s*,\s*\[([^\]]+)\]`)
	dartDirectIndexRegex = regexp.MustCompile(`(?:json|data)\s*\[\s*['"]([^'"]+)['"]\s*\]`)
)

// ParseDartSource extracts mobile DTO models from Dart source code.
func ParseDartSource(content, filePath string) []MobileModel {
	var models []MobileModel

	scanner := bufio.NewScanner(strings.NewReader(content))
	var currentModel *MobileModel
	fieldMap := make(map[string]*ModelField)
	requiredMap := make(map[string]bool)
	jsonKeysMap := make(map[string][]string)

	lineNum := 0
	inClass := false

	flushCurrent := func() {
		if currentModel != nil {
			// Apply required and json keys
			for i := range currentModel.Fields {
				f := &currentModel.Fields[i]
				if requiredMap[f.Name] {
					f.IsRequired = true
				}
				if keys, ok := jsonKeysMap[f.Name]; ok && len(keys) > 0 {
					f.JSONKeys = keys
				} else if len(f.JSONKeys) == 0 {
					// Default to camelCase name and snake_case variant
					f.JSONKeys = []string{f.Name, toSnakeCase(f.Name)}
				}
			}
			if len(currentModel.Fields) > 0 {
				models = append(models, *currentModel)
			}
		}
		currentModel = nil
		fieldMap = make(map[string]*ModelField)
		requiredMap = make(map[string]bool)
		jsonKeysMap = make(map[string][]string)
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Check class declaration
		if classMatch := dartClassRegex.FindStringSubmatch(trimmed); len(classMatch) > 1 {
			// Ignore abstract classes like OrderStatuses, CourierStatuses unless they have fields
			className := classMatch[1]
			if !strings.HasSuffix(className, "State") && !strings.HasSuffix(className, "Controller") {
				flushCurrent()
				inClass = true
				currentModel = &MobileModel{
					Name:               className,
					Platform:           PlatformFlutter,
					FilePath:           filePath,
					Fields:             make([]ModelField, 0),
					CandidateEndpoints: inferEndpointsFromName(className),
				}
				continue
			}
		}

		if !inClass || currentModel == nil {
			continue
		}

		// Check required constructor parameter
		if reqMatches := dartRequiredRegex.FindAllStringSubmatch(line, -1); len(reqMatches) > 0 {
			for _, rm := range reqMatches {
				requiredMap[rm[1]] = true
			}
		}

		// Check field declaration: final String? phone; or final String id;
		if fieldMatch := dartFieldRegex.FindStringSubmatch(line); len(fieldMatch) > 2 {
			rawType := fieldMatch[1]
			fieldName := fieldMatch[2]

			isNullable := strings.HasSuffix(rawType, "?")
			cleanType := strings.TrimSuffix(rawType, "?")
			normalizedType := normalizeDartType(cleanType)

			field := ModelField{
				Name:       fieldName,
				Type:       normalizedType,
				IsNullable: isNullable,
				LineNumber: lineNum,
			}
			currentModel.Fields = append(currentModel.Fields, field)
			fieldMap[fieldName] = &currentModel.Fields[len(currentModel.Fields)-1]
			continue
		}

		// Check read helper in fromJson: readString(data, ['courier_id', 'courierId', 'id'])
		if readMatches := dartReadHelperRegex.FindAllStringSubmatch(line, -1); len(readMatches) > 0 {
			for _, m := range readMatches {
				rawKeys := m[1]
				keys := extractQuotedStrings(rawKeys)
				// Look for field assignment: targetField = ... or fieldName: ...
				if fieldTarget := extractAssignedField(line); fieldTarget != "" {
					jsonKeysMap[fieldTarget] = append(jsonKeysMap[fieldTarget], keys...)
				}
			}
		}

		// Check direct json['key'] indexing
		if idxMatches := dartDirectIndexRegex.FindAllStringSubmatch(line, -1); len(idxMatches) > 0 {
			for _, m := range idxMatches {
				key := m[1]
				if fieldTarget := extractAssignedField(line); fieldTarget != "" {
					jsonKeysMap[fieldTarget] = append(jsonKeysMap[fieldTarget], key)
				}
			}
		}

		// Check @JsonKey(name: "...")
		if jkMatch := dartJsonKeyRegex.FindStringSubmatch(line); len(jkMatch) > 1 {
			// Next field will have this key
			key := jkMatch[1]
			if scanner.Scan() {
				lineNum++
				nextLine := scanner.Text()
				if nextFieldMatch := dartFieldRegex.FindStringSubmatch(nextLine); len(nextFieldMatch) > 2 {
					fieldName := nextFieldMatch[2]
					jsonKeysMap[fieldName] = append(jsonKeysMap[fieldName], key)
				}
			}
		}
	}

	flushCurrent()
	return models
}

func normalizeDartType(t string) string {
	lower := strings.ToLower(t)
	switch {
	case lower == "string":
		return "string"
	case lower == "int":
		return "int"
	case lower == "double" || lower == "num":
		return "double"
	case lower == "bool":
		return "bool"
	case strings.HasPrefix(lower, "list") || strings.HasPrefix(lower, "set"):
		return "array"
	case strings.HasPrefix(lower, "map") || lower == "dynamic":
		return "object"
	case lower == "datetime":
		return "date"
	default:
		return "object"
	}
}

func extractQuotedStrings(raw string) []string {
	re := regexp.MustCompile(`['"]([^'"]+)['"]`)
	matches := re.FindAllStringSubmatch(raw, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}

func extractAssignedField(line string) string {
	parts := strings.Split(line, ":")
	if len(parts) >= 2 {
		candidate := strings.TrimSpace(parts[0])
		// Check valid identifier
		if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(candidate) {
			return candidate
		}
	}
	parts = strings.Split(line, "=")
	if len(parts) >= 2 {
		candidate := strings.TrimSpace(parts[0])
		subParts := strings.Fields(candidate)
		if len(subParts) > 0 {
			last := subParts[len(subParts)-1]
			if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(last) {
				return last
			}
		}
	}
	return ""
}

func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('_')
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

func inferEndpointsFromName(name string) []string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "courier"):
		return []string{"/api/couriers", "/api/admin/couriers"}
	case strings.Contains(lower, "order"):
		return []string{"/api/clients/orders", "/api/clients/create-order", "/api/orders"}
	case strings.Contains(lower, "rest") || strings.Contains(lower, "dish"):
		return []string{"/api/rests", "/api/rests/dishes"}
	case strings.Contains(lower, "tariff"):
		return []string{"/api/admin/delivery/locali-tariff", "/api/rests/delivery-tariffs"}
	case strings.Contains(lower, "user") || strings.Contains(lower, "client"):
		return []string{"/api/clients", "/api/admin/users"}
	case strings.Contains(lower, "cart"):
		return []string{"/api/cart"}
	default:
		return []string{"/api/" + toSnakeCase(name)}
	}
}
