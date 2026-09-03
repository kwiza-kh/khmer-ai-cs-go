package config

import "testing"

func TestValidateRejectsBadConfigs(t *testing.T) {
	valid := func() *Config {
		return &Config{
			Server:                ServerConfig{Port: "8080", Mode: "debug"},
			DatabaseURL:           "postgres://khmer:secret@localhost:5432/db",
			JWT:                   JwtConfig{Secret: "0123456789abcdef0123456789abcdef", ExpireHour: 24},
			PlatformCredentialKey: "Zm9v",
			AllowedOrigins:        []string{"http://localhost:3000"},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	// Short JWT secret.
	c := valid()
	c.JWT.Secret = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("short secret must be rejected")
	}
	// Insecure default secret.
	c = valid()
	c.JWT.Secret = InsecureDefaultJwtSecret
	if err := c.Validate(); err == nil {
		t.Fatal("insecure default must be rejected")
	}
	// Missing platform credential key.
	c = valid()
	c.PlatformCredentialKey = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing credential key must be rejected")
	}
	// Wildcard CORS.
	c = valid()
	c.AllowedOrigins = []string{"*"}
	if err := c.Validate(); err == nil {
		t.Fatal("wildcard origin must be rejected")
	}
	// Compose default password.
	c = valid()
	c.DatabaseURL = "postgres://khmer:khmer_secret@localhost:5432/db"
	if err := c.Validate(); err == nil {
		t.Fatal("default compose password must be rejected")
	}
	// Missing DATABASE_URL.
	c = valid()
	c.DatabaseURL = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing DATABASE_URL must be rejected")
	}
	// Non-postgres scheme.
	c = valid()
	c.DatabaseURL = "mysql://khmer:secret@localhost:5432/db"
	if err := c.Validate(); err == nil {
		t.Fatal("non-postgres scheme must be rejected")
	}
}
