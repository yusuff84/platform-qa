package fixtures

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

// token builds an unsigned JWT with the given claims — the shape the backend
// issues, minus a signature the engine never verifies.
func token(claims map[string]interface{}) string {
	body, _ := json.Marshal(claims)
	return fmt.Sprintf("%s.%s.sig",
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`)),
		base64.RawURLEncoding.EncodeToString(body))
}

func newManager() *FixtureManager {
	return NewFixtureManager(nil, nil, nil, nil)
}

func TestBindAccount_RejectsTokenOfAnotherRole(t *testing.T) {
	fm := newManager()
	courierToken := token(map[string]interface{}{"user_id": "k-1", "isCourier": true})

	// The whole point of the check: a suite bound to the wrong role would keep
	// passing while testing the wrong identity.
	if err := fm.BindAccount("client", "Клиент", "+79991234567", courierToken); err == nil {
		t.Fatal("курьерский токен не должен привязываться к роли client")
	}
	if _, _, _, bound := fm.BoundAccount("client"); bound {
		t.Error("отклонённая привязка не должна сохраняться")
	}
}

func TestBindAccount_RequiresIdentityForNonAdminRoles(t *testing.T) {
	fm := newManager()

	// A courier without user_id cannot be assigned an order.
	noID := token(map[string]interface{}{"isCourier": true})
	if err := fm.BindAccount("courier", "Курьер", "", noID); err == nil {
		t.Fatal("курьер без user_id не должен привязываться")
	}

	// The hardcoded super-root admin legitimately has none.
	root := token(map[string]interface{}{"isAdmin": true, "user_id": nil})
	if err := fm.BindAccount("admin", "Супер-админ", "root", root); err != nil {
		t.Fatalf("админ без user_id должен привязываться: %v", err)
	}
}

func TestBindAccount_CourierFixtureReturnsCourierID(t *testing.T) {
	fm := newManager()
	courierToken := token(map[string]interface{}{"user_id": "courier-uuid-1", "isCourier": true, "phoneNumber": "+79221234567"})

	if err := fm.BindAccount("courier", "Курьер Иван", "+79221234567", courierToken); err != nil {
		t.Fatalf("привязка курьера: %v", err)
	}

	// The admin API assigns orders by courier_id, so the fixture must hand
	// back the id from the token — not the phone it was bound under.
	id, tok, err := fm.CreateUniqueCourier(context.Background())
	if err != nil {
		t.Fatalf("CreateUniqueCourier: %v", err)
	}
	if id != "courier-uuid-1" {
		t.Errorf("ожидался courier_id из токена, получено %q", id)
	}
	if tok != courierToken {
		t.Error("должен возвращаться привязанный токен")
	}
}

func TestBindAccount_ClientAndRestReturnBoundIdentity(t *testing.T) {
	fm := newManager()

	clientToken := token(map[string]interface{}{"user_id": "client-uuid"})
	if err := fm.BindAccount("client", "VIP", "+79991112233", clientToken); err != nil {
		t.Fatalf("привязка клиента: %v", err)
	}
	phone, tok, err := fm.CreateUniqueClient(context.Background())
	if err != nil {
		t.Fatalf("CreateUniqueClient: %v", err)
	}
	if phone != "+79991112233" || tok != clientToken {
		t.Errorf("клиент: ожидался привязанный аккаунт, получено %q", phone)
	}

	restToken := token(map[string]interface{}{"user_id": "rest-uuid", "isRestaurant": true})
	if err := fm.BindAccount("restaurant", "Бургерная", "burger_login", restToken); err != nil {
		t.Fatalf("привязка ресторана (алиас restaurant): %v", err)
	}
	login, tok, err := fm.CreateUniqueRestaurant(context.Background())
	if err != nil {
		t.Fatalf("CreateUniqueRestaurant: %v", err)
	}
	if login != "burger_login" || tok != restToken {
		t.Errorf("ресторан: ожидался привязанный аккаунт, получено %q", login)
	}
}

func TestBindAccount_EmptyTokenClearsBinding(t *testing.T) {
	fm := newManager()
	clientToken := token(map[string]interface{}{"user_id": "client-uuid"})

	if err := fm.BindAccount("client", "VIP", "+79991112233", clientToken); err != nil {
		t.Fatalf("привязка: %v", err)
	}
	if err := fm.BindAccount("client", "", "", ""); err != nil {
		t.Fatalf("снятие привязки: %v", err)
	}
	if _, _, _, bound := fm.BoundAccount("client"); bound {
		t.Fatal("после снятия привязки роль должна снова генерировать аккаунт")
	}
}

func TestBindAccount_UnknownRole(t *testing.T) {
	fm := newManager()
	if err := fm.BindAccount("manager", "", "", token(map[string]interface{}{"user_id": "x"})); err == nil {
		t.Fatal("неизвестная роль должна отклоняться")
	}
}
