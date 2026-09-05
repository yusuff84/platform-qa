package spec_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"locali-e2e-engine/pkg/spec"
)

func sampleTestMeta() *spec.Meta {
	return &spec.Meta{
		Key:   "sample_api",
		Title: "Sample Store API",
		Endpoints: []spec.EndpointInfo{
			{
				Method:      "POST",
				Path:        "/rests/categories",
				Summary:     "Create category",
				Tag:         "categories",
				HasBody:     true,
				ExampleBody: json.RawMessage(`{"name":"Pizza"}`),
			},
			{
				Method:  "GET",
				Path:    "/rests/categories/{id}",
				Summary: "Get category by ID",
				Tag:     "categories",
			},
			{
				Method:      "PUT",
				Path:        "/rests/categories/{id}",
				Summary:     "Update category",
				Tag:         "categories",
				HasBody:     true,
				ExampleBody: json.RawMessage(`{"name":"Super Pizza"}`),
			},
			{
				Method:  "DELETE",
				Path:    "/rests/categories/{id}",
				Summary: "Delete category",
				Tag:     "categories",
			},
			{
				Method:  "GET",
				Path:    "/admin/audit-logs",
				Summary: "Admin audit logs",
				Tag:     "admin",
			},
			{
				Method:      "POST",
				Path:        "/couriers/location",
				Summary:     "Update courier position",
				Tag:         "courier",
				HasBody:     true,
				ExampleBody: json.RawMessage(`{"lat":43.1,"lng":45.2}`),
			},
		},
	}
}

func TestReplenishScenarios_AllStrategies(t *testing.T) {
	meta := sampleTestMeta()
	opts := spec.ReplenishOptions{
		Strategies: []string{"smoke", "crud", "rbac", "negative"},
	}

	res := spec.ReplenishScenarios(meta, opts)
	require.NotNil(t, res)
	assert.NotEmpty(t, res.Scenarios)

	// Check breakdown
	assert.Greater(t, res.ByStrategy["smoke"], 0, "should produce smoke scenarios")
	assert.Greater(t, res.ByStrategy["crud"], 0, "should produce CRUD scenarios for /rests/categories")
	assert.Greater(t, res.ByStrategy["rbac"], 0, "should produce RBAC scenarios")
	assert.Greater(t, res.ByStrategy["negative"], 0, "should produce negative validation scenarios")

	// Ensure EVERY generated scenario is valid according to schema rules!
	for _, sc := range res.Scenarios {
		err := sc.Validate()
		assert.NoError(t, err, "Scenario %s should be valid", sc.Key)
	}
}

func TestReplenishScenarios_CRUDChaining(t *testing.T) {
	meta := sampleTestMeta()
	opts := spec.ReplenishOptions{
		Strategies: []string{"crud"},
	}

	res := spec.ReplenishScenarios(meta, opts)
	require.NotNil(t, res)
	require.Len(t, res.Scenarios, 1)

	crudScen := res.Scenarios[0]
	assert.Contains(t, crudScen.Key, "spec_crud_")
	assert.Equal(t, "custom", crudScen.Category)

	// Verify steps sequence: POST -> GET -> PUT -> DELETE -> GET verify
	require.Len(t, crudScen.Steps, 5)
	assert.Equal(t, "POST", crudScen.Steps[0].Method)
	assert.Equal(t, "rest", crudScen.Steps[0].Role)
	assert.Contains(t, crudScen.Steps[0].Extract, "item_id")

	assert.Equal(t, "GET", crudScen.Steps[1].Method)
	assert.Contains(t, crudScen.Steps[1].Path, "{{item_id}}")

	assert.Equal(t, "PUT", crudScen.Steps[2].Method)
	assert.Equal(t, "DELETE", crudScen.Steps[3].Method)
	assert.Equal(t, "GET", crudScen.Steps[4].Method)
}

func TestReplenishScenarios_TagFilter(t *testing.T) {
	meta := sampleTestMeta()
	opts := spec.ReplenishOptions{
		Strategies: []string{"smoke"},
		Tags:       []string{"admin"},
	}

	res := spec.ReplenishScenarios(meta, opts)
	require.NotNil(t, res)
	assert.Len(t, res.Scenarios, 1)
	assert.Contains(t, res.Scenarios[0].Key, "admin")
}

func TestReplenishScenarios_UncoveredOnly(t *testing.T) {
	meta := sampleTestMeta()
	opts := spec.ReplenishOptions{
		Strategies:    []string{"smoke"},
		UncoveredOnly: true,
		CoveredKeys: map[string]bool{
			"POST /rests/categories":     true,
			"GET /rests/categories/{id}": true,
			"PUT /rests/categories/{id}": true,
			"DELETE /rests/categories/{id}": true,
			"GET /admin/audit-logs":      true,
		},
	}

	res := spec.ReplenishScenarios(meta, opts)
	require.NotNil(t, res)
	// Only /couriers/location was not in coveredKeys
	assert.Len(t, res.Scenarios, 1)
	assert.Contains(t, res.Scenarios[0].Key, "courier")
}
