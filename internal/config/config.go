package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	SupabaseURL       string
	SupabaseSecretKey string
	DatabaseURL       string
	AdminUserID       string
	Port              string
	AllowedOrigin     string
}

// Load reads configuration from the environment (after merging an optional
// .env file in the working directory) and fails fast on anything missing.
func Load() (Config, error) {
	loadDotenv(".env")

	cfg := Config{
		SupabaseURL:       strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/"),
		SupabaseSecretKey: os.Getenv("SUPABASE_SECRET_KEY"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AdminUserID:       os.Getenv("ADMIN_USER_ID"),
		Port:              os.Getenv("PORT"),
		AllowedOrigin:     os.Getenv("ALLOWED_ORIGIN"),
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}

	var missing []string
	for name, val := range map[string]string{
		"SUPABASE_URL":        cfg.SupabaseURL,
		"SUPABASE_SECRET_KEY": cfg.SupabaseSecretKey,
		"DATABASE_URL":        cfg.DatabaseURL,
		"ADMIN_USER_ID":       cfg.AdminUserID,
	} {
		if val == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// loadDotenv sets vars from a KEY=VALUE file without overriding the real
// environment. Deliberately minimal: no quoting rules beyond trimming a
// single pair of surrounding quotes, no expansion.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, val)
	}
}
