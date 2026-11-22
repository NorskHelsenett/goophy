package main

import (
	"flag"
	"io"
	"testing"
)

func TestDashboardWanted(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  string
		want bool
	}{
		{nil, "", true},
		{[]string{"--tui"}, "", true},
		{[]string{"--tui=false"}, "", false},
		{[]string{"--no-tui"}, "", false},
		{nil, "false", false},
		{nil, "0", false},
		{nil, "off", false},
		{nil, "true", true},
		{[]string{"--tui"}, "false", true}, // the flag wins over its env variable
		{[]string{"--tui", "--no-tui"}, "", false},
	} {
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		noTUI := fs.Bool("no-tui", false, "")
		tui := fs.Bool("tui", true, "")
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if got := dashboardWanted(fs, *tui, *noTUI, tc.env); got != tc.want {
			t.Errorf("args %v, GOOPHY_TUI=%q: got %v, want %v", tc.args, tc.env, got, tc.want)
		}
	}
}
