// Command harborrs is the single-binary entry point.
//
// Subcommands:
//
//	harborrs serve [-data DIR] [-config FILE]
//	harborrs import [-data DIR] OPML
//	harborrs poll-once [-data DIR]
//	harborrs hashpass PASSWORD
//	harborrs version
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kfet/harborrs"
	"github.com/kfet/harborrs/internal/auth"
	"github.com/kfet/harborrs/internal/config"
	"github.com/kfet/harborrs/internal/poll"
	"github.com/kfet/harborrs/internal/reader"
	"github.com/kfet/harborrs/internal/store"
	uipkg "github.com/kfet/harborrs/internal/ui"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, harborrs.Version)
		return 0
	case "serve":
		return cmdServe(rest, stdout, stderr)
	case "import":
		return cmdImport(rest, stdout, stderr)
	case "poll-once":
		return cmdPollOnce(rest, stdout, stderr)
	case "hashpass":
		return cmdHashpass(rest, stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n%s\n", cmd, usage)
		return 2
	}
}

const usage = `harborrs — single-binary RSS server

usage:
  harborrs serve     [-data DIR] [-config FILE]
  harborrs import    [-data DIR] OPMLFILE
  harborrs poll-once [-data DIR]
  harborrs hashpass  PASSWORD
  harborrs version`

func defaultDataDir() string {
	if v := os.Getenv("HARBORRS_DATA"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "harborrs")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./harborrs-data"
	}
	return filepath.Join(home, ".local", "share", "harborrs")
}

// commonFlags parses -data + -config and returns the resolved values.
func commonFlags(fs *flag.FlagSet) (*string, *string) {
	data := fs.String("data", defaultDataDir(), "data directory")
	cfg := fs.String("config", "", "path to config.json (defaults to <data>/config.json)")
	return data, cfg
}

func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataPtr, cfgPtr := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	data := *dataPtr
	cfgPath := *cfgPtr
	if cfgPath == "" {
		cfgPath = filepath.Join(data, "config.json")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return 1
	}
	if cfg.Auth.PasswordHash == "" {
		fmt.Fprintln(stderr, "no auth.password_hash set in config; generate one with `harborrs hashpass <password>`")
		return 1
	}
	st, err := store.Open(data)
	if err != nil {
		fmt.Fprintln(stderr, "store:", err)
		return 1
	}
	as, err := auth.OpenStore(filepath.Join(data, "tokens.json"), cfg.Auth)
	if err != nil {
		fmt.Fprintln(stderr, "auth:", err)
		return 1
	}
	op := config.NewFileOPML(data)
	mux := http.NewServeMux()
	readSrv := reader.New(st, as, op)
	readHandler := readSrv.Routes(mux)
	uiSrv, err := uipkg.New(st, as, op, cfg.UI.Theme, data)
	if err != nil {
		fmt.Fprintln(stderr, "ui:", err)
		return 1
	}
	uiSrv.Secure = cfg.UI.Secure
	uiSrv.Routes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusSeeOther)
			return
		}
		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:         cfg.Listen,
		Handler:      readHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	poller := poll.New(st)
	feeds := func() []string {
		o, err := op.Load()
		if err != nil {
			return nil
		}
		urls := make([]string, 0, len(o.Feeds))
		for _, f := range o.Feeds {
			urls = append(urls, f.XMLURL)
		}
		return urls
	}
	go func() { _ = poller.Run(ctx, feeds, 30*time.Second) }()

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "harborrs listening on %s\n", cfg.Listen)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutdown)
		return 0
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, "serve:", err)
			return 1
		}
		return 0
	}
}

func cmdImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataPtr, _ := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: harborrs import [-data DIR] OPMLFILE")
		return 2
	}
	src := fs.Arg(0)
	data := *dataPtr
	inc, err := store.ReadOPML(src)
	if err != nil {
		fmt.Fprintln(stderr, "read:", err)
		return 1
	}
	op := config.NewFileOPML(data)
	cur, err := op.Load()
	if err != nil {
		fmt.Fprintln(stderr, "load:", err)
		return 1
	}
	added := 0
	for _, f := range inc.Feeds {
		if cur.Add(f) {
			added++
		}
	}
	if err := op.Save(cur); err != nil {
		fmt.Fprintln(stderr, "save:", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported %d new feed(s), %d total\n", added, len(cur.Feeds))
	return 0
}

func cmdPollOnce(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("poll-once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataPtr, _ := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	data := *dataPtr
	st, err := store.Open(data)
	if err != nil {
		fmt.Fprintln(stderr, "store:", err)
		return 1
	}
	op := config.NewFileOPML(data)
	o, err := op.Load()
	if err != nil {
		fmt.Fprintln(stderr, "opml:", err)
		return 1
	}
	p := poll.New(st)
	total := 0
	for _, f := range o.Feeds {
		// Reset NextFetch so this command always polls.
		fh := store.FeedHash(f.XMLURL)
		fs, _ := st.LoadFeedState(fh)
		fs.NextFetch = time.Time{}
		_ = st.SaveFeedState(fh, fs)
		n, err := p.Poll(context.Background(), f.XMLURL)
		if err != nil {
			fmt.Fprintf(stderr, "poll %s: %v\n", f.XMLURL, err)
			continue
		}
		total += n
		fmt.Fprintf(stdout, "%s: %d new\n", f.XMLURL, n)
	}
	fmt.Fprintf(stdout, "total new entries: %d\n", total)
	return 0
}

func cmdHashpass(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: harborrs hashpass PASSWORD")
		return 2
	}
	h, err := auth.HashPassword(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, h)
	return 0
}
