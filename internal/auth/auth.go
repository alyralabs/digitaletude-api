// Package auth verifies Supabase-issued JWTs for admin routes.
//
// Claims are checked, not just the signature: Supabase's legacy anon and
// service_role API keys are themselves JWTs signed for the same project, so
// signature+expiry alone would accept the anon key that ships to every
// browser. See plans/00-foundations.md §4 in the frontend repo.
package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/alyralabs/digitaletude-api/internal/httpserver"
)

type Verifier struct {
	keyfunc     jwt.Keyfunc
	adminUserID string
}

// NewVerifier fetches (and auto-refreshes) the project's JWKS.
// supabaseURL is the bare project URL, e.g. https://xyz.supabase.co
func NewVerifier(supabaseURL, adminUserID string) (*Verifier, error) {
	k, err := keyfunc.NewDefault([]string{supabaseURL + "/auth/v1/.well-known/jwks.json"})
	if err != nil {
		return nil, err
	}
	return &Verifier{keyfunc: k.Keyfunc, adminUserID: adminUserID}, nil
}

// NewVerifierWithKeyfunc injects a keyfunc directly — used by tests.
func NewVerifierWithKeyfunc(kf jwt.Keyfunc, adminUserID string) *Verifier {
	return &Verifier{keyfunc: kf, adminUserID: adminUserID}
}

// Middleware rejects any request that does not carry a valid admin JWT.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			httpserver.Err(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}

		token, err := jwt.Parse(raw, v.keyfunc,
			jwt.WithValidMethods([]string{"ES256", "RS256", "HS256"}),
			jwt.WithAudience("authenticated"),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(30*time.Second),
		)
		if err != nil || !token.Valid {
			httpserver.Err(w, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			httpserver.Err(w, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}
		if role, _ := claims["role"].(string); role != "authenticated" {
			httpserver.Err(w, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}
		if sub, _ := claims.GetSubject(); sub != v.adminUserID {
			httpserver.Err(w, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}

		next.ServeHTTP(w, r)
	})
}
