package secret

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadFilePermissions(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok")
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(ok, []byte("v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loose, []byte("sensitive-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFile(ok); err != nil || string(b) != "v\n" {
		t.Errorf("ReadFile(ok) = %q, %v", b, err)
	}
	_, err := ReadFile(loose)
	if !errors.Is(err, ErrPermissions) {
		t.Fatalf("ReadFile(loose) err = %v, want ErrPermissions", err)
	}
	if strings.Contains(err.Error(), "sensitive-value") {
		t.Error("error message leaks file content")
	}
	if _, err := ReadFile(dir); err == nil {
		t.Error("a directory must be refused")
	}
}

func TestFromEnvOrFile(t *testing.T) {
	t.Setenv("DOCSP_TEST_SECRET", "from-env")
	if v, err := FromEnvOrFile("DOCSP_TEST_SECRET", "/nonexistent"); err != nil || v != "from-env" {
		t.Errorf("env: %q %v", v, err)
	}
	if v, err := FromEnvOrFile("DOCSP_TEST_UNSET", ""); err != nil || v != "" {
		t.Errorf("unset: %q %v", v, err)
	}
}

func TestLines(t *testing.T) {
	got := Lines([]byte("# c\n a \n\nb\n#x\n"))
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Lines = %q", got)
	}
}
