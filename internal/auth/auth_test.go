package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const adminID = "11111111-1111-1111-1111-111111111111"

func newTestKey(t *testing.T) (*ecdsa.PrivateKey, jwt.Keyfunc) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, func(*jwt.Token) (any, error) { return &priv.PublicKey, nil }
}

func sign(t *testing.T, priv *ecdsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"aud":  "authenticated",
		"role": "authenticated",
		"sub":  adminID,
		"exp":  time.Now().Add(time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	}
}

func TestMiddleware(t *testing.T) {
	priv, kf := newTestKey(t)

	expired := validClaims()
	expired["exp"] = time.Now().Add(-2 * time.Hour).Unix()

	wrongAud := validClaims()
	wrongAud["aud"] = "something-else"

	wrongSub := validClaims()
	wrongSub["sub"] = "22222222-2222-2222-2222-222222222222"

	// The shape of Supabase's legacy anon API key: validly signed, long-lived,
	// but role "anon" and no sub. This is the token every visitor's browser
	// holds — it must NOT pass.
	anonKey := jwt.MapClaims{
		"iss":  "supabase",
		"role": "anon",
		"aud":  "authenticated",
		"exp":  time.Now().Add(24 * time.Hour * 365).Unix(),
	}

	noExp := jwt.MapClaims{
		"aud":  "authenticated",
		"role": "authenticated",
		"sub":  adminID,
	}

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"valid", "Bearer " + sign(t, priv, validClaims()), http.StatusOK},
		{"expired", "Bearer " + sign(t, priv, expired), http.StatusUnauthorized},
		{"wrong audience", "Bearer " + sign(t, priv, wrongAud), http.StatusUnauthorized},
		{"wrong sub", "Bearer " + sign(t, priv, wrongSub), http.StatusUnauthorized},
		{"anon key", "Bearer " + sign(t, priv, anonKey), http.StatusUnauthorized},
		{"no expiry", "Bearer " + sign(t, priv, noExp), http.StatusUnauthorized},
		{"malformed", "Bearer not-a-jwt", http.StatusUnauthorized},
		{"missing header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic abc123", http.StatusUnauthorized},
	}

	v := NewVerifierWithKeyfunc(kf, adminID)
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/anything", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("got status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
