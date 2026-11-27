package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArgsCarryEveryIsolationFlag(t *testing.T) {
	a := Args("n", Spec{Mounts: []Mount{{Host: "/src", Container: "/repo"}}, Script: "true"})
	joined := strings.Join(a, " ")
	for _, want := range []string{
		"--network none", "--read-only", "--tmpfs /work:rw,nosuid,nodev,mode=1777,size=64m", "--cap-drop ALL",
		"--security-opt no-new-privileges", "--pids-limit 64", "--memory 256m", "--memory-swap 256m",
		"--cpus 1", "--user 65534:65534", "--workdir /work",
		"--mount type=bind,src=/src,dst=/repo,readonly", DefaultImage,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker args lack %q:\n%s", want, joined)
		}
	}
	if slices.Contains(a, "--privileged") || strings.Contains(joined, "rw,") && strings.Contains(joined, "type=bind,src=/src,dst=/repo,rw") {
		t.Fatalf("writable or privileged run: %s", joined)
	}
	if !strings.Contains(DefaultImage, "@sha256:") {
		t.Fatalf("the image must be pinned by digest: %s", DefaultImage)
	}
}

func TestRunRejectsUnsafeMounts(t *testing.T) {
	d := &Docker{bin: "/nonexistent/docker"}
	if _, err := d.Run(context.Background(), Spec{Mounts: []Mount{{Host: "/a,dst=/etc", Container: "/x"}}}); err == nil {
		t.Fatal("a mount with a comma must be refused")
	}
}

// docker runs the sandbox tests on a host with a docker daemon; elsewhere they are skipped.
func docker(t *testing.T) *Docker {
	t.Helper()
	if testing.Short() {
		t.Skip("sandbox tests need docker (skipped in -short)")
	}
	d, err := Detect(context.Background())
	if err != nil {
		t.Skip("skipped: no sandbox (docker not available)")
	}
	return d
}

func run(t *testing.T, d *Docker, s Spec) Result {
	t.Helper()
	r, err := d.Run(context.Background(), s)
	if err != nil {
		t.Fatalf("run: %v (%s)", err, r.Output)
	}
	return r
}

func TestSandboxIsolation(t *testing.T) {
	d := docker(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, script, want string }{
		{"network is blocked", `ls /sys/class/net; if wget -q -T 3 -O /dev/null http://1.1.1.1/ 2>/dev/null; then echo NET-OPEN; else echo net-blocked; fi`, "net-blocked"},
		{"only loopback", `ls /sys/class/net | tr '\n' ' '`, "lo "},
		{"rootfs is read-only", `if touch /etc/x 2>/dev/null || touch /x 2>/dev/null; then echo ROOT-WRITABLE; else echo root-ro; fi`, "root-ro"},
		{"repo mount is read-only", `cat /repo/README.md; if touch /repo/x 2>/dev/null; then echo REPO-WRITABLE; else echo repo-ro; fi`, "hello\nrepo-ro"},
		{"workdir is writable tmpfs", `pwd; echo ok > f && cat f`, "/work\nok"},
		{"non-root, no capabilities", `id -u; grep CapEff /proc/self/status`, "65534\nCapEff:\t0000000000000000"},
		{"no privilege escalation", `grep NoNewPrivs /proc/self/status`, "NoNewPrivs:\t1"},
		{"process and memory limits", `cat /sys/fs/cgroup/pids.max /sys/fs/cgroup/memory.max`, "64\n268435456"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := run(t, d, Spec{Mounts: []Mount{{Host: dir, Container: "/repo"}}, Script: c.script})
			if !strings.Contains(r.Output, c.want) || strings.Contains(r.Output, "OPEN") || strings.Contains(r.Output, "WRITABLE") {
				t.Fatalf("output %q, want %q (exit %d)", r.Output, c.want, r.ExitCode)
			}
		})
	}
}

func TestSandboxContainsForkBombAndTimeout(t *testing.T) {
	d := docker(t)
	start := time.Now()
	r := run(t, d, Spec{
		Script: "bomb() { bomb | bomb & }; bomb; while :; do :; done",
		Limits: Limits{Timeout: 8 * time.Second, Pids: 32},
	})
	if !r.TimedOut {
		t.Fatalf("the fork bomb and busy loop did not time out: %+v", r)
	}
	if el := time.Since(start); el > 40*time.Second {
		t.Fatalf("the fork bomb was not contained in time: %s", el)
	}
	r = run(t, d, Spec{Script: "while :; do :; done", Limits: Limits{Timeout: 2 * time.Second}})
	if !r.TimedOut || r.ExitCode != -1 {
		t.Fatalf("a busy loop with a 2s timeout: %+v", r)
	}
	r = run(t, d, Spec{Script: "exit 3"})
	if r.ExitCode != 3 || r.TimedOut {
		t.Fatalf("exit code: %+v", r)
	}
}
