package resolve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// builtinResolvers are prepended to every feed's chain. They run in order:
//
//   - strip-control-chars (the former poll.sanitizeXML) is cheap and safe
//     on clean feeds — its fast path returns the body untouched — so it
//     runs unconditionally; and
//   - webflow-to-feed runs as a zero-config auto-instance so a feedless
//     Webflow CMS blog index becomes a synthetic RSS feed with no sidecar.
//     It is a strict no-op unless the response is HTML with Webflow markers
//     that yield at least one item, so it never touches a real feed.
//
// Both are constructed directly rather than via Build because they take
// fixed (or no) params and cannot fail to build; keeping them off the
// error path means a feed poll can never be derailed by builtin
// construction. A sidecar may still add an explicitly-parameterised
// webflow-to-feed instance; the looksLikeXML guard keeps the second pass a
// no-op.
func builtinResolvers() []Resolver {
	scc, _ := newStripControlChars(nil)
	return []Resolver{scc, autoWebflowResolver()}
}

// SidecarPath returns the on-disk path of a feed's resolver sidecar:
// <dir>/resolvers/<feedHash>.json. The fixer process writes this file;
// harb only reads it.
func SidecarPath(dir, feedHash string) string {
	return filepath.Join(dir, "resolvers", feedHash+".json")
}

// LoadSpecs reads a feed's sidecar []Spec. A missing file is not an error
// (returns nil, nil) — the common case is no sidecar at all.
func LoadSpecs(dir, feedHash string) ([]Spec, error) {
	data, err := os.ReadFile(SidecarPath(dir, feedHash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var specs []Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("resolve: parse sidecar %s: %w", feedHash, err)
	}
	return specs, nil
}

// Load builds the active resolver chain for a feed: builtins first, then
// the enabled sidecar specs in file order. It is deliberately resilient —
// the fixer is a separate process and a bad spec must never stop a poll:
//
//   - builtins always build (they are trusted, in-tree);
//   - a sidecar read/parse failure is returned but the builtin-only chain
//     is still usable;
//   - a disabled spec is skipped silently (its provenance lives on disk);
//   - a spec that fails to build (unknown primitive, bad params) is
//     skipped and its error accumulated into the returned error.
//
// Callers should treat a non-nil error as a warning to log, not a reason
// to abort: the returned Chain is always safe to use.
func Load(dir, feedHash string) (Chain, error) {
	var errs []error
	resolvers := builtinResolvers()

	side, err := LoadSpecs(dir, feedHash)
	if err != nil {
		errs = append(errs, err)
	}
	for i, s := range side {
		if s.Disabled {
			continue
		}
		r, berr := Build(s)
		if berr != nil {
			errs = append(errs, fmt.Errorf("sidecar[%d]: %w", i, berr))
			continue
		}
		resolvers = append(resolvers, r)
	}
	return NewChain(resolvers...), errors.Join(errs...)
}
