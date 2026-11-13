package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/NorskHelsenett/goophy/internal/env"
	"github.com/NorskHelsenett/goophy/internal/gateway"
)

func loginCommand(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	gw := fs.String("gateway", "", "Gateway URL (default $GOOPHY_GATEWAY or "+gateway.DefaultURL+")")
	noBrowser := fs.Bool("no-browser", false, "Do not open a browser; print the link and paste the code it shows")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: goophy login [--gateway URL] [--no-browser]\n\n")
		fmt.Fprintf(os.Stderr, "Signs goophy in to the LLM gateway through the browser. Afterwards\n")
		fmt.Fprintf(os.Stderr, "'goophy serve' proxies to the gateway with your login when API_ENDPOINT\n")
		fmt.Fprintf(os.Stderr, "and API_KEY are not set.\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	base, err := gateway.NormalizeURL(resolve(*gw, "GOOPHY_GATEWAY", gateway.DefaultURL))
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A new login replaces the old one: revoke its token so logins do not
	// pile up as live tokens.
	old, oldErr := gateway.Load()

	c, err := gateway.Login(ctx, gateway.LoginOptions{Gateway: base, NoBrowser: *noBrowser, In: os.Stdin, Out: os.Stdout})
	if err != nil {
		fatal(fmt.Errorf("login failed: %w", err))
	}
	if err := gateway.Save(c); err != nil {
		fatal(fmt.Errorf("could not store the login: %w", err))
	}
	if oldErr == nil && old.Token != c.Token {
		_ = gateway.Logout(ctx, old)
	}
	who := c.Email
	if who == "" {
		who = "your account"
	}
	fmt.Printf("Logged in to %s as %s.\n", c.Gateway, who)
	if c.ExpiresAt != nil {
		fmt.Printf("The login is valid until at least %s; using it can extend that.\n", c.ExpiresAt.Local().Format("2 Jan 2006"))
	}
	fmt.Println("Run 'goophy serve' to proxy to the gateway, or 'goophy usage' to see what you have used.")
}

func logoutCommand(args []string) {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: goophy logout\n\nRevokes goophy's token on the gateway and forgets the login.\n")
	}
	_ = fs.Parse(args)

	c, err := gateway.Load()
	if errors.Is(err, gateway.ErrNotLoggedIn) {
		fmt.Println("Not logged in.")
		return
	}
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	revokeErr := gateway.Logout(ctx, c)
	var se *gateway.StatusError
	if errors.As(revokeErr, &se) && se.Status == 401 {
		revokeErr = nil // already revoked or expired
	}
	if err := gateway.Remove(); err != nil {
		fatal(err)
	}
	if revokeErr != nil {
		fmt.Printf("Logged out locally, but the token could not be revoked on %s: %v\n", c.Gateway, revokeErr)
		fmt.Println("Revoke it under Tokens in the gateway's web interface.")
		os.Exit(1)
	}
	fmt.Printf("Logged out of %s.\n", c.Gateway)
}

func usageCommand(args []string) {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	days := fs.Int("days", 30, "Days to cover, including today (1-365)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: goophy usage [--days N]\n\nShows your usage of the gateway: your own and your teams'.\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if *days < 1 || *days > 365 {
		fatal(errors.New("--days must be between 1 and 365"))
	}

	c, err := gateway.Load()
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u, err := gateway.GetUsage(ctx, c, *days)
	if err != nil {
		var se *gateway.StatusError
		if errors.As(err, &se) && se.Status == 401 {
			fatal(errors.New("the login is no longer valid; run 'goophy login'"))
		}
		fatal(err)
	}
	printUsage(os.Stdout, c, u, *days)
}

