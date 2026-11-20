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

	"github.com/charmbracelet/lipgloss"

	"github.com/NorskHelsenett/goophy/internal/clientcfg"
)

var configureTargets = []string{"opencode", "pi"}

func configureCommand(args []string) {
	fs := flag.NewFlagSet("configure", flag.ExitOnError)
	port := fs.String("port", "", "Port goophy serves on (default $PORT or 22434)")
	model := fs.String("model", "nhn-coder", "Default model for the clients")
	dryRun := fs.Bool("dry-run", false, "Show which files would change without writing them")
	printOnly := fs.Bool("print", false, "Print the settings to add by hand instead of writing them")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: goophy configure [opencode|pi]... [--model M] [--port P] [--dry-run] [--print]\n\n")
		fmt.Fprintf(os.Stderr, "Points coding agents at the local goophy. Without a name it configures each\n")
		fmt.Fprintf(os.Stderr, "of opencode and pi that is installed.\n\n")
		fmt.Fprintf(os.Stderr, "  opencode  ~/.config/opencode/opencode.json: provider \"goophy\" as the only\n")
		fmt.Fprintf(os.Stderr, "            enabled provider and the default model; autoupdate off, sharing\n")
		fmt.Fprintf(os.Stderr, "            to opencode.ai disabled.\n")
		fmt.Fprintf(os.Stderr, "  pi        ~/.pi/agent/extensions/goophy.ts (models discovered from goophy)\n")
		fmt.Fprintf(os.Stderr, "            and settings.json: goophy default, telemetry off.\n\n")
		fmt.Fprintf(os.Stderr, "Other settings are kept; a changed file's previous content is saved as .bak.\n")
		fmt.Fprintf(os.Stderr, "With --print nothing is written: it prints what to put in each file.\n\n")
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
	ui := newConfigureUI()
	if *printOnly {
		// Only the settings go to stdout, so they can be redirected to a file.
		if models, err := clientcfg.DiscoverModels(context.Background(), base); err == nil {
			opts.Models = models
		} else {
			fmt.Fprintf(os.Stderr, "goophy is not running at %s; listing %s and a few defaults.\n\n", base, *model)
		}
		printConfigs(names, opts)
		return
	}

	if models, err := clientcfg.DiscoverModels(context.Background(), base); err == nil {
		opts.Models = models
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		ui.field("goophy", ui.ok.Render("running")+ui.dim.Render(" at "+base))
		ui.field("models", strings.Join(ids, ", "))
	} else {
		ui.field("goophy", ui.warn.Render("not running")+ui.dim.Render(" at "+base))
		ui.field("", ui.dim.Render("Listing "+*model+" and a few defaults. Start 'goophy serve' and run this again for every model."))
	}
	ui.field("default", ui.bold.Render(clientcfg.ProviderID+"/"+*model))
	if *dryRun {
		ui.field("dry run", "nothing is written")
	}
	fmt.Println()

	var tips []string
	failed := false
	for _, n := range names {
		switch n {
		case "opencode":
			path, err := clientcfg.OpencodeConfigPath()
			if err == nil && !explicit && !installed("opencode", filepath.Dir(path)) {
				ui.notFound(n)
				continue
			}
			var r clientcfg.Result
			if err == nil {
				r, err = clientcfg.ConfigureOpencode(path, opts)
			}
			if err != nil {
				ui.fail(n, explainConfigErr(err))
				failed = true
				continue
			}
			ui.client(n, filepath.Dir(path), []clientcfg.Result{r}, *dryRun)
			ui.warnings(clientcfg.OpencodeWarnings(path))
			if r.Changed && !*dryRun {
				tips = append(tips, "opencode now loads only the goophy provider; keep cloud API keys out of its auth.json.")
			}
		case "pi":
			dir, err := clientcfg.PiAgentDir()
			if err == nil && !explicit && !installed("pi", filepath.Dir(dir)) {
				ui.notFound(n)
				continue
			}
			var rs []clientcfg.Result
			if err == nil {
				rs, err = clientcfg.ConfigurePi(dir, opts)
			}
			if err != nil {
				ui.fail(n, explainConfigErr(err))
				failed = true
				continue
			}
			ui.client(n, dir, rs, *dryRun)
			ui.warnings(clientcfg.PiWarnings(dir))
			if os.Getenv("PI_SKIP_VERSION_CHECK") == "" {
				tips = append(tips, "export PI_SKIP_VERSION_CHECK=1 to stop pi checking pi.dev for updates.")
			}
		}
	}
	if len(tips) > 0 {
		fmt.Println()
		for _, t := range tips {
			fmt.Println(ui.dim.Render("tip  ") + t)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// configureUI prints configure's report: coloured in a terminal, plain when
// piped or with NO_COLOR set (lipgloss detects both).
type configureUI struct {
	ok, warn, bad, dim, bold, key lipgloss.Style
}

func newConfigureUI() configureUI {
	s := lipgloss.NewStyle()
	return configureUI{
		ok:   s.Foreground(lipgloss.Color("2")),
		warn: s.Foreground(lipgloss.Color("3")),
		bad:  s.Foreground(lipgloss.Color("1")),
		dim:  s.Faint(true),
		bold: s.Bold(true),
		key:  s.Foreground(lipgloss.Color("6")),
	}
}

// field prints a header line: "label    value".
func (u configureUI) field(label, value string) {
	fmt.Printf("%s %s\n", u.dim.Render(fmt.Sprintf("%-8s", label)), value)
}

// head prints a client's status line.
func (u configureUI) head(mark lipgloss.Style, sym, client, status, where string) {
	line := mark.Render(sym) + " " + u.bold.Render(fmt.Sprintf("%-10s", client)) + status
	if where != "" {
		line += u.dim.Render("  " + where)
	}
	fmt.Println(line)
}

// sub prints an indented detail line under a client.
func (u configureUI) sub(text string) {
	for _, l := range strings.Split(text, "\n") {
		fmt.Println("            " + l)
	}
}

func (u configureUI) notFound(client string) {
	u.head(u.dim, "-", client, u.dim.Render("not found, skipped"), "")
	u.sub(u.dim.Render("Installed somewhere else? Run 'goophy configure " + client + "' to set it up anyway,"))
	u.sub(u.dim.Render("or 'goophy configure " + client + " --print' to see what to add by hand."))
}

func (u configureUI) fail(client string, err error) {
	u.head(u.bad, "✗", client, u.bad.Render("failed"), "")
	u.sub(err.Error())
}

func (u configureUI) warnings(ws []string) {
	for _, w := range ws {
		u.sub(u.warn.Render("! ") + w)
	}
}

// client prints one client's outcome: the status, then per changed file the
// settings set or replaced, models added and where the original is kept.
func (u configureUI) client(name, dir string, rs []clientcfg.Result, dryRun bool) {
	var changed []clientcfg.Result
	for _, r := range rs {
		if r.Changed {
			changed = append(changed, r)
		}
	}
	if len(changed) == 0 {
		u.head(u.ok, "✓", name, u.ok.Render("already set up"), tildePath(dir))
		return
	}
	status := "updated"
	if dryRun {
		status = "would update"
	}
	u.head(u.warn, "●", name, u.warn.Render(status), tildePath(dir))
	for _, r := range changed {
		rel, err := filepath.Rel(dir, r.Path)
		if err != nil {
			rel = r.Path
		}
		u.sub(rel + u.dim.Render(backupNote(r)))
		width := 0
		for _, c := range r.Changes {
			width = max(width, len(c.Key))
		}
		for _, c := range r.Changes {
			k := u.key.Render(fmt.Sprintf("%-*s", width, c.Key))
			if c.Old == "" {
				u.sub("  " + k + "  " + c.New)
			} else {
				u.sub("  " + k + "  " + u.dim.Render(c.Old+" → ") + c.New)
			}
		}
		if len(r.AddedModels) > 0 {
			u.sub("  " + u.ok.Render("+ ") + strings.Join(r.AddedModels, ", "))
		}
		if len(r.Changes) == 0 && len(r.AddedModels) == 0 && r.Backup != "" {
			u.sub("  " + u.dim.Render("goophy's version of this file"))
		}
	}
}

func backupNote(r clientcfg.Result) string {
	switch {
	case r.BackupKept:
		return "  (your original stays in " + filepath.Base(r.Backup) + ")"
	case r.Backup != "":
		return "  (original saved as " + filepath.Base(r.Backup) + ")"
	default:
		return "  (new)"
	}
}

// explainConfigErr adds what to do when a config file cannot be parsed.
func explainConfigErr(err error) error {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.As(err, &se) || errors.As(err, &te) || strings.Contains(err.Error(), "not a JSON object") {
		return fmt.Errorf("%v\nFix the file, or move it aside and run this again. Nothing was changed", err)
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

// installed reports whether a client is installed: its binary on PATH or in
// a usual install directory on macOS and Linux, or its config directory.
func installed(bin, configDir string) bool {
	if _, err := exec.LookPath(bin); err == nil {
		return true
	}
	if _, err := os.Stat(configDir); err == nil {
		return true
	}
	home, _ := os.UserHomeDir()
	dirs := []string{
		"/opt/homebrew/bin",              // Homebrew on Apple silicon
		"/usr/local/bin",                 // Homebrew on Intel, manual installs
		"/home/linuxbrew/.linuxbrew/bin", // Homebrew on Linux
		"/usr/bin",
		"/snap/bin",
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),    // pi's and opencode's install scripts
			filepath.Join(home, ".opencode", "bin"), // opencode's install script
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".volta", "bin"),
		)
	}
	for _, d := range dirs {
		if fi, err := os.Stat(filepath.Join(d, bin)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// printConfigs prints, for each client, the file to edit and what goes in
// it, for users who would rather edit the files themselves.
func printConfigs(names []string, opts clientcfg.Options) {
	for i, n := range names {
		if i > 0 {
			fmt.Println()
		}
		switch n {
		case "opencode":
			path, err := clientcfg.OpencodeConfigPath()
			if err != nil {
				fatal(err)
			}
			fmt.Printf("# opencode: merge these keys into %s\n", tildePath(path))
			fmt.Printf("# (create the file with exactly this if it does not exist)\n")
			fmt.Print(string(clientcfg.OpencodeSnippet(opts)))
		case "pi":
			dir, err := clientcfg.PiAgentDir()
			if err != nil {
				fatal(err)
			}
			fmt.Printf("# pi: merge these keys into %s\n", tildePath(filepath.Join(dir, "settings.json")))
			fmt.Print(string(clientcfg.PiSettingsSnippet(opts)))
			fmt.Printf("\n# pi: save this as %s\n", tildePath(filepath.Join(dir, "extensions", "goophy.ts")))
			fmt.Print(clientcfg.PiExtension(opts))
		}
	}
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
