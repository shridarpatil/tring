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
	t.Setenv("TRING_FS_BASIC_AUTH", "fs:secret")

	k := Load()
	cases := map[string]string{
		"esl_addr":      "10.0.0.5:9000", // TRING_ESL_ADDR → esl_addr
		"dsn":           "postgres://x/y",
		"fs_basic_auth": "fs:secret", // multi-underscore stays flat
	}
	for key, want := range cases {
		if got := k.String(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
