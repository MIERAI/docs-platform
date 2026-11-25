package repo

import "os"

// safePath passes the caller's PATH through so git can be found; nothing else from the
// environment (tokens, GIT_* overrides) reaches the child process.
func safePath() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	return "/usr/bin:/bin:/usr/local/bin"
}
