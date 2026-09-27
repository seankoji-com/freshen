package main

import (
	"testing"

	"github.com/seankoji-com/freshen/pkg/git"
)

func TestConfigConcurrency(t *testing.T) {
	if got := configConcurrency(7); got != 7 {
		t.Fatalf("configConcurrency(7) = %d, want 7", got)
	}
	if got := configConcurrency(0); got != 4 {
		t.Fatalf("configConcurrency(0) = %d, want 4", got)
	}
}

func TestApplyConfigAliases(t *testing.T) {
	if err := applyConfigAliases([]string{" config-local = config-remote "}); err != nil {
		t.Fatal(err)
	}
	if got, ok := git.GetLocalDirName("config-remote"); !ok || got != "config-local" {
		t.Fatalf("GetLocalDirName(config-remote) = %q, %v", got, ok)
	}
	for _, alias := range []string{"bad alias", "=remote", "local="} {
		if err := applyConfigAliases([]string{alias}); err == nil {
			t.Fatalf("applyConfigAliases accepted invalid alias %q", alias)
		}
	}
}

func TestDisplayVersion(t *testing.T) {
	cases := map[string]string{
		"0.2.0":        "v0.2.0", // GoReleaser {{.Version}}
		"v0.2.0":       "v0.2.0", // git describe
		"v0.2.0-3-gab": "v0.2.0-3-gab",
		"dev":          "dev",
		"ab12cd3":      "ab12cd3", // git describe --always with no tags
	}
	for in, want := range cases {
		if got := displayVersion(in); got != want {
			t.Errorf("displayVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
