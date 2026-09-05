package mobilecontract

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"locali-e2e-engine/pkg/spec"
)

// ScanDirectory walks rootPath and parses all mobile DTO models from Dart, Swift, and Kotlin files.
func ScanDirectory(rootPath string) ([]MobileModel, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("путь к репозиторию мобилок не указан")
	}

	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("директория %q не найдена: %w", rootPath, err)
	}
	if !info.IsDir() {
		// Single file scan
		return scanSingleFile(rootPath)
	}

	models := make([]MobileModel, 0)

	err = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // ignore inaccessible subfolders
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".dart_tool" || name == "build" || name == "Pods" || name == ".gradle" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".dart" && ext != ".swift" && ext != ".kt" {
			return nil
		}

		fileModels, ferr := scanSingleFile(path)
		if ferr == nil && len(fileModels) > 0 {
			models = append(models, fileModels...)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("ошибка сканирования репозитория: %w", err)
	}

	return models, nil
}

func scanSingleFile(filePath string) ([]MobileModel, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".dart":
		return ParseDartSource(content, filePath), nil
	case ".swift":
		return ParseSwiftSource(content, filePath), nil
	case ".kt":
		return ParseKotlinSource(content, filePath), nil
	default:
		return nil, nil
	}
}

// AnalyzeCompatibility evaluates all scanned mobile models against the OpenAPI spec.
func AnalyzeCompatibility(models []MobileModel, meta *spec.Meta, liveResponses map[string]map[string]interface{}, appName ...string) *CompatibilityReport {
	appTitle := ""
	if len(appName) > 0 {
		appTitle = appName[0]
	}

	report := &CompatibilityReport{
		Timestamp:   time.Now().UTC(),
		TotalModels: len(models),
		ByPlatform:  make(map[Platform]int),
		ByApp:       make(map[string]AppSummary),
		Results:     make([]ModelCompatibility, 0, len(models)),
	}

	for _, m := range models {
		report.ByPlatform[m.Platform]++

		// Find best matching backend JSON payload or OpenAPI sample
		doc := findMatchingPayload(m, meta, liveResponses)

		compat := ValidateModelAgainstJSON(m, doc)
		if appTitle != "" {
			compat.AppName = appTitle
		}
		switch compat.Status {
		case "COMPATIBLE":
			report.Compatible++
		case "WARNINGS":
			report.Warnings++
		case "INCOMPATIBLE_CRASH":
			report.Crashes++
		}
		report.Results = append(report.Results, compat)
	}

	if report.TotalModels > 0 {
		report.PassPercentage = float64(report.Compatible) / float64(report.TotalModels) * 100.0
	}

	if appTitle != "" {
		report.ByApp[appTitle] = AppSummary{
			AppName:    appTitle,
			Total:      report.TotalModels,
			Compatible: report.Compatible,
			Crashes:    report.Crashes,
			Warnings:   report.Warnings,
			PassRatio:  report.PassPercentage,
		}
	}

	return report
}

func findMatchingPayload(m MobileModel, meta *spec.Meta, liveResponses map[string]map[string]interface{}) map[string]interface{} {
	// 1. Check live captured responses first
	for _, ep := range m.CandidateEndpoints {
		for liveRoute, resp := range liveResponses {
			if strings.EqualFold(liveRoute, ep) || strings.Contains(strings.ToLower(liveRoute), strings.ToLower(m.Name)) {
				return resp
			}
		}
	}

	// 2. Fall back to OpenAPI example bodies or generated samples
	if meta != nil {
		for _, sEp := range meta.Endpoints {
			for _, cEp := range m.CandidateEndpoints {
				if strings.Contains(strings.ToLower(sEp.Path), strings.ToLower(cEp)) ||
					strings.Contains(strings.ToLower(sEp.Path), strings.ToLower(m.Name)) {
					if len(sEp.ExampleBody) > 0 {
						var parsed map[string]interface{}
						if err := json.Unmarshal(sEp.ExampleBody, &parsed); err == nil {
							return parsed
						}
					}
				}
			}
		}
	}

	// 3. Synthesize minimal dummy payload if none found so far
	dummy := make(map[string]interface{})
	for _, f := range m.Fields {
		key := f.Name
		if len(f.JSONKeys) > 0 {
			key = f.JSONKeys[0]
		}
		switch f.Type {
		case "string":
			dummy[key] = "test_val"
		case "int":
			dummy[key] = float64(1)
		case "double":
			dummy[key] = 1.0
		case "bool":
			dummy[key] = true
		case "array":
			dummy[key] = []interface{}{}
		default:
			dummy[key] = map[string]interface{}{}
		}
	}
	return dummy
}
