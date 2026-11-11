package delivery

import (
	"testing"

	"go.uber.org/mock/gomock"
)

func testTokens(t *testing.T) *MockTokenParser {
	t.Helper()
	tokens := NewMockTokenParser(gomock.NewController(t))
	// Хелперы маршрутизации обслуживают и анонимные запросы; разбор токена здесь необязателен.
	tokens.EXPECT().Parse("access-1").Return(testClaims(1), nil).AnyTimes()
	tokens.EXPECT().Parse("access-42").Return(testClaims(42), nil).AnyTimes()
	return tokens
}
