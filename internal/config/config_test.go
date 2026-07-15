package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearRequiredVars ensures none of Load's required vars leak in from the
// real environment the test runner happens to have set (e.g. a developer's
// shell with a .env already sourced). t.Setenv restores the prior value
// (including "unset") after the test.
func clearRequiredVars(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SUPABASE_URL", "SUPABASE_SECRET_KEY", "DATABASE_URL", "ADMIN_PORT", "ALLOWED_ORIGIN"} {
		t.Setenv(k, "")
	}
}

// chdirWithDotenv puts the test in a temp directory (Load reads ".env"
// relative to the working directory) and optionally writes a .env file
// there.
func chdirWithDotenv(t *testing.T, dotenv string) {
	t.Helper()
	dir := t.TempDir()
	if dotenv != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

func setRequiredVars(t *testing.T) {
	t.Helper()
	t.Setenv("SUPABASE_URL", "https://project.supabase.co")
	t.Setenv("SUPABASE_SECRET_KEY", "sb_secret_test")
	t.Setenv("DATABASE_URL", "postgres://user:pass@host/db")
}

func TestLoad_FailsFastOnMissingRequiredVars(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error for missing required vars")
	}
	for _, want := range []string{"SUPABASE_URL", "SUPABASE_SECRET_KEY", "DATABASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to mention missing var %q", err.Error(), want)
		}
	}
}

func TestLoad_SucceedsAndDefaultsPort(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "")
	setRequiredVars(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AdminPort != "8090" {
		t.Errorf("AdminPort = %q, want default %q", cfg.AdminPort, "8090")
	}
}

func TestLoad_TrimsTrailingSlashFromSupabaseURL(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "")
	setRequiredVars(t)
	t.Setenv("SUPABASE_URL", "https://project.supabase.co/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SupabaseURL != "https://project.supabase.co" {
		t.Errorf("SupabaseURL = %q, want trailing slash trimmed", cfg.SupabaseURL)
	}
}

func TestLoad_DotEnvFillsInMissingVars(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "ADMIN_PORT=9090\nALLOWED_ORIGIN=from-dotenv\n")
	t.Setenv("SUPABASE_URL", "https://project.supabase.co")
	t.Setenv("SUPABASE_SECRET_KEY", "sb_secret_test")
	t.Setenv("DATABASE_URL", "postgres://user:pass@host/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AdminPort != "9090" {
		t.Errorf("AdminPort = %q, want %q from .env", cfg.AdminPort, "9090")
	}
	if cfg.AllowedOrigin != "from-dotenv" {
		t.Errorf("AllowedOrigin = %q, want %q from .env", cfg.AllowedOrigin, "from-dotenv")
	}
}

func TestLoad_RealEnvWinsOverDotEnv(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "ALLOWED_ORIGIN=from-dotenv\n")
	setRequiredVars(t)
	t.Setenv("ALLOWED_ORIGIN", "from-real-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AllowedOrigin != "from-real-env" {
		t.Errorf("AllowedOrigin = %q, want real env value %q to win over .env", cfg.AllowedOrigin, "from-real-env")
	}
}

func TestLoad_DotEnvValuesAreQuoteTrimmed(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, `ALLOWED_ORIGIN="quoted-value"`+"\n")
	setRequiredVars(t)
	t.Setenv("ALLOWED_ORIGIN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AllowedOrigin != "quoted-value" {
		t.Errorf("AllowedOrigin = %q, want surrounding quotes trimmed to %q", cfg.AllowedOrigin, "quoted-value")
	}
}

func TestLoad_DotEnvIgnoresCommentsAndBlankLines(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "# a comment\n\nALLOWED_ORIGIN=from-dotenv\n")
	setRequiredVars(t)
	t.Setenv("ALLOWED_ORIGIN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AllowedOrigin != "from-dotenv" {
		t.Errorf("AllowedOrigin = %q, want %q (comments/blank lines skipped, not misparsed)", cfg.AllowedOrigin, "from-dotenv")
	}
}

func TestLoad_MissingDotEnvFileIsNotAnError(t *testing.T) {
	clearRequiredVars(t)
	chdirWithDotenv(t, "") // temp dir with no .env file at all
	setRequiredVars(t)

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want no error when .env is simply absent", err)
	}
}
