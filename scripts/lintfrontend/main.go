// Command lintfrontend does a fast static check of the bundled
// CSS / HTML templates / JavaScript so make all catches the kinds of
// regressions humans (or LLMs) keep introducing into these files —
// e.g. invalid CSS selector lists with at-rules in them, unterminated
// /* comments */, broken Go template syntax, or JS that fails to parse.
//
// Scope:
//
//	*.html     — parsed via html/template (mirrors how the server
//	             loads them at startup; same error messages).
//	*.css      — tokenized with a tiny stdlib pass:
//	               balanced { } [ ] ( )
//	               unterminated /* */ comments
//	               selector list cannot contain @-rules
//	               unknown @-rules (warn only, allowlist below)
//	*.js       — if `bun` (or `deno`) is on PATH, runs a syntax-only
//	             parse. Otherwise skipped with a one-line notice.
//	             We deliberately do *not* use node.
//
// Usage:  lintfrontend <dir-or-file>...
//
// Exits non-zero on the first reportable error so CI / make fails
// fast. Stdlib-only; safe to invoke via `go run` from the Makefile.
package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lintfrontend <dir-or-file>...")
		os.Exit(2)
	}
	var html, css, js []string
	for _, root := range os.Args[1:] {
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".html":
				html = append(html, path)
			case ".css":
				css = append(css, path)
			case ".js":
				js = append(js, path)
			}
			return nil
		}); err != nil {
			fmt.Fprintln(os.Stderr, "walk:", err)
			os.Exit(1)
		}
	}

	fail := 0
	for _, f := range html {
		if err := checkHTML(f); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fail++
		}
	}
	for _, f := range css {
		if err := checkCSS(f); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fail++
		}
	}
	if jsr := pickJSRunner(); jsr.name == "" {
		if len(js) > 0 {
			fmt.Fprintf(os.Stderr, "lintfrontend: no JS parser found (bun or deno); skipping JS syntax check for %d file(s)\n", len(js))
		}
	} else {
		for _, f := range js {
			if err := jsr.check(f); err != nil {
				fmt.Fprintln(os.Stderr, err)
				fail++
			}
		}
	}
	if fail > 0 {
		os.Exit(1)
	}
	fmt.Printf("✓ frontend lint clean (%d html, %d css, %d js)\n", len(html), len(css), len(js))
}

// checkHTML mirrors what internal/ui does at startup — gives the same
// error a user would hit after `harb serve`.
func checkHTML(path string) error {
	if _, err := template.New(filepath.Base(path)).
		Funcs(template.FuncMap{}).
		ParseFiles(path); err != nil {
		return fmt.Errorf("%s: template parse: %w", path, err)
	}
	return nil
}

// allowedAtRules is the small set of CSS at-rules we use. Anything else
// gets flagged so typos like @meda or @suportss show up.
var allowedAtRules = map[string]bool{
	"media": true, "supports": true, "import": true, "charset": true,
	"keyframes": true, "font-face": true, "page": true,
	"property": true, "container": true, "layer": true,
}

