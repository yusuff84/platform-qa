package mobilecontract

import (
	"bufio"
	"regexp"
	"strings"
)

var (
	swiftStructRegex = regexp.MustCompile(`(?:struct|class)\s+([A-Za-z0-9_]+)\s*(?::\s*([^{]+))?`)
	swiftPropRegex   = regexp.MustCompile(`^\s*(?:let|var)\s+([A-Za-z0-9_]+)\s*:\s*([A-Za-z0-9_?<>\[\]:]+)`)
	swiftCodingKeyRe = regexp.MustCompile(`case\s+([A-Za-z0-9_]+)\s*=\s*['"]([^'"]+)['"]`)
)

// ParseSwiftSource extracts mobile DTO models from Swift source code.
func ParseSwiftSource(content, filePath string) []MobileModel {
	var models []MobileModel

	scanner := bufio.NewScanner(strings.NewReader(content))
	var currentModel *MobileModel
	codingKeysMap := make(map[string]string)
	inCodingKeys := false

	lineNum := 0

	flushCurrent := func() {
		if currentModel != nil {
			for i := range currentModel.Fields {
				f := &currentModel.Fields[i]
				if key, ok := codingKeysMap[f.Name]; ok {
					f.JSONKeys = []string{key, f.Name}
				} else if len(f.JSONKeys) == 0 {
					f.JSONKeys = []string{f.Name, toSnakeCase(f.Name)}
				}
			}
			if len(currentModel.Fields) > 0 {
				models = append(models, *currentModel)
			}
		}
		currentModel = nil
		codingKeysMap = make(map[string]string)
		inCodingKeys = false
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Check struct/class declaration
		if match := swiftStructRegex.FindStringSubmatch(trimmed); len(match) > 1 {
			structName := match[1]
			inheritance := ""
			if len(match) > 2 {
				inheritance = match[2]
			}

			// Accept structs that conform to Codable/Decodable or typical DTOs
			isDTO := strings.Contains(inheritance, "Decodable") ||
				strings.Contains(inheritance, "Codable") ||
				strings.HasSuffix(structName, "DTO") ||
				strings.HasSuffix(structName, "Response") ||
				strings.HasSuffix(structName, "Model") ||
				strings.Contains(inheritance, "Identifiable")

			if isDTO || (!strings.HasSuffix(structName, "View") && !strings.HasSuffix(structName, "ViewModel")) {
				flushCurrent()
				currentModel = &MobileModel{
					Name:               structName,
					Platform:           PlatformIOS,
					FilePath:           filePath,
					Fields:             make([]ModelField, 0),
					CandidateEndpoints: inferEndpointsFromName(structName),
				}
				continue
			}
		}

		if currentModel == nil {
			continue
		}

		// Check if entering CodingKeys enum
		if strings.Contains(trimmed, "enum CodingKeys") {
			inCodingKeys = true
			continue
		}
		if inCodingKeys {
			if strings.Contains(trimmed, "}") {
				inCodingKeys = false
				continue
			}
			if ckMatch := swiftCodingKeyRe.FindStringSubmatch(trimmed); len(ckMatch) > 2 {
				codingKeysMap[ckMatch[1]] = ckMatch[2]
			}
			continue
		}

		// Check property declaration: let id: String or var phone: String?
		if propMatch := swiftPropRegex.FindStringSubmatch(trimmed); len(propMatch) > 2 {
			propName := propMatch[1]
			rawType := propMatch[2]

			isNullable := strings.HasSuffix(rawType, "?")
			cleanType := strings.TrimSuffix(rawType, "?")
			normType := normalizeSwiftType(cleanType)

			field := ModelField{
				Name:       propName,
				Type:       normType,
				IsRequired: !isNullable,
				IsNullable: isNullable,
				LineNumber: lineNum,
			}
			currentModel.Fields = append(currentModel.Fields, field)
		}
	}

	flushCurrent()
	return models
}

func normalizeSwiftType(t string) string {
	lower := strings.ToLower(t)
	switch {
	case lower == "string":
		return "string"
	case lower == "int" || lower == "int64" || lower == "int32":
		return "int"
	case lower == "double" || lower == "float" || lower == "cgfloat":
		return "double"
	case lower == "bool":
		return "bool"
	case strings.HasPrefix(lower, "[") && strings.HasSuffix(lower, "]"):
		return "array"
	case lower == "date":
		return "date"
	default:
		return "object"
	}
}