func printUsage(w *os.File, c gateway.Credentials, u gateway.Usage, days int) {
	who := c.Email
	if who == "" {
		who = "you"
	}
	period := fmt.Sprintf("the last %d days", days)
	if days == 1 {
		period = "today"
	}
	fmt.Fprintf(w, "Usage on %s for %s, %s\n\n", strings.TrimPrefix(c.Gateway, "https://"), who, period)

	t := u.Totals
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintf(tw, "  Requests\t%s\t", count(t.Requests))
	if t.Errors > 0 {
		fmt.Fprintf(tw, "(%s failed)", count(t.Errors))
	}
	fmt.Fprintln(tw)
	fmt.Fprintf(tw, "  Tokens\t%s\t(%s in, %s out)\n", count(t.TotalTokens), count(t.PromptTokens), count(t.CompletionTokens))
	tw.Flush()

	table := func(title string, rows []gateway.UsageRow) {
		if len(rows) == 0 {
			return
		}
		labels := make([]string, len(rows))
		width := len(title)
		for i, r := range rows {
			labels[i] = r.Label
			if labels[i] == "" {
				labels[i] = r.Key
			}
			width = max(width, len(labels[i]))
		}
		// Names left, numbers right: tabwriter aligns every column one way.
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', tabwriter.AlignRight)
		fmt.Fprintf(tw, "  %-*s\tRequests\tTokens\t\n", width, title)
		for i, r := range rows {
			fmt.Fprintf(tw, "  %-*s\t%s\t%s\t\n", width, labels[i], count(r.Requests), count(r.TotalTokens))
		}
		tw.Flush()
	}
	table("Model", u.ByModel)
	if len(u.ByTeam) > 1 {
		table("Billed to", u.ByTeam)
	}
	fmt.Fprintln(w, "\nQuotas are not enforced yet.")
}

// count formats n compactly: 950, 12.3k, 4.56M.
func count(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}

// gatewayLogin returns the stored login to proxy with when serve is given no
// upstream and no key of its own.
func gatewayLogin(endpointFlag, ollamaEndpointFlag, apiKeyFlag string) (gateway.Credentials, bool) {
	c, err := gateway.Load()
	if err != nil {
		return c, false
	}
	var overrides []string
	for _, o := range []struct{ flag, envKey, name string }{
		{endpointFlag, "API_ENDPOINT", "--api-endpoint"},
		{ollamaEndpointFlag, "OLLAMA_ENDPOINT", "--ollama-endpoint"},
		{apiKeyFlag, "API_KEY", "--api-key"},
	} {
		switch {
		case o.flag != "":
			overrides = append(overrides, o.name)
		case env.GetEnv(o.envKey, "") != "":
			overrides = append(overrides, o.envKey)
		}
	}
	if len(overrides) > 0 {
		// A forgotten .env from before the login is easy to miss: it wins,
		// and its stale key gets every request refused.
		fmt.Fprintf(os.Stderr, "Warning: not using your goophy login for %s, because %s %s set (environment, .env or flag).\n",
			c.Gateway, strings.Join(overrides, ", "), map[bool]string{true: "is", false: "are"}[len(overrides) == 1])
		fmt.Fprintln(os.Stderr, "         Remove them to proxy with the login.")
		return gateway.Credentials{}, false
	}
	if c.Expired() {
		c = recheckLogin(c)
	}
	return c, true
}

// recheckLogin asks the gateway about a login whose stored expiry has
// passed: the gateway extends it as it is used, so it may well be alive.
// A live login gets its new expiry stored; a refused one is fatal.
func recheckLogin(c gateway.Credentials) gateway.Credentials {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	me, err := gateway.WhoAmI(ctx, c)
	var se *gateway.StatusError
	switch {
	case errors.As(err, &se) && se.Status == http.StatusUnauthorized:
		fatal(errors.New("your gateway login has expired; run 'goophy login' (or set API_ENDPOINT to use another upstream)"))
	case err != nil:
		// Cannot tell now; let the gateway decide per request.
		fmt.Fprintf(os.Stderr, "Warning: could not check your gateway login: %v\n", err)
	default:
		c.ExpiresAt = me.Token.ExpiresAt
		_ = gateway.Save(c)
	}
	return c
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
