package ai

import (
	"context"
	"errors"
	"testing"
)

func TestDefaultIsNone(t *testing.T) {
	p, err := New("")
	if err != nil || p.Name() != "none" {
		t.Fatalf("New(\"\") = %v, %v", p, err)
	}
	props, err := p.Propose(context.Background(), Request{Kind: "audit"})
	if props != nil || !errors.Is(err, ErrDisabled) {
		t.Errorf("none provider proposed %v, %v", props, err)
	}
	if _, err := New("magic"); err == nil {
		t.Error("unknown providers must be rejected")
	}
}
