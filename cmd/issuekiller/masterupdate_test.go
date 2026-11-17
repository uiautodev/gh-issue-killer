package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeadFromLSRemote(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{"abc123\trefs/heads/master\n", "abc123"},
		{"  abc123   refs/heads/master  ", "abc123"},
		{"", ""},
		{"   \n\t", ""},
	}
	for _, tc := range cases {
		if got := headFromLSRemote(tc.out); got != tc.want {
			t.Fatalf("headFromLSRemote(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

func TestFollowMasterConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"repos": ["acme/app"], "followMaster": true, "selfUpdate": true}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FollowMaster || !cfg.SelfUpdate {
		t.Fatalf("followMaster=%v selfUpdate=%v, want both true", cfg.FollowMaster, cfg.SelfUpdate)
	}

	plain := filepath.Join(t.TempDir(), "plain.json")
	if err := os.WriteFile(plain, []byte(`{"repos": ["acme/app"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err := loadConfig(plain); err != nil || cfg.FollowMaster {
		t.Fatalf("default followMaster = %v (err %v), want false", cfg.FollowMaster, err)
	}
}
