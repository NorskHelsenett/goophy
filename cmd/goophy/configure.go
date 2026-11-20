package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
		fmt.Printf("Models from goophy at %s: %d\n", base, len(models))
	} else {
		fmt.Printf("goophy is not answering at %s (%v); using the default model list.\n", base, err)
	}

	verb := "Wrote"
	if *dryRun {
		verb = "Would write"
	}
	report := func(rs []clientcfg.Result) {
		for _, r := range rs {
			switch {
			case !r.Changed:
				fmt.Printf("  %s: already up to date\n", r.Path)
			case r.BackupKept:
				fmt.Printf("  %s %s (original kept in %s)\n", verb, r.Path, filepath.Base(r.Backup))
			case r.Backup != "":
				fmt.Printf("  %s %s (previous: %s)\n", verb, r.Path, filepath.Base(r.Backup))
			default:
				fmt.Printf("  %s %s\n", verb, r.Path)
			}
		}
	}

	failed := false
	for _, n := range names {
		switch n {
		case "opencode":
			path, err := clientcfg.OpencodeConfigPath()
			if err == nil && !explicit && !installed("opencode", filepath.Dir(path)) {
				fmt.Println("opencode: not installed, skipped")
				continue
			}
			fmt.Println("opencode:")
			var r clientcfg.Result
			if err == nil {
				r, err = clientcfg.ConfigureOpencode(path, opts)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
				failed = true
				continue
			}
			report([]clientcfg.Result{r})
			fmt.Printf("  Default model: %s/%s. Keep cloud API keys out of opencode's auth.json.\n", clientcfg.ProviderID, *model)
		case "pi":
			dir, err := clientcfg.PiAgentDir()
			if err == nil && !explicit && !installed("pi", filepath.Dir(dir)) {
				fmt.Println("pi: not installed, skipped")
				continue
			}
			fmt.Println("pi:")
			var rs []clientcfg.Result
			if err == nil {
				rs, err = clientcfg.ConfigurePi(dir, opts)
			}
			report(rs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", err)
				failed = true
				continue
			}
			fmt.Printf("  Default model: %s/%s. Set PI_SKIP_VERSION_CHECK=1 to stop pi's pi.dev version check too.\n", clientcfg.ProviderID, *model)
		}
	}
	if failed {
		os.Exit(1)
	}
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
