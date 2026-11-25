package claim

import (
	"reflect"
	"slices"
	"testing"
)

func TestValues(t *testing.T) {
	cases := []struct {
		in   string
		kind ValueKind
		num  float64
		text string
	}{
		{"keeps logs for 3 days", Duration, 3 * 86400, ""},
		{"retention: 30d", Duration, 30 * 86400, ""},
		{"保留 14 天", Duration, 14 * 86400, ""},
		{"timeout of 500ms", Duration, 0.5, ""},
		{"a 20MB image", Size, 20e6, ""},
		{"limit 512Mi", Size, 512 << 20, ""},
		{"at most 2% of samples", Percent, 0.02, ""},
		{"runs daily at 09:00 JST", Clock, 9 * 60, ""},
		{"every 2 minutes", Every, 120, ""},
		{"每 5 分钟", Every, 300, ""},
		{"nightly build", Every, 86400, ""},
		{"Tracing (v0.6.1)", Version, 0, "0.6.1"},
		{"golang:1.27.4-alpine", Version, 0, "1.27.4-alpine"},
		{"make clean deletes six images", Count, 6, ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			var got *Value
			for _, v := range Values(c.in) {
				if v.Kind == c.kind {
					got = &v
					break
				}
			}
			if got == nil {
				t.Fatalf("no %s value in %q: %+v", c.kind, c.in, Values(c.in))
			}
			if got.Num != c.num || c.text != "" && got.Text != c.text {
				t.Errorf("got %+v, want num %v text %q", *got, c.num, c.text)
			}
		})
	}
}

func TestNotValues(t *testing.T) {
	for _, in := range []string{"ones and twos", "the 1.5 ratio", "10.0.0.1 is private", "one s", "v1"} {
		for _, v := range Values(in) {
			if v.Kind != Count {
				t.Errorf("%q: unexpected %+v", in, v)
			}
		}
	}
}

func TestParseCron(t *testing.T) {
	cases := map[string]bool{"0 9 * * 1-5": true, "*/15 * * * *": true, "a b c d e": false, "0 9 * *": false}
	for in, want := range cases {
		if _, got := ParseCron(in); got != want {
			t.Errorf("ParseCron(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStatesRolesLists(t *testing.T) {
	cases := []struct {
		in     string
		states []string
		roles  []string
		items  []string
		status bool
	}{
		{"状态: 实现中 · 2026-11-24", []string{StateInProgress}, nil, nil, true},
		{"> 版本 v0.1 · 状态：**评审中**", []string{StateInProgress}, nil, nil, true},
		{"Group by is planned for R3.", []string{StatePlanned}, nil, nil, false},
		{"还没有 EXPLAIN 闸门", []string{StateNotYet}, nil, nil, false},
		{"Humans write `design/` and `db/schema/`.", nil, []string{RoleHuman}, []string{"design", "db/schema"}, false},
		{"usage: check|prepare|prompt|guard", nil, nil, []string{"check", "prepare", "prompt", "guard"}, false},
		{"jobs: `test`, `lint` and `docs`", nil, nil, []string{"test", "lint", "docs"}, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			cl := fromText(c.in, spansOf(c.in))
			if !slices.Equal(cl.States, c.states) || !slices.Equal(cl.Roles, c.roles) || cl.Status != c.status {
				t.Errorf("states %v roles %v status %v", cl.States, cl.Roles, cl.Status)
			}
			var items []string
			if len(cl.Lists) > 0 {
				items = cl.Lists[0].Items
			}
			if !reflect.DeepEqual(items, c.items) {
				t.Errorf("list %v, want %v", items, c.items)
			}
		})
	}
}

func TestMarkdown(t *testing.T) {
	src := "# Title\n\nStatus: draft\n\n```yaml\nretention: 3d\n```\n\nApps:\n\n- `alpha`: one\n- `beta`: two\n\n" +
		"| key | value |\n|---|---|\n| retention | 30 days |\n"
	cls := Markdown("README.md", []byte(src))
	lines := map[int]bool{}
	for _, c := range cls {
		lines[c.Line] = true
		if c.Section != "Title" {
			t.Errorf("line %d: section %q", c.Line, c.Section)
		}
	}
	for _, want := range []int{3, 9, 16} {
		if !lines[want] {
			t.Errorf("no claim on line %d: %+v", want, cls)
		}
	}
	if lines[6] {
		t.Error("code blocks are not claims")
	}
}

func TestComments(t *testing.T) {
	cls := Comments("main.go", []Line{{N: 3, Text: "// Tracing (v0.6.1): spans are sampled at 10%."}})
	if len(cls) != 1 || !cls[0].Comment || len(cls[0].Values) != 2 {
		t.Fatalf("got %+v", cls)
	}
}

func FuzzMarkdown(f *testing.F) {
	f.Add([]byte("# T\n\n状态: 实现中\n- `a`\n- `b`\n| x | 3 days |\n```\ncode\n```\nevery 5 minutes at 09:00 UTC v1.2.3 `0 9 * * *`"))
	f.Add([]byte("`unterminated ` span `x` | y | 1.2.3.4 50% 12Mi"))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, c := range Markdown("f.md", b) {
			if c.Line < 1 || !c.Checkable() {
				t.Fatalf("bad claim %+v", c)
			}
		}
	})
}

func FuzzValues(f *testing.F) {
	f.Add("3 days, 20MB, 2%, 09:00 JST, every 2 minutes, v0.1.0, six images")
	f.Fuzz(func(t *testing.T, s string) {
		for _, v := range Values(s) {
			if v.Col < 0 || v.Col > len(s) {
				t.Fatalf("bad offset %+v", v)
			}
		}
		_, _ = ParseCron(s)
		_, _ = ParseVersion(s)
	})
}
