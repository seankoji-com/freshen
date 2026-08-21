package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	configHome := t.TempDir()
	original := userConfigDir
	userConfigDir = func() (string, error) { return configHome, nil }
	t.Cleanup(func() { userConfigDir = original })
	want := Config{Workspace: "/repos", Owner: "octo", Concurrency: 2, Aliases: []string{"local=remote"}}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != want.Workspace || got.Owner != want.Owner || got.Concurrency != want.Concurrency {
		t.Fatalf("Load() = %#v", got)
	}
	if _, err := os.Stat(filepath.Join(configHome, "freshen", "config.json")); err != nil {
		t.Fatal(err)
	}
}
