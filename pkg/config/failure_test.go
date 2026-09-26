package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingAndCorruptPreferences(t *testing.T) {
	original := userConfigDir
	root := t.TempDir()
	userConfigDir = func() (string, error) { return root, nil }
	t.Cleanup(func() { userConfigDir = original })
	if got, err := Load(); err != nil || got.Workspace != "" {
		t.Fatalf("missing config: %#v, %v", got, err)
	}
	if err := Save(Config{Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	if err := os.WriteFile(path, []byte(`{"owner":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("corrupt preferences silently accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("directory accepted as preferences")
	}
}

func TestConfigFailuresAreReturned(t *testing.T) {
	original := userConfigDir
	t.Cleanup(func() { userConfigDir = original })
	sentinel := errors.New("config home unavailable")
	userConfigDir = func() (string, error) { return "", sentinel }
	if _, err := Load(); !errors.Is(err, sentinel) {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(Config{}); !errors.Is(err, sentinel) {
		t.Fatalf("Save: %v", err)
	}
	root := t.TempDir()
	userConfigDir = func() (string, error) { return root, nil }
	if err := os.WriteFile(filepath.Join(root, "freshen"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Save(Config{}); err == nil {
		t.Fatal("save succeeded through a file")
	}
	data, _ := os.ReadFile(filepath.Join(root, "freshen"))
	if string(data) != "preserve" {
		t.Fatal("existing file changed")
	}
}
