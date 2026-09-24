package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alex2481kobe/askother/internal/tools"
	"github.com/alex2481kobe/askother/internal/worker"
)

// Every default, mode and pinned setting in the help comes from the
// adapters' Facts.
func TestHelpRendersFacts(t *testing.T) {
	facts := []worker.Facts{{
		Name: "wk", DefaultMode: "m-default", Modes: []string{"m-default", "m-open"},
		Pinned: []string{"pin-a (reason a)", "pin-b (reason b)"},
	}}
	var b bytes.Buffer
	renderHelp(&b, facts, "/opt/o r/askother")
	out := b.String()
	for _, want := range []string{
		"\nDefaults ", "  wk\n", "default mode  m-default\n", "modes         m-default, m-open\n",
		"pinned        pin-a (reason a)\n", "              pin-b (reason b)\n",
		"\nSetup ", "claude mcp add -s user askother -- '/opt/o r/askother' mcp\n",
		"[mcp_servers.askother]\n", `command = "/opt/o r/askother"` + "\n", `args = ["mcp"]`, `default_tools_approval_mode = "approve"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}

func TestHelpRealWorkers(t *testing.T) {
	var b bytes.Buffer
	renderHelp(&b, tools.Facts(adapters()), "/opt/askother")
	out := b.String()
	for _, f := range tools.Facts(adapters()) {
		for _, want := range append([]string{f.DefaultMode}, f.Pinned...) {
			if !strings.Contains(out, want) {
				t.Errorf("help lacks %s fact %q", f.Name, want)
			}
		}
	}
	if !strings.Contains(out, "  codex\n") || !strings.Contains(out, "  claude\n") {
		t.Errorf("help lacks a worker:\n%s", out)
	}
	for _, gone := range []string{"resume ", "workers tool"} {
		if strings.Contains(out, gone) {
			t.Errorf("help still mentions %q", gone)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/askother": "/usr/local/bin/askother",
		"/a b/askother":           "'/a b/askother'",
		"/it's/askother":          `'/it'\''s/askother'`,
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDispatchRefusals(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"wait"}, {"mcp", "extra"}} {
		var out, errb bytes.Buffer
		if code := dispatch(args, &out, &errb); code != exitRefused || out.Len() != 0 || !strings.Contains(errb.String(), "Usage:") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out.String(), errb.String())
		}
	}
	var out bytes.Buffer
	if code := dispatch([]string{"version"}, &out, &out); code != 0 || !strings.HasPrefix(out.String(), "askother ") {
		t.Errorf("version: %d %q", code, out.String())
	}
}

func TestSubcommandHelp(t *testing.T) {
	for _, args := range [][]string{{"mcp", "--help"}, {"wait", "-h"}, {"ui", "--help"}, {"wait", "run-1", "--help"}} {
		var out, errb bytes.Buffer
		if code := dispatch(args, &out, &errb); code != 0 || !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out.String(), errb.String())
		}
	}
}
