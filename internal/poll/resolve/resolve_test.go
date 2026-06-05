package resolve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestStripControlChars(t *testing.T) {
	r, err := Build(Spec{Name: "strip-control-chars"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, in, want string }{
		{"clean passthrough", "<a>hi there</a>", "<a>hi there</a>"},
		{"keeps tab/lf/cr", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"strips backspace", "a\x08b", "ab"},
		{"strips assorted C0", "\x00\x01\x0b\x0c\x1fX", "X"},
		{"empty", "", ""},
		{"utf16 le bom untouched", "\xff\xfeh\x00i\x00", "\xff\xfeh\x00i\x00"},
		{"utf16 be bom untouched", "\xfe\xff\x00h\x00i", "\xfe\xff\x00h\x00i"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.Transform([]byte(c.in), FeedMeta{})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("Transform(%q)=%q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSetHeader(t *testing.T) {
	r, err := Build(Spec{Name: "set-header", Params: map[string]string{"key": "User-Agent", "value": "custom/1"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.Header.Set("User-Agent", "default")
	if err := r.ShapeRequest(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("User-Agent"); got != "custom/1" {
		t.Fatalf("UA=%q, want custom/1", got)
	}

	// empty value deletes
	del, _ := Build(Spec{Name: "set-header", Params: map[string]string{"key": "X-Gone"}})
	req.Header.Set("X-Gone", "y")
	_ = del.ShapeRequest(req)
	if got := req.Header.Get("X-Gone"); got != "" {
		t.Fatalf("X-Gone=%q, want empty", got)
	}

	if _, err := Build(Spec{Name: "set-header"}); err == nil {
		t.Fatal("want error for missing key")
	}
}

func TestRecodeCharset(t *testing.T) {
	// windows-1252 0x92 is a right single quote U+2019; latin1 would map
	// it to a C1 control. Verify the table path.
	r, err := Build(Spec{Name: "recode-charset", Params: map[string]string{"from": "windows-1252"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Transform([]byte{'i', 't', 0x92, 's'}, FeedMeta{})
	if string(got) != "it\u2019s" {
		t.Fatalf("got %q, want it’s", got)
	}

	// latin1 0xE9 -> é
	l, _ := Build(Spec{Name: "recode-charset", Params: map[string]string{"from": "latin1"}})
	got, _ = l.Transform([]byte{0xE9}, FeedMeta{})
	if string(got) != "é" {
		t.Fatalf("got %q, want é", got)
	}

	if _, err := Build(Spec{Name: "recode-charset", Params: map[string]string{"from": "ebcdic"}}); err == nil {
		t.Fatal("want error for unsupported charset")
	}
}

func TestRegexReplace(t *testing.T) {
	r, err := Build(Spec{Name: "regex-replace", Params: map[string]string{"pattern": `<!DOCTYPE[^>]*>`, "replace": ""}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Transform([]byte(`<!DOCTYPE html><rss>x</rss>`), FeedMeta{})
	if string(got) != "<rss>x</rss>" {
		t.Fatalf("got %q", got)
	}
	if _, err := Build(Spec{Name: "regex-replace", Params: map[string]string{"pattern": "("}}); err == nil {
		t.Fatal("want error for bad pattern")
	}
	if _, err := Build(Spec{Name: "regex-replace"}); err == nil {
		t.Fatal("want error for missing pattern")
	}
}

func TestContentTypeGate(t *testing.T) {
	r, _ := Build(Spec{Name: "regex-replace", Params: map[string]string{
		"pattern": "x", "replace": "y", "content_type_contains": "text/html",
	}})
	if r.Applies(FeedMeta{ContentType: "application/xml"}) {
		t.Fatal("should not apply to xml")
	}
	if !r.Applies(FeedMeta{ContentType: "text/html; charset=utf-8"}) {
		t.Fatal("should apply to html")
	}
}

func TestBuildUnknown(t *testing.T) {
	if _, err := Build(Spec{Name: "no-such-primitive"}); err == nil {
		t.Fatal("want error for unknown primitive")
	}
}

func TestKnownVocabulary(t *testing.T) {
	known := Known()
	want := map[string]bool{
		"strip-control-chars": true, "set-header": true,
		"recode-charset": true, "regex-replace": true,
	}
	for _, k := range known {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("missing primitives from Known(): %v", want)
	}
}

func TestChainOrderAndNames(t *testing.T) {
	strip, _ := Build(Spec{Name: "strip-control-chars"})
	rx, _ := Build(Spec{Name: "regex-replace", Params: map[string]string{"pattern": "a", "replace": "b"}})
	c := NewChain(strip, rx)
	if got := c.Names(); len(got) != 2 || got[0] != "strip-control-chars" || got[1] != "regex-replace" {
		t.Fatalf("names=%v", got)
	}
	// strip removes \x00, then regex turns a->b
	out, err := c.Transform([]byte("a\x00a"), FeedMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "bb" {
		t.Fatalf("got %q, want bb", out)
	}
}

func TestLoadBuiltinsOnly(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir, "deadbeef")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := c.Names(); len(got) != 2 || got[0] != "strip-control-chars" || got[1] != "webflow-to-feed" {
		t.Fatalf("want builtin-only chain [strip-control-chars webflow-to-feed], got %v", got)
	}
}

func TestLoadSidecarMergesAfterBuiltins(t *testing.T) {
	dir := t.TempDir()
	fh := "abc123"
	specs := []Spec{
		{Name: "set-header", Params: map[string]string{"key": "User-Agent", "value": "x"}, Source: "agent", Note: "CDN tarpit"},
		{Name: "regex-replace", Params: map[string]string{"pattern": "a", "replace": "b"}, Disabled: true},
	}
	writeSidecar(t, dir, fh, specs)

	c, err := Load(dir, fh)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// builtins (strip-control-chars, webflow-to-feed) + enabled set-header;
	// disabled regex-replace skipped.
	if got := c.Names(); len(got) != 3 || got[0] != "strip-control-chars" || got[1] != "webflow-to-feed" || got[2] != "set-header" {
		t.Fatalf("chain=%v", got)
	}
}

func TestLoadBadSpecIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	fh := "bad"
	writeSidecar(t, dir, fh, []Spec{
		{Name: "no-such-primitive"},
		{Name: "set-header", Params: map[string]string{"key": "X", "value": "1"}},
	})
	c, err := Load(dir, fh)
	if err == nil {
		t.Fatal("want non-nil (warning) error for bad spec")
	}
	// chain still usable: builtins + the good set-header.
	if got := c.Names(); len(got) != 3 || got[2] != "set-header" {
		t.Fatalf("chain=%v", got)
	}
}

func TestLoadCorruptSidecarKeepsBuiltins(t *testing.T) {
	dir := t.TempDir()
	fh := "corrupt"
	path := SidecarPath(dir, fh)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir, fh)
	if err == nil {
		t.Fatal("want parse error")
	}
	if c.Len() != 2 {
		t.Fatalf("want builtin-only chain, got %v", c.Names())
	}
}

func writeSidecar(t *testing.T, dir, fh string, specs []Spec) {
	t.Helper()
	path := SidecarPath(dir, fh)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// errResolver is a test double that errors in whichever hook is requested,
// exercising the chain's error-propagation paths.
type errResolver struct {
	applies            bool
	shapeErr, transErr error
}

func (e errResolver) Name() string          { return "err-resolver" }
func (e errResolver) Applies(FeedMeta) bool { return e.applies }
func (e errResolver) ShapeRequest(*http.Request) error {
	return e.shapeErr
}
func (e errResolver) Transform(b []byte, _ FeedMeta) ([]byte, error) {
	return b, e.transErr
}

func TestChainShapeRequest(t *testing.T) {
	sh, _ := Build(Spec{Name: "set-header", Params: map[string]string{"key": "X-A", "value": "1"}})
	// A resolver that does not apply must be skipped.
	skip := errResolver{applies: false, shapeErr: errSentinel}
	c := NewChain(sh, skip)

	req, _ := http.NewRequest("GET", "http://x", nil)
	if err := c.ShapeRequest(req, FeedMeta{URL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-A") != "1" {
		t.Fatal("set-header in chain did not run")
	}

	// An applicable resolver that errors aborts the chain.
	bad := NewChain(errResolver{applies: true, shapeErr: errSentinel})
	if err := bad.ShapeRequest(req, FeedMeta{}); err != errSentinel {
		t.Fatalf("err=%v, want sentinel", err)
	}
}

func TestChainTransformError(t *testing.T) {
	// Skipped (non-applicable) erroring resolver leaves body untouched.
	c := NewChain(errResolver{applies: false, transErr: errSentinel})
	out, err := c.Transform([]byte("x"), FeedMeta{})
	if err != nil || string(out) != "x" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	// Applicable erroring resolver aborts and returns the input body.
	bad := NewChain(errResolver{applies: true, transErr: errSentinel})
	out, err = bad.Transform([]byte("x"), FeedMeta{})
	if err != errSentinel || string(out) != "x" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBaseNoops(t *testing.T) {
	// strip-control-chars is a transform primitive; its ShapeRequest is the
	// base no-op.
	strip, _ := Build(Spec{Name: "strip-control-chars"})
	req, _ := http.NewRequest("GET", "http://x", nil)
	if err := strip.ShapeRequest(req); err != nil {
		t.Fatal(err)
	}
	// set-header is a fetch primitive; its Transform is the base no-op.
	sh, _ := Build(Spec{Name: "set-header", Params: map[string]string{"key": "X", "value": "y"}})
	out, err := sh.Transform([]byte("body"), FeedMeta{})
	if err != nil || string(out) != "body" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	Register("set-header", func(map[string]string) (Resolver, error) { return nil, nil })
}

func TestLoadSpecsReadError(t *testing.T) {
	dir := t.TempDir()
	fh := "isdir"
	// Make the sidecar path a directory so ReadFile fails with a non
	// NotExist error.
	if err := os.MkdirAll(SidecarPath(dir, fh), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpecs(dir, fh); err == nil {
		t.Fatal("want read error")
	}
	// Load surfaces it as a warning but still yields a builtin-only chain.
	c, err := Load(dir, fh)
	if err == nil {
		t.Fatal("want warning error")
	}
	if c.Len() != 2 {
		t.Fatalf("want builtin-only chain, got %v", c.Names())
	}
}

var errSentinel = errorString("boom")

type errorString string

func (e errorString) Error() string { return string(e) }
