package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// readOnlyGit lists the only git sub-commands docs-platform may run against an inspected repo.
var readOnlyGit = map[string]bool{
	"diff": true, "log": true, "rev-parse": true, "ls-files": true, "show": true,
	"blame": true, "merge-base": true, // the agentic judge's changes_since and drift validation
}

// refPattern accepts branch names, tags, SHAs and rev expressions like HEAD~1 or origin/main,
// and nothing that git could read as an option.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/~^@{}-]{0,200}$`)

// ValidRef reports whether s is safe to pass to git as a revision.
func ValidRef(s string) bool {
	return refPattern.MatchString(s) && !strings.Contains(s, "..")
}

// GitTimeout bounds every git invocation.
const GitTimeout = 60 * time.Second

// Git runs a read-only git sub-command in dir. Arguments are passed as an argv vector, never
// through a shell; the sub-command must be on the read-only allowlist.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) == 0 || !readOnlyGit[args[0]] {
		return "", fmt.Errorf("git sub-command %v is not on the read-only allowlist", args)
	}
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()
	full := append([]string{"-C", dir, "--no-pager", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec // argv vector, allowlisted sub-command, validated refs
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "PATH=" + safePath()}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(errb.String()))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return out.String(), nil
}

// ChangedFiles lists files changed between base and head (merge-base semantics, like a PR).
func ChangedFiles(ctx context.Context, dir, base, head string) ([]string, error) {
	if !ValidRef(base) || !ValidRef(head) {
		return nil, fmt.Errorf("invalid git ref %q or %q", base, head)
	}
	out, err := Git(ctx, dir, "diff", "--name-only", "--no-renames", "-z", base+"..."+head, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// CommitMessages returns the full messages of commits in base..head.
func CommitMessages(ctx context.Context, dir, base, head string) (string, error) {
	if !ValidRef(base) || !ValidRef(head) {
		return "", fmt.Errorf("invalid git ref %q or %q", base, head)
	}
	return Git(ctx, dir, "log", "--format=%B%x00", base+".."+head, "--")
}
