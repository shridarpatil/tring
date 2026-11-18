package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	k := Load()
	if got := k.String("esl_addr"); got != "127.0.0.1:8021" {
		t.Errorf("esl_addr default = %q, want 127.0.0.1:8021", got)
	}
	if got := k.String("gateway_addr"); got != ":8087" {
		t.Errorf("gateway_addr default = %q, want :8087", got)
	}
}

func TestLoadEnvOverridesDefault(t *testing.T) {
	t.Setenv("TRING_ESL_ADDR", "10.0.0.5:9000")
	t.Setenv("TRING_DSN", "postgres://x/y")
	t.Setenv("TRING_GATEWAY_PUBLIC_URL", "https://gw.example.com")

	k := Load()
	cases := map[string]string{
		"esl_addr":           "10.0.0.5:9000", // TRING_ESL_ADDR → esl_addr
		"dsn":                "postgres://x/y",
		"gateway_public_url": "https://gw.example.com", // multi-underscore stays flat
	}
	for key, want := range cases {
		if got := k.String(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestWeakSecretsFlagsDefaultsAndShortValues(t *testing.T) {
	k := Load()
	got := WeakSecrets(k, "jwt_secret", "esl_password")
	if len(got) != 2 {
		t.Fatalf("built-in defaults: WeakSecrets = %v, want both", got)
	}

	t.Setenv("TRING_JWT_SECRET", "short")
	t.Setenv("TRING_ESL_PASSWORD", "0123456789abcdef0123456789abcdef")
	got = WeakSecrets(Load(), "jwt_secret", "esl_password")
	if len(got) != 1 || got[0] != "jwt_secret" {
		t.Fatalf("WeakSecrets = %v, want [jwt_secret]", got)
	}
}
