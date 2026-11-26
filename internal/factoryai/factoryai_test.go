package factoryai

import (
	"strings"
	"testing"
)

func TestEditApply(t *testing.T) {
	src := "a\nconst sla = 300.0\nb\nb\n"
	for _, tc := range []struct {
		e    Edit
		want string
		err  string
	}{
		{Edit{Path: "x", Old: "const sla = 300.0", New: "const sla = 301.0"}, "a\nconst sla = 301.0\nb\nb\n", ""},
		{Edit{Path: "x", Old: "b\n", New: "c\n"}, "", "occurs 2 times"},
		{Edit{Path: "x", Old: "nope", New: "x"}, "", "not in the file"},
		{Edit{Path: "x", Old: "", New: "x"}, "", "quotes no text"},
		{Edit{Path: "x", Old: "a", New: "a"}, "", "changes nothing"},
	} {
		got, err := tc.e.Apply(src)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%+v: err %v, want %q", tc.e, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%+v: %q %v", tc.e, got, err)
		}
	}
}

func TestInsertApply(t *testing.T) {
	src := "  用例:\n    - 说明: 一\n      结果: { a: 1 }\n"
	got, err := Insert{Path: "d", Anchor: "      结果: { a: 1 }", Text: "    - 说明: 二\n      结果: { a: 2 }"}.Apply(src)
	if err != nil || got != src+"    - 说明: 二\n      结果: { a: 2 }\n" {
		t.Errorf("%q %v", got, err)
	}
	if _, err := (Insert{Path: "d", Anchor: "结果", Text: "x"}).Apply(src); err == nil {
		t.Error("a partial line is not an anchor")
	}
	if _, err := (Insert{Path: "d", Anchor: "x\nx", Text: "y"}).Apply("x\nx\n"); err == nil {
		t.Error("an anchor that is not one line of the file is refused")
	}
	if _, err := (Insert{Path: "d", Anchor: "x", Text: "y"}).Apply("x\nx\n"); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("ambiguous anchor: %v", err)
	}
}

func TestRuleBlock(t *testing.T) {
	doc := "业务规则:\n  - 情况: 健康度\n    结果: |\n      没有 → NO_DATA\n      其他 → CRITICAL\n  - 情况: 下一条\n"
	if got := RuleBlock(doc, 2); got != "  - 情况: 健康度\n    结果: |\n      没有 → NO_DATA\n      其他 → CRITICAL" {
		t.Errorf("%q", got)
	}
	if RuleBlock(doc, 99) != "" {
		t.Error("no such line")
	}
}
