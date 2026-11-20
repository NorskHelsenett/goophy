package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NorskHelsenett/goophy/internal/clientcfg"
)

var configureTargets = []string{"opencode", "pi"}

func configureCommand(args []string) {
	fs := flag.NewFlagSet("configure", flag.ExitOnError)
	port := fs.String("port", "", "Port goophy serves on (default $PORT or 22434)")
	model := fs.String("model", "nhn-coder", "Default model for the clients")
	dryRun := fs.Bool("dry-run", false, "Show which files would change without writing them")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: goophy configure [opencode|pi]... [--model M] [--port P] [--dry-run]\n\n")
		fmt.Fprintf(os.Stderr, "Points coding agents at the local goophy. Without a name it configures each\n")
		fmt.Fprintf(os.Stderr, "of opencode and pi that is installed.\n\n")
		fmt.Fprintf(os.Stderr, "  opencode  ~/.config/opencode/opencode.json: provider \"goophy\" as the only\n")
		fmt.Fprintf(os.Stderr, "            enabled provider and the default model; autoupdate off, sharing\n")
		fmt.Fprintf(os.Stderr, "            to opencode.ai disabled.\n")
		fmt.Fprintf(os.Stderr, "  pi        ~/.pi/agent/extensions/goophy.ts (models discovered from goophy)\n")
		fmt.Fprintf(os.Stderr, "            and settings.json: goophy default, telemetry off.\n\n")
		fmt.Fprintf(os.Stderr, "Other settings are kept; a changed file's previous content is saved as .bak.\n\n")
		fs.PrintDefaults()
	}
	flags, names := splitFlagsAndNames(args)
	_ = fs.Parse(flags)
	names = append(names, fs.Args()...)

	explicit := len(names) > 0
	if !explicit {
		names = configureTargets
	}
	for _, n := range names {
		if n != "opencode" && n != "pi" {
			fs.Usage()
			fatal(fmt.Errorf("unknown client %q (want opencode or pi)", n))
		}
	}

	base := "http://localhost:" + resolve(*port, "PORT", "22434") + "/v1"
	opts := clientcfg.Options{BaseURL: base, DefaultModel: *model, DryRun: *dryRun}
	if models, err := clientcfg.DiscoverModels(context.Background(), base); err == nil {
		opts.Models = models
		fmt.Printf("goophy at %s serves %d models.\n", base, len(models))
	} else {
		fmt.Printf("goophy is not running at %s, so only %s and a few defaults are listed.\n", base, *model)
		fmt.Printf("Start 'goophy serve' and run this again to list every model.\n")
	}
	fmt.Printf("Default model: %s/%s\n\n", clientcfg.ProviderID, *model)

	var tips []string
	failed := false
	line := func(client, status string) { fmt.Printf("%-9s %s\n", client, status) }
	detail := func(format string, a ...any) {
		for _, l := range strings.Split(fmt.Sprintf(format, a...), "\n") {
			fmt.Printf("%-9s %s\n", "", l)
		}
	}
	fail := func(client string, err error) {
		line(client, "failed")
		detail("%v", err)
		failed = true
	}

	for _, n := range names {
		switch n {
		case "opencode":
			path, err := clientcfg.OpencodeConfigPath()
			if err != nil {
				fail(n, err)
				continue
			}
			if !explicit && !installed("opencode", filepath.Dir(path)) {
				line(n, "not installed, skipped")
				continue
			}
			r, err := clientcfg.ConfigureOpencode(path, opts)
			if err != nil {
				fail(n, explainConfigErr(err))
				continue
			}
			describe(n, []clientcfg.Result{r}, *dryRun, line, detail)
			if r.Changed {
				tips = append(tips, "opencode now loads only the goophy provider; do not put cloud API keys in its auth.json.")
			}
		case "pi":
			dir, err := clientcfg.PiAgentDir()
			if err != nil {
				fail(n, err)
				continue
			}
			if !explicit && !installed("pi", filepath.Dir(dir)) {
				line(n, "not installed, skipped")
				continue
			}
			rs, err := clientcfg.ConfigurePi(dir, opts)
			if err != nil {
				fail(n, explainConfigErr(err))
				continue
			}
			describe(n, rs, *dryRun, line, detail)
			if os.Getenv("PI_SKIP_VERSION_CHECK") == "" {
				tips = append(tips, "To stop pi checking pi.dev for updates, export PI_SKIP_VERSION_CHECK=1.")
			}
		}
	}
	if len(tips) > 0 {
		fmt.Println()
		for _, t := range tips {
			fmt.Println("Tip: " + t)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// describe prints one client's outcome: a status line, then a line per file
// that changed.
func describe(client string, rs []clientcfg.Result, dryRun bool, line func(string, string), detail func(string, ...any)) {
	var changed []clientcfg.Result
	for _, r := range rs {
		if r.Changed {
			changed = append(changed, r)
		}
	}
	switch {
	case len(changed) == 0:
		line(client, "already set up")
		return
	case dryRun:
		line(client, "would update")
	default:
		line(client, "updated")
	}
	for _, r := range changed {
		switch {
		case r.BackupKept:
			detail("%s (your original is in %s)", tildePath(r.Path), filepath.Base(r.Backup))
		case r.Backup != "":
			detail("%s (backup: %s)", tildePath(r.Path), filepath.Base(r.Backup))
		default:
			detail("%s (new)", tildePath(r.Path))
		}
	}
}

// explainConfigErr adds what to do when a config file cannot be parsed.
func explainConfigErr(err error) error {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.As(err, &se) || errors.As(err, &te) || strings.Contains(err.Error(), "not a JSON object") {
		return fmt.Errorf("%v\nFix the file, or move it aside and run this again; nothing was changed", err)
	}
	return err
}

// tildePath shortens a path in the home directory to ~/...
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}

// installed reports whether a client's binary is on PATH or its config
// directory exists.
func installed(bin, dir string) bool {
	if _, err := exec.LookPath(bin); err == nil {
		return true
	}
	_, err := os.Stat(dir)
	return err == nil
}

// splitFlagsAndNames lets client names come before flags
// ("goophy configure pi --model x"), which flag.Parse alone would stop at.
func splitFlagsAndNames(args []string) (flags, names []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// Value-taking flags given as "--flag value".
			switch a {
			case "-port", "--port", "-model", "--model":
				if i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
			continue
		}
		names = append(names, a)
	}
	return flags, names
}
