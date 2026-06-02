package resolve

import (
	"net/http"
)

// Chain is an ordered set of resolvers applied as a unit. The zero value
// is a valid empty chain (no-op). Build a chain with NewChain or by
// loading builtins + a sidecar via Load.
type Chain struct {
	resolvers []Resolver
}

// NewChain wraps an ordered slice of resolvers.
func NewChain(rs ...Resolver) Chain {
	return Chain{resolvers: rs}
}

// Names returns the names of the resolvers in the chain, in order. Useful
// for recording which resolvers ran in an observation.
func (c Chain) Names() []string {
	out := make([]string, len(c.resolvers))
	for i, r := range c.resolvers {
		out[i] = r.Name()
	}
	return out
}

// Len reports how many resolvers are in the chain.
func (c Chain) Len() int { return len(c.resolvers) }

// ShapeRequest runs every applicable resolver's ShapeRequest in order.
// meta carries the request URL; response fields are zero at this stage.
func (c Chain) ShapeRequest(req *http.Request, meta FeedMeta) error {
	for _, r := range c.resolvers {
		if !r.Applies(meta) {
			continue
		}
		if err := r.ShapeRequest(req); err != nil {
			return err
		}
	}
	return nil
}

// Transform runs every applicable resolver's Transform in order, threading
// the body through each. A resolver that errors aborts the chain.
func (c Chain) Transform(body []byte, meta FeedMeta) ([]byte, error) {
	for _, r := range c.resolvers {
		if !r.Applies(meta) {
			continue
		}
		out, err := r.Transform(body, meta)
		if err != nil {
			return body, err
		}
		body = out
	}
	return body, nil
}
