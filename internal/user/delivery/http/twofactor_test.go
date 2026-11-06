package delivery

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/api"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/transport/http/principal"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/user/usecase"
	"go.uber.org/mock/gomock"
)

func TestSetupResponseIsJSONAndNeverCached(t *testing.T) {
	uc := NewMockUsecase(gomock.NewController(t))
	ctx := principal.WithUserID(context.Background(), 7)
	uc.EXPECT().EnableTwoFactor(ctx, int64(7)).Return(usecase.TwoFactorSetup{QRCode: "data:image/png;base64,AAAA", Secret: "ABCDEF"}, nil)
	resp, err := NewHandler(uc).EnableTwoFactorAuth(ctx, api.EnableTwoFactorAuthRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err = resp.VisitEnableTwoFactorAuthResponse(w); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d headers=%v", w.Code, w.Header())
	}
}
