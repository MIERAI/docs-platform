package repo

import (
	"testing"
)

const sampleDiff = `diff --git a/cmd/main.go b/cmd/main.go
index 1111111..2222222 100644
--- a/cmd/main.go
+++ b/cmd/main.go
@@ -10 +10 @@ func main() {
-	port := flag.Int("port", 8080, "listen port")
+	port := flag.Int("listen-port", 9090, "listen port")
@@ -20,0 +21,2 @@
+	// new
+	x := 1
diff --git a/old.sh b/old.sh
deleted file mode 100755
--- a/old.sh
+++ /dev/null
@@ -1,2 +0,0 @@
-#!/bin/sh
-echo hi
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

func TestParseUnifiedDiff(t *testing.T) {
	fs := ParseUnifiedDiff(sampleDiff)
	if len(fs) != 3 {
		t.Fatalf("files %d: %+v", len(fs), fs)
	}
	tests := []struct {
		i                 int
		name              string
		added, removed    int
		firstAdd, firstRm int
	}{
		{0, "cmd/main.go", 3, 1, 10, 10},
		{1, "old.sh", 0, 2, 0, 1},
	}
	for _, tt := range tests {
		f := fs[tt.i]
		if f.Name() != tt.name || len(f.Added) != tt.added || len(f.Removed) != tt.removed {
			t.Fatalf("file %d: %+v", tt.i, f)
		}
		if tt.added > 0 && f.Added[0].N != tt.firstAdd || tt.removed > 0 && f.Removed[0].N != tt.firstRm {
			t.Errorf("file %d line numbers: %+v", tt.i, f)
		}
	}
	if fs[0].Added[2].N != 22 || fs[0].Added[2].Text != "\tx := 1" {
		t.Errorf("second hunk %+v", fs[0].Added)
	}
	if fs[1].Path != "" || fs[1].OldPath != "old.sh" || !fs[2].Binary {
		t.Errorf("deleted/binary %+v %+v", fs[1], fs[2])
	}
}

func FuzzParseUnifiedDiff(f *testing.F) {
	f.Add(sampleDiff)
	f.Add("diff --git a b\n@@ -x +y @@\n+a\n")
	f.Add("@@ -1 +1 @@\n+orphan\n")
	f.Fuzz(func(t *testing.T, s string) {
		for _, fd := range ParseUnifiedDiff(s) {
			for _, l := range append(append([]DiffLine{}, fd.Added...), fd.Removed...) {
				if l.N < 0 {
					t.Fatalf("negative line %d", l.N)
				}
			}
		}
	})
}