// checkCSS does a minimal tokenizer pass over a CSS file. It is *not*
// a real CSS parser — it just catches the four classes of mistake the
// repo has actually hit:
//
//  1. unbalanced { } [ ] ( )
//  2. unterminated /* … */ comments
//  3. an @-rule appearing inside a comma-separated selector list
//     (e.g.  [data-theme="dark"], @media ... { ... }) — invalid syntax
//     that browsers silently drop, masking real bugs.
//  4. unknown @-rules (typos)
func checkCSS(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(raw)

	// Strip strings + comments and track unterminated comments.
	stripped, err := stripCommentsAndStrings(src, path)
	if err != nil {
		return err
	}

	// Balance check.
	if err := balancedBrackets(stripped, path); err != nil {
		return err
	}

	// Selector-list-with-@-rule: walk top-level (brace-depth 0). A
	// selector ends at '{' or ';'; any '@' encountered after a ',' and
	// before that terminator is the bug we want to catch.
	depth := 0
	hadComma := false
	line := 1
	for i := 0; i < len(stripped); i++ {
		c := stripped[i]
		if c == '\n' {
			line++
		}
		switch c {
		case '{':
			depth++
			hadComma = false
		case '}':
			depth--
			hadComma = false
		case ';':
			if depth == 0 {
				hadComma = false
			}
		case ',':
			if depth == 0 {
				hadComma = true
			}
		case '@':
			if depth == 0 && hadComma {
				return fmt.Errorf("%s:%d: '@' at-rule inside selector list — invalid CSS; browsers will silently drop this block", path, line)
			}
			if depth == 0 {
				// extract @-rule name (letters / dashes after @)
				j := i + 1
				for j < len(stripped) && (isLetter(stripped[j]) || stripped[j] == '-') {
					j++
				}
				name := strings.ToLower(stripped[i+1 : j])
				if name != "" && !allowedAtRules[name] {
					return fmt.Errorf("%s:%d: unknown @-rule '@%s' (typo?)", path, line, name)
				}
			}
		}
	}
	return nil
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// stripCommentsAndStrings returns src with /* … */, // …, '…' and "…"
// runs replaced by spaces of the same length. It is what makes the
// tokenizer tolerable on real CSS.
func stripCommentsAndStrings(src, path string) (string, error) {
	out := []byte(src)
	line := 1
	for i := 0; i < len(out); i++ {
		switch {
		case out[i] == '\n':
			line++
		case i+1 < len(out) && out[i] == '/' && out[i+1] == '*':
			start := line
			end := strings.Index(string(out[i:]), "*/")
			if end < 0 {
				return "", fmt.Errorf("%s:%d: unterminated /* */ comment", path, start)
			}
			for j := i; j < i+end+2; j++ {
				if out[j] != '\n' {
					out[j] = ' '
				}
			}
			// Bump line count for any newlines inside the comment.
			line += strings.Count(string(out[i:i+end+2]), "\n")
			i += end + 1
		case out[i] == '"' || out[i] == '\'':
			q := out[i]
			out[i] = ' '
			for j := i + 1; j < len(out); j++ {
				if out[j] == '\\' && j+1 < len(out) {
					out[j] = ' '
					out[j+1] = ' '
					j++
					continue
				}
				if out[j] == q {
					out[j] = ' '
					i = j
					break
				}
				if out[j] == '\n' {
					return "", fmt.Errorf("%s:%d: unterminated string literal", path, line)
				}
				out[j] = ' '
			}
		}
	}
	return string(out), nil
}

func balancedBrackets(src, path string) error {
	pairs := map[byte]byte{')': '(', ']': '[', '}': '{'}
	var stack []byte
	line := 1
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\n' {
			line++
		}
		switch c {
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != pairs[c] {
				return fmt.Errorf("%s:%d: unbalanced '%c'", path, line, c)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 {
		return fmt.Errorf("%s: unbalanced %s — file is missing %d closer(s)", path, string(stack), len(stack))
	}
	return nil
}

// jsRunner is the chosen JS syntax checker.
type jsRunner struct {
	name string
	run  func(path string) (string, error)
}

// pickJSRunner returns the first available runner from a node-free
// allow-list. We deliberately avoid node — bun and deno are
// single-binary, fast, and don't pull npm into the loop.
func pickJSRunner() jsRunner {
	if p, err := exec.LookPath("bun"); err == nil {
		return jsRunner{name: "bun", run: func(f string) (string, error) {
			// `bun build --no-bundle FILE` parses without executing or
			// writing output. It exits non-zero on syntax errors and
			// prints diagnostics to stderr.
			cmd := exec.Command(p, "build", "--no-bundle", f)
			var sb strings.Builder
			cmd.Stdout = &sb
			cmd.Stderr = &sb
			err := cmd.Run()
			return strings.TrimRight(sb.String(), "\n"), err
		}}
	}
	if p, err := exec.LookPath("deno"); err == nil {
		return jsRunner{name: "deno", run: func(f string) (string, error) {
			cmd := exec.Command(p, "check", "--no-config", "--no-lock", f)
			var sb strings.Builder
			cmd.Stdout = &sb
			cmd.Stderr = &sb
			err := cmd.Run()
			return strings.TrimRight(sb.String(), "\n"), err
		}}
	}
	return jsRunner{}
}

func (r jsRunner) check(path string) error {
	out, err := r.run(path)
	if err != nil {
		return fmt.Errorf("%s: js syntax (%s): %s", path, r.name, out)
	}
	return nil
}

// (no node-based shim — pickJSRunner returns bun or deno.)
