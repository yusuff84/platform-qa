package mobilecontract

import (
	"bufio"
	"regexp"
	"strings"
)

var (
	kotlinClassRegex    = regexp.MustCompile(`(?:data\s+class|class)\s+([A-Za-z0-9_]+)`)
	kotlinParamRegex    = regexp.MustCompile(`(?:val|var)\s+([A-Za-z0-9_]+)\s*:\s*([A-Za-z0-9_?<>]+)`)
	kotlinAnnotationRe  = regexp.MustCompile(`@(?:SerializedName|SerialName|Json\s*\(\s*name\s*=)\s*\(?['"]([^'"]+)['"]\)?`)
)

// ParseKotlinSource extracts mobile DTO models from Kotlin source code.
func ParseKotlinSource(content, filePath string) []MobileModel {
	var models []MobileModel

	scanner := bufio.NewScanner(strings.NewReader(content))
	var currentModel *MobileModel
	pendingKey := ""
	lineNum := 0

	flushCurrent := func() {
		if currentModel != nil && len(currentModel.Fields) > 0 {
			models = append(models, *currentModel)
		}
		currentModel = nil
		pendingKey = ""
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Check class declaration
		if match := kotlinClassRegex.FindStringSubmatch(trimmed); len(match) > 1 {
			className := match[1]
			if !strings.HasSuffix(className, "Activity") &&
				!strings.HasSuffix(className, "Fragment") &&
				!strings.HasSuffix(className, "ViewModel") &&
				!strings.HasSuffix(className, "Adapter") {
				flushCurrent()
				currentModel = &MobileModel{
					Name:               className,
					Platform:           PlatformAndroid,
					FilePath:           filePath,
					Fields:             make([]ModelField, 0),
					CandidateEndpoints: inferEndpointsFromName(className),
				}
			}
		}

		if currentModel == nil {
			continue
		}

		// Look for annotations on previous or same line
		if annMatch := kotlinAnnotationRe.FindStringSubmatch(trimmed); len(annMatch) > 1 {
			pendingKey = annMatch[1]
		}

		// Look for property in constructor or body: val id: String, or val phone: String? = null
		if paramMatch := kotlinParamRegex.FindStringSubmatch(trimmed); len(paramMatch) > 2 {
			paramName := paramMatch[1]
			rawType := paramMatch[2]

			isNullable := strings.HasSuffix(rawType, "?")
			hasDefault := strings.Contains(trimmed, "=")
			isRequired := !isNullable && !hasDefault

			cleanType := strings.TrimSuffix(rawType, "?")
			normType := normalizeKotlinType(cleanType)

			keys := []string{paramName, toSnakeCase(paramName)}
			if pendingKey != "" {
				keys = []string{pendingKey, paramName}
				pendingKey = ""
			}

			field := ModelField{
				Name:       paramName,
				Type:       normType,
				IsRequired: isRequired,
				IsNullable: isNullable,
				JSONKeys:   keys,
				LineNumber: lineNum,
			}
			currentModel.Fields = append(currentModel.Fields, field)
		}

		// If closing constructor/class
		if strings.HasPrefix(trimmed, ")") && currentModel != nil && len(currentModel.Fields) > 0 {
			flushCurrent()
		}
	}

	flushCurrent()
	return models
}

func normalizeKotlinType(t string) string {
	lower := strings.ToLower(t)
	switch {
	case lower == "string":
		return "string"
	case lower == "int" || lower == "long":
		return "int"
	case lower == "double" || lower == "float":
		return "double"
	case lower == "boolean":
		return "bool"
	case strings.HasPrefix(lower, "list") || strings.HasPrefix(lower, "arraylist"):
		return "array"
	case strings.HasPrefix(lower, "map"):
		return "object"
	default:
		return "object"
	}
}
