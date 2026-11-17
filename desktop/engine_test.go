package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The desktop shell must adopt the directories of a pre-rename install instead
// of starting with an empty config and state.
func TestMigrateDirAdoptsLegacyDirectory(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, legacyStateDirName)
	if err := os.MkdirAll(filepath.Join(legacy, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "token"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !migrateDir(legacy, filepath.Join(home, stateDirName)) {
		t.Fatal("migrateDir reported that the old directory is still in use")
	}
	if _, err := os.Stat(filepath.Join(home, stateDirName, "token")); err != nil {
		t.Fatalf("token was not adopted: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory still exists: %v", err)
	}
}

func TestMigrateDirKeepsExistingNewDirectory(t *testing.T) {
	home := t.TempDir()
	renamed := filepath.Join(home, stateDirName)
	legacy := filepath.Join(home, legacyStateDirName)
	for dir, token := range map[string]string{renamed: "new", legacy: "old"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if !migrateDir(legacy, renamed) {
		t.Fatal("migrateDir should succeed when the new directory already exists")
	}
	data, err := os.ReadFile(filepath.Join(renamed, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("token = %q, want the new directory untouched", data)
	}
}

func TestEnvValuePrefersRenamedVariable(t *testing.T) {
	t.Setenv("ISSUEKILLER_TEST_VALUE", "new")
	t.Setenv("GHIK_TEST_VALUE", "old")
	if got := envValue("ISSUEKILLER_TEST_VALUE", "GHIK_TEST_VALUE"); got != "new" {
		t.Fatalf("value = %q, want the renamed variable", got)
	}
	t.Setenv("ISSUEKILLER_TEST_VALUE", "")
	if got := envValue("ISSUEKILLER_TEST_VALUE", "GHIK_TEST_VALUE"); got != "old" {
		t.Fatalf("value = %q, want the legacy fallback", got)
	}
}
