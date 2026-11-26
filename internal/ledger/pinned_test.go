package ledger

import (
	"context"
	"testing"
)

// A pinned file (the producer's footprint) re-opens a claim when it changes, even when the
// claim rests on no evidence; the claim's own document is never pinned.
func TestPinnedFootprint(t *testing.T) {
	g := newRepo(t)
	b, err := NewBuilder(context.Background(), g.dir, "demo", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	c := Claim{ID: "x", Repo: "demo", Path: "README.md", Line: 3, Text: "The service listens on port 9090.", Category: "unverifiable_in_repo"}
	b.Pin(&c, []string{"config.yaml", "./config.yaml", "README.md", "no/such/file.go"})
	if len(c.Evidence.Pinned) != 1 || c.Evidence.Pinned[0].Path != "config.yaml" || c.Evidence.Pinned[0].Blob == "" {
		t.Fatalf("pinned %+v, want config.yaml with its blob only", c.Evidence.Pinned)
	}
	if !c.Evidence.Empty() {
		t.Error("a pin is a footprint, not evidence")
	}
	doc := func(string) (string, bool) { return "", false }
	if st := Invalidate(&c, &Changes{Paths: map[string]byte{"other.go": 'M'}}, doc); st.Status != Recheck {
		t.Errorf("unrelated change: %s, want recheck (no evidence)", st.Status)
	}
	st := Invalidate(&c, &Changes{Paths: map[string]byte{"config.yaml": 'M'}}, doc)
	if st.Status != Stale || st.Reasons[0].Kind != ReasonPinnedChanged {
		t.Errorf("pinned file changed: %+v, want stale pinned_changed", st)
	}
	if st := Invalidate(&c, &Changes{Paths: map[string]byte{"config.yaml": 'D'}}, doc); st.Status != Stale {
		t.Errorf("pinned file removed: %s, want stale", st.Status)
	}
}
