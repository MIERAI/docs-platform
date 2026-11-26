package contract

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Adapter extracts one side's surface from a source.
type Adapter func(ctx context.Context, src *Source) (*Surface, error)

// Adapters are the built-in adapters by name.
var Adapters = map[string]Adapter{
	"factory-design":  FactoryDesign,
	"factory-code":    FactoryCode,
	"openapi-graphql": OpenAPIGraphQL,
	"kotlin-graphql":  KotlinGraphQL,
}

// AdapterNames lists the built-in adapters.
func AdapterNames() []string {
	var n []string
	for k := range Adapters {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Options of one contract run.
type Options struct {
	DocSet, Authority, Generator string
	DesignAdapter, CodeAdapter   string
	Design, Code                 *Source
	// History classifies every item from git history (one or more git log per item).
	History bool
	Workers int
	Now     time.Time
}

// Run extracts both surfaces, diffs them and (optionally) classifies the direction of each
// item from history.
func Run(ctx context.Context, o Options) (*Document, error) {
	da, ok := Adapters[o.DesignAdapter]
	if !ok {
		return nil, fmt.Errorf("contract: unknown design adapter %q (have %v)", o.DesignAdapter, AdapterNames())
	}
	ca, ok := Adapters[o.CodeAdapter]
	if !ok {
		return nil, fmt.Errorf("contract: unknown code adapter %q (have %v)", o.CodeAdapter, AdapterNames())
	}
	switch o.Authority {
	case "", AuthorityDesign, AuthorityCode, AuthorityAsk:
	default:
		return nil, fmt.Errorf("contract: authority %q: want design, code or ask", o.Authority)
	}
	design, err := da(ctx, o.Design)
	if err != nil {
		return nil, err
	}
	design.Side = SideDesign
	code, err := ca(ctx, o.Code)
	if err != nil {
		return nil, err
	}
	code.Side = SideCode
	items := Diff(design, code, DiffOptions{SelectionSubset: o.DesignAdapter == "openapi-graphql"})
	if o.History {
		NewHistory(o.Design, o.Code, o.Now).Classify(ctx, items, o.Workers)
	}
	return Build(o.DocSet, o.Authority, o.Generator, design, code, items, o.History, o.Now), nil
}
