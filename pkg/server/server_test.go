package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"locali-e2e-engine/config"
	"locali-e2e-engine/pkg/spec"
)

func setupTestServer(t *testing.T) (*Server, func()) {
	tmpDir, err := os.MkdirTemp("", "server_test_*")
	require.NoError(t, err)

	cfg := &config.Config{
		BaseURL: "http://localhost:3000",
		DataDir: tmpDir,
	}

	webDir := filepath.Join(tmpDir, "web")
	_ = os.MkdirAll(webDir, 0o755)

	srv, err := NewServer(cfg, webDir)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}
	return srv, cleanup
}

func TestServer_AnalysisAndReplenish(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Initially coverage is empty
	{
		req := httptest.NewRequest(http.MethodGet, "/api/analysis/coverage", nil)
		w := httptest.NewRecorder()
		srv.handleAnalysisCoverage(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var cov map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &cov)
		require.NoError(t, err)
		assert.Equal(t, float64(0), cov["totalEndpoints"])
	}

	// 2. Import a test spec
	meta := &spec.Meta{
		Key:   "demo_api",
		Title: "Demo API",
		Endpoints: []spec.EndpointInfo{
			{
				Method:      "POST",
				Path:        "/rests/categories",
				Summary:     "Create category",
				Tag:         "categories",
				HasBody:     true,
				ExampleBody: json.RawMessage(`{"name":"Desserts"}`),
			},
			{
				Method:  "GET",
				Path:    "/rests/categories",
				Summary: "List categories",
				Tag:     "categories",
			},
			{
				Method:  "GET",
				Path:    "/uncovered/something",
				Summary: "Uncovered route",
				Tag:     "misc",
			},
		},
	}
	err := srv.specs.Save(meta, []byte(`{"swagger":"2.0"}`))
	require.NoError(t, err)

	// 3. Check coverage after spec imported
	{
		req := httptest.NewRequest(http.MethodGet, "/api/analysis/coverage", nil)
		w := httptest.NewRecorder()
		srv.handleAnalysisCoverage(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var cov map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &cov)
		require.NoError(t, err)
		assert.Equal(t, float64(3), cov["totalEndpoints"])
		// /rests/categories covered by builtin api_menu suite!
		assert.Equal(t, float64(2), cov["coveredEndpoints"])
		assert.InDelta(t, 66.66, cov["coveragePercent"], 0.1)
	}

	// 4. Test Scenario Auto-Replenishment (Preview)
	{
		body := []byte(`{"strategies":["crud","negative"],"preview":true}`)
		req := httptest.NewRequest(http.MethodPost, "/api/scenarios/replenish", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleScenariosReplenish(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var res map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &res)
		require.NoError(t, err)
		assert.Greater(t, res["count"].(float64), float64(0))
	}

	// 5. Test Scenario Auto-Replenishment (Persist)
	{
		body := []byte(`{"strategies":["crud","smoke"],"preview":false,"overwrite":true}`)
		req := httptest.NewRequest(http.MethodPost, "/api/scenarios/replenish", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleScenariosReplenish(w, req)
		assert.Equal(t, http.StatusCreated, w.Code)

		var res map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &res)
		require.NoError(t, err)
		assert.Equal(t, "ok", res["status"])
		assert.Greater(t, res["savedCount"].(float64), float64(0))
	}

	// 6. Test Quality Metrics
	{
		req := httptest.NewRequest(http.MethodGet, "/api/analysis/metrics", nil)
		w := httptest.NewRecorder()
		srv.handleAnalysisMetrics(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var met map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &met)
		require.NoError(t, err)
		assert.NotNil(t, met["statusBreakdown"])
	}

	// 7. Test Diagnostics (no run yet -> healthy default)
	{
		req := httptest.NewRequest(http.MethodGet, "/api/analysis/diagnostics", nil)
		w := httptest.NewRecorder()
		srv.handleAnalysisDiagnostics(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var diag map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &diag)
		require.NoError(t, err)
		assert.Equal(t, "HEALTHY", diag["category"])
	}

	// 8. Test Stand Swagger Sync & Auto-generation
	{
		// Create a mock stand with a local spec file path
		specFilePath := filepath.Join(srv.cfg.DataDir, "test_swagger.json")
		err := os.WriteFile(specFilePath, []byte(`{
			"swagger": "2.0",
			"info": {"title": "Test Sync Swagger", "version": "1.0"},
			"paths": {
				"/users": {
					"get": {"summary": "Get users", "tags": ["users"], "responses": {"200": {"description": "OK"}}},
					"post": {"summary": "Create user", "tags": ["users"], "parameters": [{"name":"body","in":"body","schema":{"type":"object"}}], "responses": {"201": {"description": "Created"}}}
				}
			}
		}`), 0o644)
		require.NoError(t, err)

		stand, err := srv.stands.Add("Test Stand With Swagger", "http://teststand.local", "1234", specFilePath)
		require.NoError(t, err)
		assert.Equal(t, specFilePath, stand.SwaggerURL)

		req := httptest.NewRequest(http.MethodPost, "/api/stands/"+stand.ID+"/sync-swagger", nil)
		w := httptest.NewRecorder()
		srv.handleStandByID(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var syncRes map[string]interface{}
		err = json.Unmarshal(w.Body.Bytes(), &syncRes)
		require.NoError(t, err)
		assert.Equal(t, "ok", syncRes["status"])
		assert.Greater(t, syncRes["savedCount"].(float64), float64(0))
	}

	// 9. Test Mobile Contract Scan API
	{
		body := []byte(`{"path":"."}`)
		req := httptest.NewRequest(http.MethodPost, "/api/mobilecontract/scan", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleMobileContractScan(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var scanRes map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &scanRes)
		require.NoError(t, err)
		assert.NotNil(t, scanRes["byPlatform"])
	}
}
