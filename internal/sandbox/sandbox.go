// Package sandbox runs untrusted snippets (documentation examples) in a real isolation
// boundary: a throw-away container with no network, a read-only root file system, a tmpfs
// working directory, no capabilities, no privilege escalation, a non-root user, process,
// memory and CPU limits and a wall-clock timeout; host directories are mounted read-only.
//
// Clearing environment variables is not isolation: when no container runtime is available
// the caller gets ErrNoSandbox and must report the example as not run, never run it on the
// host.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is busybox 1.36 pinned by its multi-architecture index digest: a ~4 MB image
// with a POSIX shell and coreutils, nothing else.
const DefaultImage = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"

// ErrNoSandbox is returned when no container runtime is available: nothing may run.
var ErrNoSandbox = errors.New("no sandbox: docker is not available")

// Default limits.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultMemory    = "256m"
	DefaultCPUs      = "1"
	DefaultPids      = 64
	DefaultUser      = "65534:65534" // nobody
	DefaultTmpfsSize = "64m"
	// Workdir is the container's writable working directory (tmpfs).
	Workdir = "/work"
	// maxOutput bounds the output kept from one run (its tail).
	maxOutput = 16 << 10
)

// Limits bound one run. Zero values take the defaults.
type Limits struct {
	Timeout   time.Duration
	Memory    string
	CPUs      string
	Pids      int
	User      string
	TmpfsSize string
}

func (l Limits) withDefaults() Limits {
	if l.Timeout <= 0 {
		l.Timeout = DefaultTimeout
	}
	if l.Memory == "" {
		l.Memory = DefaultMemory
	}
	if l.CPUs == "" {
		l.CPUs = DefaultCPUs
	}
	if l.Pids <= 0 {
		l.Pids = DefaultPids
	}
	if l.User == "" {
		l.User = DefaultUser
	}
	if l.TmpfsSize == "" {
		l.TmpfsSize = DefaultTmpfsSize
	}
	return l
}

// Mount is a host directory mounted read-only into the container (there are no writable
// mounts).
type Mount struct {
	Host      string
	Container string
}

// Spec is one run: Script is run by the image's /bin/sh -c (stdin is closed).
type Spec struct {
	Image  string
	Mounts []Mount
	Env    []string
	Script string
	Limits Limits
}

// Result of a run. ExitCode is -1 when the run timed out.
type Result struct {
	ExitCode int           `json:"exit_code"`
	TimedOut bool          `json:"timed_out,omitempty"`
	Output   string        `json:"output,omitempty"`
	Duration time.Duration `json:"-"`
}

// Runner runs a spec in isolation.
type Runner interface {
	Name() string
	// Arch is the GOARCH of the sandbox (for host-built binaries).
	Arch() string
	Run(ctx context.Context, s Spec) (Result, error)
}

// Docker is the docker CLI runner.
type Docker struct {
	bin  string
	arch string
}

// Detect finds a working docker daemon; ErrNoSandbox when there is none.
func Detect(ctx context.Context) (*Docker, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrNoSandbox
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version", "--format", "{{.Server.Arch}}").Output() //nolint:gosec // docker CLI from PATH, fixed argv
	arch := strings.TrimSpace(string(out))
	if err != nil || arch == "" {
		return nil, ErrNoSandbox
	}
	return &Docker{bin: bin, arch: arch}, nil
}

// Name implements Runner.
func (*Docker) Name() string { return "docker" }

// Arch implements Runner.
func (d *Docker) Arch() string { return d.arch }

// Args is the docker command line of a run (without the binary). Every isolation flag is
// here, in one place, so that a test can assert it.
func Args(name string, s Spec) []string {
	l := s.Limits.withDefaults()
	img := s.Image
	if img == "" {
		img = DefaultImage
	}
	a := []string{
		"run", "--rm", "--name", name,
		"--network", "none",
		"--read-only",
		"--tmpfs", Workdir + ":rw,nosuid,nodev,mode=1777,size=" + l.TmpfsSize,
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,mode=1777,size=16m",
		"--workdir", Workdir,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(l.Pids),
		"--memory", l.Memory, "--memory-swap", l.Memory,
		"--cpus", l.CPUs,
		"--user", l.User,
		"--ipc", "none",
		"--ulimit", "nofile=256:256",
		"--stop-timeout", "1",
		"--log-driver", "none",
		"--env", "HOME=" + Workdir, "--env", "TMPDIR=/tmp",
	}
	for _, e := range s.Env {
		a = append(a, "--env", e)
	}
	for _, m := range s.Mounts {
		a = append(a, "--mount", "type=bind,src="+m.Host+",dst="+path.Clean(m.Container)+",readonly")
	}
	return append(a, "--entrypoint", "/bin/sh", img, "-c", s.Script)
}

// Run implements Runner. The container is always removed (docker rm -f -v), also when the
// run times out: killing the docker CLI alone would leave it running.
func (d *Docker) Run(ctx context.Context, s Spec) (Result, error) {
	for _, m := range s.Mounts {
		if strings.ContainsAny(m.Host+m.Container, ",\n") || !strings.HasPrefix(m.Container, "/") {
			return Result{}, fmt.Errorf("sandbox: unsafe mount %q -> %q", m.Host, m.Container)
		}
	}
	name, err := containerName()
	if err != nil {
		return Result{}, err
	}
	l := s.Limits.withDefaults()
	rctx, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	defer d.remove(name)
	cmd := exec.CommandContext(rctx, d.bin, Args(name, s)...) //nolint:gosec // docker CLI, argv vector; the script is an argument of the container's shell
	var out tail
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	err = cmd.Run()
	r := Result{Output: out.String(), Duration: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case rctx.Err() != nil && ctx.Err() == nil:
		r.ExitCode, r.TimedOut = -1, true
		return r, nil
	case errors.As(err, &ee):
		r.ExitCode = ee.ExitCode()
		if r.ExitCode == 125 { // docker itself failed (image, flags, daemon)
			return r, fmt.Errorf("sandbox: docker run: %s", lastLine(r.Output))
		}
		return r, nil
	case err != nil:
		return r, fmt.Errorf("sandbox: %w", err)
	}
	return r, nil
}

func (d *Docker) remove(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, d.bin, "rm", "-f", "-v", name).Run() //nolint:gosec // docker CLI, fixed argv
}

func containerName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	return "docsp-sandbox-" + hex.EncodeToString(b), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// tail keeps the last maxOutput bytes written.
type tail struct{ b bytes.Buffer }

func (t *tail) Write(p []byte) (int, error) {
	t.b.Write(p)
	if over := t.b.Len() - maxOutput; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

func (t *tail) String() string { return t.b.String() }
