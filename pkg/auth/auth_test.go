package auth

import (
	"testing"

	"ai-research-mindmapper-go/pkg/config"
)

func TestAuthBypasses(t *testing.T) {
	cfg := &config.Config{
		StressTestKey:       "stress_secret_123",
		ScriptAPIKey:        "script_secret_456",
		ExtensionPublicKey:  "ext_pubkey_789",
		ClerkPublishableKey: config.DefaultClerkPublishableKey,
	}
	a := NewAuthenticator(cfg)

	// Missing header
	if _, err := a.GetUserID(""); err == nil {
		t.Fatal("expected error on empty auth header")
	}

	// Bad prefix
	if _, err := a.GetUserID("Token 123"); err == nil {
		t.Fatal("expected error on bad auth header prefix")
	}

	// Stress test bypass
	uid, err := a.GetUserID("Bearer stress_secret_123")
	if err != nil || uid != "user_stress_test" {
		t.Fatalf("expected user_stress_test, got %s (err: %v)", uid, err)
	}

	// Script bypass
	uid, err = a.GetUserID("Bearer script_secret_456")
	if err != nil || uid != "user_local_script" {
		t.Fatalf("expected user_local_script, got %s (err: %v)", uid, err)
	}

	// Extension bypass: ext_<uuid>_<ext_pubkey_789>
	uid, err = a.GetUserID("Bearer ext_my-install-uuid-1234_ext_pubkey_789")
	if err != nil || uid != "user_ext_my-install-uuid-1234" {
		t.Fatalf("expected user_ext_my-install-uuid-1234, got %s (err: %v)", uid, err)
	}
}

func TestLiveClerkJWKS(t *testing.T) {
	cfg := &config.Config{
		ClerkPublishableKey: config.DefaultClerkPublishableKey,
	}
	a := NewAuthenticator(cfg)
	key, err := a.getKey("ins_3Cxc6w852zbl7qBYzuCGlyLRutc")
	if err != nil {
		t.Fatalf("failed to get live key: %v", err)
	}
	if key == nil || key.N == nil || key.E != 65537 {
		t.Fatalf("invalid parsed RSA public key: %+v", key)
	}
}

