package delivery

import (
	"net/http"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/api"
)

// Generated response headers support one Set-Cookie. These adapters add each
// cookie separately before delegating the status and body to the generated code.
type responseCookies []*http.Cookie

func (cookies responseCookies) write(w http.ResponseWriter) {
	for _, cookie := range cookies {
		http.SetCookie(w, cookie)
	}
}

type loginResponse struct {
	api.LoginUser200JSONResponse
	cookies responseCookies
}

func (response loginResponse) VisitLoginUserResponse(w http.ResponseWriter) error {
	response.cookies.write(w)
	return response.LoginUser200JSONResponse.VisitLoginUserResponse(w)
}

type registerResponse struct {
	api.RegisterUser201JSONResponse
	cookies responseCookies
}

func (response registerResponse) VisitRegisterUserResponse(w http.ResponseWriter) error {
	response.cookies.write(w)
	return response.RegisterUser201JSONResponse.VisitRegisterUserResponse(w)
}

type refreshResponse struct {
	api.RefreshToken204Response
	cookies responseCookies
}

func (response refreshResponse) VisitRefreshTokenResponse(w http.ResponseWriter) error {
	response.cookies.write(w)
	return response.RefreshToken204Response.VisitRefreshTokenResponse(w)
}

type logoutResponse struct {
	api.LogoutUser204Response
	cookies responseCookies
}

func (response logoutResponse) VisitLogoutUserResponse(w http.ResponseWriter) error {
	response.cookies.write(w)
	return response.LogoutUser204Response.VisitLogoutUserResponse(w)
}
