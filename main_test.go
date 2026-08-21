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
	if err := applyConfigAliases([]string{"config-local=config-remote"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := git.GetLocalDirName("config-remote"); !ok || got != "config-local" {
		t.Fatalf("GetLocalDirName(config-remote) = %q, %v", got, ok)
	}
	if err := applyConfigAliases([]string{"bad alias"}); err == nil {
		t.Fatal("applyConfigAliases accepted an invalid alias")
	}
}
