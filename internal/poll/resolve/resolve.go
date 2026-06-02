// Package resolve turns per-feed breakage workarounds from hardcoded
// branches in the poll hot path into data: a chain of small, named,
// parameterised Resolvers.
//
// Breakage happens in two places, so a Resolver gets two hooks:
//
//	fetch:  ShapeRequest mutates the outgoing *http.Request
//	          │  HTTP exchange
//	parse:  Transform repairs the response body before gofeed sees it
//
// Resolvers come from two sources behind one interface:
//
//   - builtins, registered in this package and applied to every feed
//     (e.g. strip-control-chars, the former poll.sanitizeXML);
//   - a per-feed sidecar at <data-dir>/resolvers/<feedHash>.json holding
//     a []Spec, written out-of-process by the fixer. harb only reads
//     it.
//
// Every applied resolver is a primitive selected from a fixed vocabulary
// (the registry below) and parameterised by a Spec. The fixer never emits
// code — only Specs — which keeps applied resolvers auditable, revertable
// (Spec.Disabled), and attributable (Spec.Source / Spec.Note).
package resolve

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// FeedMeta is the context a Resolver gates on via Applies. The Transform
// stage sees response fields (ContentType, Status); the ShapeRequest
// stage runs before the exchange, so those are zero there.
type FeedMeta struct {
	URL         string
	ContentType string
	Status      int
}

// Resolver is one feed-fixing step. Implementations are constructed from
// a Spec by a registered factory; they must be stateless and safe to
// reuse across feeds and goroutines.
type Resolver interface {
	// Name is the primitive id (e.g. "strip-control-chars").
	Name() string
	// Applies reports whether this resolver should run for this feed.
	Applies(FeedMeta) bool
	// ShapeRequest may mutate the outgoing request (headers, etc.).
	// Called before the HTTP exchange. A no-op for parse-stage
	// resolvers.
	ShapeRequest(*http.Request) error
	// Transform repairs a response body before parsing. Must return the
	// body unchanged if it has nothing to do. A no-op for fetch-stage
	// resolvers.
	Transform(body []byte, m FeedMeta) ([]byte, error)
}

// Spec is the serialisable description of one applied resolver. Builtins
// synthesise their own; sidecar entries are unmarshalled from JSON.
type Spec struct {
	// Name selects the primitive from the registry.
	Name string `json:"name"`
	// Params parameterise the primitive. Keys are primitive-specific.
	Params map[string]string `json:"params,omitempty"`
	// Source is provenance: "builtin", "user", or "agent".
	Source string `json:"source,omitempty"`
	// Note is a human-readable reason the resolver was added.
	Note string `json:"note,omitempty"`
	// Added is when the resolver was written.
	Added time.Time `json:"added,omitempty"`
	// Disabled is the kill switch: a disabled Spec is loaded (so its
	// provenance survives) but never built into the chain.
	Disabled bool `json:"disabled,omitempty"`
}

// Factory builds a Resolver from a Spec's params.
type Factory func(params map[string]string) (Resolver, error)

var registry = map[string]Factory{}

// Register adds a primitive factory under name. Panics on duplicate —
// registration happens at init time, so a clash is a programming error.
func Register(name string, f Factory) {
	if _, dup := registry[name]; dup {
		panic("resolve: duplicate primitive registration: " + name)
	}
	registry[name] = f
}

// Known returns the registered primitive names, sorted. This is the
// transform vocabulary the fixer is allowed to emit.
func Known() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build constructs a Resolver from a Spec. Disabled specs and unknown
// primitives are an error (callers skip disabled before calling).
func Build(s Spec) (Resolver, error) {
	f, ok := registry[s.Name]
	if !ok {
		return nil, fmt.Errorf("resolve: unknown primitive %q (known: %v)", s.Name, Known())
	}
	r, err := f(s.Params)
	if err != nil {
		return nil, fmt.Errorf("resolve: build %q: %w", s.Name, err)
	}
	return r, nil
}
