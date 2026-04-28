package config

import (
	"testing"
)

func TestParseCookies(t *testing.T) {
	tests := []struct {
		raw  string
		want map[string]string
	}{
		{"", map[string]string{}},
		{"k=v", map[string]string{"k": "v"}},
		{"a=1;b=2;c=3", map[string]string{"a": "1", "b": "2", "c": "3"}},
		{"  k = v ; k2 = v2 ", map[string]string{"k": "v", "k2": "v2"}},
		{"ipb_member_id=123;ipb_pass_hash=abc;igneous=xyz", map[string]string{
			"ipb_member_id": "123", "ipb_pass_hash": "abc", "igneous": "xyz",
		}},
	}
	for _, tt := range tests {
		got := parseCookies(tt.raw)
		if len(got) != len(tt.want) {
			t.Errorf("parseCookies(%q) = %v, want %v", tt.raw, got, tt.want)
			continue
		}
		for k, v := range tt.want {
			if got[k] != v {
				t.Errorf("parseCookies(%q)[%q] = %q, want %q", tt.raw, k, got[k], v)
			}
		}
	}
}

func TestGetEnvFloat(t *testing.T) {
	t.Setenv("TEST_FLOAT", "2.5")
	if v := getEnvFloat("TEST_FLOAT", 1.0); v != 2.5 {
		t.Errorf("got %f, want 2.5", v)
	}
	if v := getEnvFloat("TEST_FLOAT_MISSING", 3.0); v != 3.0 {
		t.Errorf("got %f, want 3.0", v)
	}
}

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("EX_COOKIES", "ipb_member_id=123;igneous=abc")
	t.Setenv("EX_BASE_URL", "https://exhentai.org")
	t.Setenv("RATE_INTERVAL", "2.0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.Cookies["ipb_member_id"] != "123" {
		t.Errorf("Cookies = %v", cfg.Cookies)
	}
	if cfg.RateInterval != 2.0 {
		t.Errorf("RateInterval = %f", cfg.RateInterval)
	}
}

func TestLoadMissingDB(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	_, err := Load()
	if err == nil {
		t.Error("expected error for missing DATABASE_URL")
	}
}
