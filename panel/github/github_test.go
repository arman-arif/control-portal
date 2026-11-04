package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAppJWTVerifies(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_800_000_000, 0)
	jwt, err := AppJWT(42, key, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(b, &claims)
	if claims.Iss != "42" || claims.Iat != now.Unix()-60 || claims.Exp-claims.Iat > 600 {
		t.Fatalf("claims %+v", claims)
	}
}

func TestParseKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for _, p := range [][]byte{pkcs1, pkcs8} {
		got, err := ParseKey(p)
		if err != nil || !got.Equal(key) {
			t.Fatalf("ParseKey: %v", err)
		}
	}
	if _, err := ParseKey([]byte("nope")); err == nil {
		t.Fatal("accepted garbage")
	}
}

func TestInstallationToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/7/access_tokens" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"message":"nope"}`, 401)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		w.Write([]byte(`{"token":"ghs_abc","expires_at":"2030-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	c := &Client{API: srv.URL, HTTP: srv.Client()}
	tok, err := c.InstallationToken(context.Background(), 1, key, 7, []string{"shop"})
	if err != nil || tok != "ghs_abc" {
		t.Fatalf("token %q %v", tok, err)
	}
	if repos, _ := got["repositories"].([]any); len(repos) != 1 || repos[0] != "shop" {
		t.Fatalf("body %+v", got)
	}
	if _, err := c.InstallationToken(context.Background(), 1, key, 8, nil); err == nil {
		t.Fatal("no error for a refused request")
	}
}
