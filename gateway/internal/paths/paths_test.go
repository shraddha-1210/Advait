package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrgsDirPrecedence(t *testing.T) {
	t.Setenv("DRUNIX_ORGS", "")
	t.Setenv("DRUNIX_HOME", "")
	if got, want := OrgsDir(), filepath.Join("/root/drunix", "drunix-network", "test-network", "organizations"); got != want {
		t.Errorf("default: got %s, want %s", got, want)
	}
	t.Setenv("DRUNIX_HOME", "/home/me/drunix")
	if got, want := OrgsDir(), filepath.Join("/home/me/drunix", "drunix-network", "test-network", "organizations"); got != want {
		t.Errorf("DRUNIX_HOME: got %s, want %s", got, want)
	}
	t.Setenv("DRUNIX_ORGS", "/x/orgs")
	if got := OrgsDir(); got != "/x/orgs" {
		t.Errorf("DRUNIX_ORGS must win: got %s", got)
	}
}

func TestCheckOrgsDirNamesTheFix(t *testing.T) {
	dir := t.TempDir()
	if err := CheckOrgsDir(filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "DRUNIX_HOME") {
		t.Errorf("missing dir: %v", err)
	}
	for _, org := range []string{"org1", "org2"} {
		os.MkdirAll(filepath.Join(dir, "peerOrganizations", org+".example.com"), 0o755)
	}
	if err := CheckOrgsDir(dir); err == nil || !strings.Contains(err.Error(), "add-orgs.sh") {
		t.Errorf("no oracle/auditor orgs: %v", err)
	}
	for _, org := range []string{"oracle", "auditor"} {
		os.MkdirAll(filepath.Join(dir, "peerOrganizations", org+".example.com"), 0o755)
	}
	if err := CheckOrgsDir(dir); err != nil {
		t.Errorf("complete dir: %v", err)
	}
}

func TestOracleKeyFoundFromAnySubdirectory(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "network", "oracle", "oracle.key")
	os.MkdirAll(filepath.Dir(key), 0o755)
	os.WriteFile(key, []byte("x"), 0o600)
	sub := filepath.Join(root, "gateway", "integration")
	os.MkdirAll(sub, 0o755)
	t.Setenv("ORACLE_KEY", "")
	for _, dir := range []string{root, filepath.Join(root, "gateway"), sub} {
		t.Chdir(dir)
		got, err := OracleKey()
		if err != nil || got != key {
			t.Errorf("from %s: got %q, %v; want %s", dir, got, err, key)
		}
	}
}

func TestOracleKeyMissingExplainsWhy(t *testing.T) {
	t.Setenv("ORACLE_KEY", "")
	t.Chdir(t.TempDir())
	if _, err := OracleKey(); err == nil || !strings.Contains(err.Error(), "git-ignored") {
		t.Errorf("want an explanation, got %v", err)
	}
	t.Setenv("ORACLE_KEY", "/does/not/exist")
	if _, err := OracleKey(); err == nil {
		t.Error("a bad ORACLE_KEY must be reported")
	}
}
