// Package secret reads private instance files (API keys, denylists): only regular files that
// are not readable by group or others, bounded in size. Contents are never logged.
package secret

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MaxSize bounds a private file.
const MaxSize = 1 << 20

// ErrPermissions is returned for files readable by group or others.
var ErrPermissions = errors.New("private file must not be accessible by group or others (chmod 600)")

// ReadFile reads a private file after checking its mode.
func ReadFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat private file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("private file %s is not a regular file", p)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: %w", p, ErrPermissions)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read private file: %w", err)
	}
	if len(b) > MaxSize {
		return nil, fmt.Errorf("private file %s exceeds %d bytes", p, MaxSize)
	}
	return b, nil
}

// FromEnvOrFile returns the value of env var name if set, else the trimmed content of file
// (when file is non-empty). It never returns the value inside an error message.
func FromEnvOrFile(name, file string) (string, error) {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return v, nil
	}
	if file == "" {
		return "", nil
	}
	b, err := ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Lines parses a line-oriented private list: one entry per line, # comments, blanks skipped.
func Lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
