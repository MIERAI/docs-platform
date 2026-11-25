package comments

import (
	"reflect"
	"testing"

	"github.com/MIERAI/docs-platform/internal/plugin"
)

func TestSegments(t *testing.T) {
	cases := []struct {
		path, src string
		want      []string
	}{
		{"a.go", "x := \"// not\" // yes\n/* b\nc */ d\nurl := \"http://x\"\n", []string{" yes\n", " b\n", "c "}},
		{"a.sh", "echo '#no' # yes\n#top\nx=a#b\n", []string{" yes\n", "top\n"}},
		{"a.yaml", "color: \"#fff\" # note\n", []string{" note\n"}},
		{"a.txt", "# nothing\n", nil},
	}
	for _, c := range cases {
		var got []string
		for _, s := range Segments(c.path, []byte(c.src)) {
			got = append(got, s.Text)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: segments = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestStripKeepsOffsets(t *testing.T) {
	src := []byte("a := 1 // see x.go\nb := 2\n")
	got := Extractor{}.Strip("f.go", src)
	if len(got) != len(src) || string(got[:7]) != "a := 1 " || string(got[19:]) != "b := 2\n" {
		t.Errorf("Strip = %q", got)
	}
}

func TestExtract(t *testing.T) {
	res, err := Extractor{}.Extract(plugin.File{Path: "x/y.go", Content: []byte("// Uses `CONFIG_PATH` and scripts/run.sh (see helper.go).\npackage y\n")})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range res.References {
		got = append(got, string(r.Kind)+" "+r.Text+" "+r.Context)
	}
	want := []string{"env_var CONFIG_PATH comment", "file_path scripts/run.sh comment", "file_path helper.go comment"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("references = %q, want %q", got, want)
	}
}

func FuzzSegments(f *testing.F) {
	for _, s := range []string{"/* a", "\"//\" // x", "'#' # y", "`x` // `z`"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, p := range []string{"a.go", "a.sh", "a.tf"} {
			for _, seg := range Segments(p, []byte(s)) {
				if seg.Start < 0 || seg.End > len(s) || seg.Start > seg.End {
					t.Fatalf("bad segment %+v for %d bytes", seg, len(s))
				}
			}
			if out := (Extractor{}).Strip(p, []byte(s)); len(out) != len(s) {
				t.Fatal("Strip changed the length")
			}
			_, _ = Extractor{}.Extract(plugin.File{Path: p, Content: []byte(s)})
		}
	})
}
