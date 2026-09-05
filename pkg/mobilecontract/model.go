package mobilecontract

import "time"

// Platform indicates mobile client framework/language.
type Platform string

const (
	PlatformFlutter Platform = "flutter" // Dart
	PlatformIOS     Platform = "ios"     // Swift (Codable/Decodable)
	PlatformAndroid Platform = "android" // Kotlin (data class, Moshi, Kotlinx, Gson)
)

// IssueSeverity indicates the risk level of contract discrepancy.
type IssueSeverity string

const (
	SeverityCrash   IssueSeverity = "CRITICAL_CRASH" // Will cause TypeError / DecodingError / NPE at runtime
	SeverityWarning IssueSeverity = "WARNING"        // Optional field missing, fallback key used, etc.
	SeverityInfo    IssueSeverity = "INFO"           // Extra backend field not used by client
)

// ModelField describes a single field in a mobile DTO.
type ModelField struct {
	Name       string   `json:"name"`                 // Variable name in code (e.g., courierId)
	Type       string   `json:"type"`                 // string, int, double, bool, array, object, date
	IsRequired bool     `json:"isRequired"`           // true if constructor requires it
	IsNullable bool     `json:"isNullable"`           // true if Type? (can accept null)
	JSONKeys   []string `json:"jsonKeys"`             // Expected JSON keys in order of precedence
	LineNumber int      `json:"lineNumber,omitempty"` // Line in source file where field is declared
}

// MobileModel describes a mobile Data Transfer Object.
type MobileModel struct {
	Name               string       `json:"name"`               // e.g. Courier, Order, OrderDish
	Platform           Platform     `json:"platform"`           // flutter | ios | android
	FilePath           string       `json:"filePath"`           // Relative or absolute path to source file
	Fields             []ModelField `json:"fields"`             // List of parsed fields
	CandidateEndpoints []string     `json:"candidateEndpoints"` // Inferred matching API routes
}

// ContractIssue describes a detected contract divergence.
type ContractIssue struct {
	Severity       IssueSeverity `json:"severity"`       // CRITICAL_CRASH | WARNING | INFO
	ModelName      string        `json:"modelName"`      // e.g. Courier
	FieldName      string        `json:"fieldName"`      // e.g. id
	Platform       Platform      `json:"platform"`       // flutter | ios | android
	ExpectedType   string        `json:"expectedType"`   // e.g. string (non-nullable)
	ActualType     string        `json:"actualType"`     // e.g. null, int, missing
	TestedKey      string        `json:"testedKey"`      // e.g. courier_id
	Description    string        `json:"description"`    // Human explanation of the error
	Recommendation string        `json:"recommendation"` // Concrete fix advice
	SourceFile     string        `json:"sourceFile"`     // Path to mobile code file
	LineNumber     int           `json:"lineNumber"`     // Line number in code
}

// ModelCompatibility details the check result for one mobile model.
type ModelCompatibility struct {
	AppName    string          `json:"appName,omitempty"`
	Model      MobileModel     `json:"model"`
	Status     string          `json:"status"` // COMPATIBLE | WARNINGS | INCOMPATIBLE_CRASH
	Endpoint   string          `json:"endpoint,omitempty"`
	Issues     []ContractIssue `json:"issues"`
	MatchRatio float64         `json:"matchRatio"` // % of fields correctly satisfied
}

// AppSummary aggregates compatibility statistics for one application.
type AppSummary struct {
	AppID      string  `json:"appId"`
	AppName    string  `json:"appName"`
	Platform   string  `json:"platform"`
	Total      int     `json:"total"`
	Compatible int     `json:"compatible"`
	Crashes    int     `json:"crashes"`
	Warnings   int     `json:"warnings"`
	PassRatio  float64 `json:"passRatio"`
}

// CompatibilityReport is the aggregate report across all scanned mobile DTOs.
type CompatibilityReport struct {
	Timestamp      time.Time            `json:"timestamp"`
	ScannedFiles   int                  `json:"scannedFiles"`
	TotalModels    int                  `json:"totalModels"`
	ByPlatform     map[Platform]int     `json:"byPlatform"`
	ByApp          map[string]AppSummary `json:"byApp,omitempty"`
	Compatible     int                  `json:"compatible"`
	Warnings       int                  `json:"warnings"`
	Crashes        int                  `json:"crashes"`
	PassPercentage float64              `json:"passPercentage"`
	Results        []ModelCompatibility `json:"results"`
}

// MobileApp represents a configured mobile application target.
type MobileApp struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Platform    Platform             `json:"platform"` // flutter | ios | android
	SourceType  string               `json:"sourceType"` // gitlab | local
	RepoURL     string               `json:"repoUrl,omitempty"`
	Token       string               `json:"token,omitempty"`
	Branch      string               `json:"branch,omitempty"`
	LocalPath   string               `json:"localPath,omitempty"`
	LastScanned *time.Time           `json:"lastScanned,omitempty"`
	LastReport  *CompatibilityReport `json:"lastReport,omitempty"`
}
