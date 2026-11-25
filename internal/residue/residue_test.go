package residue

import "testing"

func TestCheck(t *testing.T) {
	c := New([]string{"Secret Project", "acme-internal"})
	src := "intro\nthe secret project is here\nnot acme-internalized; acme-internal.example yes\nSecret Project again"
	got := c.Check("README.md", []byte(src))
	if len(got) != 2 {
		t.Fatalf("issues = %+v", got)
	}
	if len(got[0].Locations) != 2 || got[0].Locations[0].Line != 2 || got[0].Locations[1].Line != 4 {
		t.Errorf("term 1 locations = %+v", got[0].Locations)
	}
	if len(got[1].Locations) != 1 || got[1].Locations[0].Line != 3 {
		t.Errorf("term 2 locations = %+v", got[1].Locations)
	}
	if New(nil).Check("x", []byte("anything")) != nil || !New(nil).Empty() {
		t.Error("an empty denylist checks nothing")
	}
}
