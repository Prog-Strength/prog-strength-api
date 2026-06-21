// Command memctl is a thin operator CLI over the agent vector-memory admin
// endpoints. It wraps GET /admin/memories (list) and POST
// /admin/memories/search (search) so tuning the retrieval threshold and
// eyeballing the index is a one-liner instead of a hand-rolled curl with a JWT.
//
// Stdlib only by design: the SOW says start with flag/net/http and promote to
// cobra only if the surface grows. Two subcommands, dispatched on os.Args[1].
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// defaultAPI is the fallback base URL when neither --api nor MEMCTL_API is set.
const defaultAPI = "http://localhost:8080"

// errExit1 / errExit2 propagate the desired process exit code up to main, which
// maps any non-nil error to exit(1) — except errExit2, which it remaps to
// exit(2) for usage/parse failures. The flag package already prints those.
var (
	errExit1 = errors.New("request failed")
	errExit2 = errors.New("usage error")
)

// newFlagSet builds a per-subcommand FlagSet with ContinueOnError so a parse
// failure returns to us (we translate it to exit 2) instead of os.Exit-ing
// from inside flag.
func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}

// httpClient carries a timeout so a hung server doesn't hang the operator's
// shell; both subcommands share it.
var httpClient = &http.Client{Timeout: 30 * time.Second}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "list":
		err = runList(os.Args[2:], httpClient, os.Stdout)
	case "search":
		err = runSearch(os.Args[2:], httpClient, os.Stdout)
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		if errors.Is(err, errExit2) {
			// Usage/parse error: flag already printed details to stderr.
			os.Exit(2)
		}
		if !errors.Is(err, errExit1) {
			// errExit1 was already reported (status + body) by doRequest.
			fmt.Fprintln(os.Stderr, "memctl:", err)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: memctl <list|search> [flags]")
	fmt.Fprintln(os.Stderr, "  list    [--user <id>] [--limit N] [--offset N]")
	fmt.Fprintln(os.Stderr, "  search  --query \"...\" [--user <id>] [--k N] [--threshold F]")
	fmt.Fprintln(os.Stderr, "  shared  [--api <url>] [--token <jwt>]  (env MEMCTL_API, MEMCTL_TOKEN)")
}

// listOptions holds the parsed `list` flags. base/token are shared.
type listOptions struct {
	base   string
	token  string
	user   string
	limit  int
	offset int
}

// runList parses the `list` subcommand and GETs /admin/memories. A non-nil
// returned error is a flag/parse/exit-2 failure (already printed for flag
// errors); transport/status failures come back from doRequest.
func runList(args []string, client *http.Client, out io.Writer) error {
	fs := newFlagSet("list")
	var (
		base   = fs.String("api", envOr("MEMCTL_API", defaultAPI), "base API URL")
		token  = fs.String("token", os.Getenv("MEMCTL_TOKEN"), "admin JWT")
		user   = fs.String("user", "", "filter by user id")
		limit  = fs.Int("limit", 100, "max rows")
		offset = fs.Int("offset", 0, "row offset")
	)
	if err := fs.Parse(args); err != nil {
		// flag.ExitOnError prints + exits; ContinueOnError returns here.
		return errExit2
	}

	opts := listOptions{
		base:   *base,
		token:  *token,
		user:   *user,
		limit:  *limit,
		offset: *offset,
	}
	return doList(context.Background(), opts, client, out)
}

func doList(ctx context.Context, opts listOptions, client *http.Client, out io.Writer) error {
	endpoint, err := url.Parse(strings.TrimRight(opts.base, "/") + "/admin/memories")
	if err != nil {
		return fmt.Errorf("bad api url: %w", err)
	}

	// limit/offset always carry their flag defaults — the server clamps them —
	// but user_id is only meaningful when set, so omit it otherwise.
	q := endpoint.Query()
	q.Set("limit", fmt.Sprintf("%d", opts.limit))
	q.Set("offset", fmt.Sprintf("%d", opts.offset))
	if opts.user != "" {
		q.Set("user_id", opts.user)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	setAuth(req, opts.token)
	return doRequest(client, req, out)
}

// searchOptions holds the parsed `search` flags. k/threshold are pointers so a
// nil value means "the operator did not set the flag" (omit from the body),
// distinct from an explicit zero — an explicit --threshold 0 must be sent
// because 0 means "full sweep, no cap" on the server.
type searchOptions struct {
	base      string
	token     string
	query     string
	user      string
	k         *int
	threshold *float64
}

func runSearch(args []string, client *http.Client, out io.Writer) error {
	fs := newFlagSet("search")
	var (
		base      = fs.String("api", envOr("MEMCTL_API", defaultAPI), "base API URL")
		token     = fs.String("token", os.Getenv("MEMCTL_TOKEN"), "admin JWT")
		query     = fs.String("query", "", "search query (required)")
		user      = fs.String("user", "", "user id")
		k         = fs.Int("k", 0, "max matches (omitted unless set)")
		threshold = fs.Float64("threshold", 0, "distance cap (omitted unless set)")
	)
	if err := fs.Parse(args); err != nil {
		return errExit2
	}
	if *query == "" {
		fmt.Fprintln(os.Stderr, "memctl search: --query is required")
		return errExit2
	}

	opts := searchOptions{
		base:  *base,
		token: *token,
		query: *query,
		user:  *user,
	}
	// Visit reports only the flags the operator actually set. We use it instead
	// of comparing against a sentinel default so an explicit --threshold 0 (full
	// sweep) is forwarded while an unset --threshold is omitted (server applies
	// its configured default cap). Same reasoning for --k.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "k":
			v := *k
			opts.k = &v
		case "threshold":
			v := *threshold
			opts.threshold = &v
		}
	})

	return doSearch(context.Background(), opts, client, out)
}

func doSearch(ctx context.Context, opts searchOptions, client *http.Client, out io.Writer) error {
	// map[string]any so only the set fields are marshaled into the body.
	body := map[string]any{"query": opts.query}
	if opts.user != "" {
		body["user_id"] = opts.user
	}
	if opts.k != nil {
		body["k"] = *opts.k
	}
	if opts.threshold != nil {
		body["threshold"] = *opts.threshold
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(opts.base, "/") + "/admin/memories/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req, opts.token)
	return doRequest(client, req, out)
}

// doRequest executes req, pretty-prints a 2xx JSON body to out, and on a
// non-2xx status prints status + body to stderr and returns an exit-1 error.
func doRequest(client *http.Client, req *http.Request, out io.Writer) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "memctl: %s\n%s\n", resp.Status, raw)
		return errExit1
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		// Not JSON (unexpected for these endpoints) — echo verbatim.
		_, _ = out.Write(raw)
		return nil
	}
	_, _ = pretty.WriteTo(out)
	_, _ = io.WriteString(out, "\n")
	return nil
}

// setAuth attaches the admin bearer token. The endpoints are admin-gated, so a
// missing token will produce a 401/403 from the server — we still send the
// request (and surface that status) rather than failing locally.
func setAuth(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
