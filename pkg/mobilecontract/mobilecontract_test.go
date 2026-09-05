package mobilecontract_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"locali-e2e-engine/pkg/mobilecontract"
)

const sampleDartCourier = `
class Courier {
  const Courier({
    required this.id,
    required this.name,
    this.phone,
    this.workStatus = false,
  });

  final String id;
  final String name;
  final String? phone;
  final bool workStatus;

  factory Courier.fromJson(Map<String, dynamic> json) {
    return Courier(
      id: readString(json, ['courier_id', 'courierId', 'id']) ?? '',
      name: readString(json, ['name', 'firstName']) ?? 'Курьер',
      phone: readString(json, ['phoneNumber', 'phone']),
      workStatus: readBool(json, ['workStatus', 'isWorking']),
    );
  }
}
`

const sampleSwiftOrder = `
struct OrderDTO: Codable {
    let orderId: String
    let orderNumber: Int
    let deliveryPrice: Double?
    let isPaid: Bool
    
    enum CodingKeys: String, CodingKey {
        case orderId = "order_id"
        case orderNumber = "orderNumber"
        case deliveryPrice = "delivery_price"
        case isPaid = "is_paid"
    }
}
`

const sampleKotlinUser = `
data class UserDTO(
    @SerializedName("user_id")
    val id: String,
    @SerializedName("phone_number")
    val phone: String? = null,
    val isActive: Boolean = true
)
`

func TestParseDartSource(t *testing.T) {
	models := mobilecontract.ParseDartSource(sampleDartCourier, "lib/domain/courier.dart")
	require.Len(t, models, 1)

	m := models[0]
	assert.Equal(t, "Courier", m.Name)
	assert.Equal(t, mobilecontract.PlatformFlutter, m.Platform)
	require.Len(t, m.Fields, 4)

	// id: required, non-nullable String, keys ['courier_id', 'courierId', 'id']
	idField := m.Fields[0]
	assert.Equal(t, "id", idField.Name)
	assert.Equal(t, "string", idField.Type)
	assert.True(t, idField.IsRequired)
	assert.False(t, idField.IsNullable)
	assert.Contains(t, idField.JSONKeys, "courier_id")

	// phone: optional String?
	phoneField := m.Fields[2]
	assert.Equal(t, "phone", phoneField.Name)
	assert.True(t, phoneField.IsNullable)
	assert.Contains(t, phoneField.JSONKeys, "phoneNumber")
}

func TestParseSwiftSource(t *testing.T) {
	models := mobilecontract.ParseSwiftSource(sampleSwiftOrder, "OrderDTO.swift")
	require.Len(t, models, 1)

	m := models[0]
	assert.Equal(t, "OrderDTO", m.Name)
	assert.Equal(t, mobilecontract.PlatformIOS, m.Platform)
	require.Len(t, m.Fields, 4)

	// orderId maps to order_id
	orderIdField := m.Fields[0]
	assert.Equal(t, "orderId", orderIdField.Name)
	assert.Equal(t, "string", orderIdField.Type)
	assert.True(t, orderIdField.IsRequired)
	assert.Contains(t, orderIdField.JSONKeys, "order_id")

	// deliveryPrice is optional Double?
	priceField := m.Fields[2]
	assert.Equal(t, "deliveryPrice", priceField.Name)
	assert.Equal(t, "double", priceField.Type)
	assert.True(t, priceField.IsNullable)
	assert.Contains(t, priceField.JSONKeys, "delivery_price")
}

func TestParseKotlinSource(t *testing.T) {
	models := mobilecontract.ParseKotlinSource(sampleKotlinUser, "UserDTO.kt")
	require.Len(t, models, 1)

	m := models[0]
	assert.Equal(t, "UserDTO", m.Name)
	assert.Equal(t, mobilecontract.PlatformAndroid, m.Platform)
	require.Len(t, m.Fields, 3)

	idField := m.Fields[0]
	assert.Equal(t, "id", idField.Name)
	assert.True(t, idField.IsRequired)
	assert.Contains(t, idField.JSONKeys, "user_id")

	phoneField := m.Fields[1]
	assert.Equal(t, "phone", phoneField.Name)
	assert.True(t, phoneField.IsNullable)
	assert.Contains(t, phoneField.JSONKeys, "phone_number")
}

func TestValidateModelAgainstJSON_Scenarios(t *testing.T) {
	models := mobilecontract.ParseDartSource(sampleDartCourier, "courier.dart")
	require.Len(t, models, 1)
	courierModel := models[0]

	t.Run("fully compatible payload", func(t *testing.T) {
		payload := map[string]interface{}{
			"courier_id": "c-123",
			"name":       "Иван Курьер",
			"phone":      "+79991112233",
			"workStatus": true,
		}
		compat := mobilecontract.ValidateModelAgainstJSON(courierModel, payload)
		assert.Equal(t, "COMPATIBLE", compat.Status)
		assert.Equal(t, 100.0, compat.MatchRatio)
	})

	t.Run("nullability crash: required field is null", func(t *testing.T) {
		payload := map[string]interface{}{
			"courier_id": nil, // backend returned null for required non-nullable String id!
			"name":       "Иван",
		}
		compat := mobilecontract.ValidateModelAgainstJSON(courierModel, payload)
		assert.Equal(t, "INCOMPATIBLE_CRASH", compat.Status)

		require.NotEmpty(t, compat.Issues)
		assert.Equal(t, mobilecontract.SeverityCrash, compat.Issues[0].Severity)
		assert.Contains(t, compat.Issues[0].Description, "TypeError / DecodingError")
	})

	t.Run("missing required field", func(t *testing.T) {
		payload := map[string]interface{}{
			"phone": "+79991112233", // id and name missing
		}
		compat := mobilecontract.ValidateModelAgainstJSON(courierModel, payload)
		assert.Equal(t, "INCOMPATIBLE_CRASH", compat.Status)
	})
}

func TestScanDirectory(t *testing.T) {
	// Test scanning non-existent path
	_, err := mobilecontract.ScanDirectory("/path/does/not/exist")
	assert.Error(t, err)

	// Test scanning current directory (returns 0 mobile models, but no error)
	models, err := mobilecontract.ScanDirectory(".")
	assert.NoError(t, err)
	assert.NotNil(t, models)
}

func TestAppStore_CRUD(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := mobilecontract.NewAppStore(tmpDir)
	require.NoError(t, err)

	// Seeded with default Locali Director
	list := store.List()
	require.Len(t, list, 1)
	assert.Equal(t, "Locali Director", list[0].Name)

	// Add new app
	added, err := store.Add(mobilecontract.MobileApp{
		Name:       "Locali Client iOS",
		Platform:   mobilecontract.PlatformIOS,
		SourceType: "gitlab",
		RepoURL:    "https://lokaligitlabru.ru/app/locali-client-ios.git",
		Branch:     "develop",
	})
	require.NoError(t, err)
	assert.Equal(t, "locali-client-ios", added.ID)

	// List has 2 apps now
	require.Len(t, store.List(), 2)

	// Get app
	got, ok := store.Get("locali-client-ios")
	require.True(t, ok)
	assert.Equal(t, "develop", got.Branch)

	// Update app
	got.Branch = "release-1.2"
	err = store.Update(got.ID, got)
	require.NoError(t, err)

	updated, _ := store.Get("locali-client-ios")
	assert.Equal(t, "release-1.2", updated.Branch)

	// Delete app
	err = store.Delete("locali-client-ios")
	require.NoError(t, err)
	assert.Len(t, store.List(), 1)
}
