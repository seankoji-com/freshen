package main

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cacheFixtureSHA = "0123456789012345678901234567890123456789"

type cacheFixture struct {
	t       *testing.T
	dir     string
	env     map[string]string
	outputs map[string]string
	store   string
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	f := &cacheFixture{t: t, dir: t.TempDir(), env: map[string]string{
		"RUNNER_OS": "Windows", "RUNNER_ARCH": "X64", "GITHUB_REPOSITORY": "fixture/freshen",
		"GITHUB_JOB": "test", "GITHUB_SHA": cacheFixtureSHA, "GITHUB_EVENT_NAME": "push",
		"GITHUB_REF": "refs/heads/main", "JOB_STATUS": "success",
		"FIXTURE_GOVERSION": "go1.26.5", "FIXTURE_GOOS": "windows", "FIXTURE_GOARCH": "amd64",
		"FIXTURE_CGO_ENABLED": "1", "FIXTURE_CC": "gcc", "FIXTURE_COMPILER": "gcc fixture 1",
		"FIXTURE_REVISION": cacheFixtureSHA,
	}, outputs: make(map[string]string)}
	for _, name := range []string{".github/scripts/go-cache.sh", ".github/workflows/ci.yml", "Makefile"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f.write(name, data)
	}
	f.write("go.mod", []byte("module fixture\n\ngo 1.26.5\n"))
	f.write("go.sum", []byte("fixture v1.0.0 h1:fixture\n"))
	f.write("bin/go", []byte(`#!/usr/bin/env bash
set -eu
[[ "$1" == env ]]
shift
for key in "$@"; do
  name="FIXTURE_$key"
  printf '%s\n' "${!name-}"
done
`))
	f.write("bin/git", []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$FIXTURE_REVISION\"\n"))
	f.write("bin/gcc", []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$FIXTURE_COMPILER\"\n"))
	f.env["PATH"] = filepath.Join(f.dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	f.env["RUNNER_TEMP"] = filepath.Join(f.dir, "temp")
	f.write("temp/.keep", nil)
	f.env["GITHUB_ENV"] = filepath.Join(f.dir, "env")
	f.env["GITHUB_OUTPUT"] = filepath.Join(f.dir, "outputs")
	f.write("env", nil)
	f.write("outputs", nil)
	return f
}

func (f *cacheFixture) write(name string, data []byte) {
	f.t.Helper()
	path := filepath.Join(f.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cacheFixture) command(mode string, wantSuccess bool) {
	f.t.Helper()
	cmd := exec.Command("bash", ".github/scripts/go-cache.sh", mode)
	cmd.Dir = f.dir
	// Remove inherited Go/toolchain and cache-policy values. Fixtures never use
	// the host's mutable stores or execute real Git/Go/compiler operations.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := f.env[key]; !overridden && !strings.HasPrefix(key, "GO") && !strings.HasPrefix(key, "MODULE_") && !strings.HasPrefix(key, "BUILD_") && key != "FRESHEN_GO_ROOT" && key != "XDG_CACHE_HOME" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range f.env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	if (err == nil) != wantSuccess {
		f.t.Fatalf("%s: success=%v, want %v: %s", mode, err == nil, wantSuccess, out)
	}
	if !wantSuccess {
		f.t.Logf("rejected %s: %s", mode, strings.TrimSpace(string(out)))
	}
}

func (f *cacheFixture) load(name string, dst map[string]string) {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			dst[key] = value
		}
	}
}

func (f *cacheFixture) prepare() {
	f.t.Helper()
	f.command("prepare", true)
	f.load("env", f.env)
	f.load("outputs", f.outputs)
	f.store = filepath.Dir(f.env["GOCACHE"])
	for _, kind := range []string{"modules", "build"} {
		if len(f.outputs[kind]) > 512 {
			f.t.Fatalf("%s key exceeds Actions cache limit: %d", kind, len(f.outputs[kind]))
		}
		prefix := strings.ToUpper(kind)
		if kind == "modules" {
			prefix = "MODULE"
		}
		f.env[prefix+"_PRIMARY"] = f.outputs[kind]
		f.env[prefix+"_MATCHED"] = ""
		f.env[prefix+"_HIT"] = ""
	}
}

func TestCICachePublishPolicy(t *testing.T) {
	for _, tc := range []struct{ event, ref, status string }{
		{"push", "refs/heads/main", "success"},
		{"pull_request", "refs/pull/154/merge", "success"},
		{"pull_request", "refs/heads/main", "success"},
		{"pull_request_target", "refs/heads/main", "success"},
		{"merge_group", "refs/heads/main", "success"},
		{"merge_group", "refs/heads/gh-readonly-queue/main/fixture", "success"},
		{"push", "refs/heads/topic", "success"}, {"push", "refs/tags/v1", "success"},
		{"workflow_dispatch", "refs/heads/main", "success"}, {"schedule", "refs/heads/main", "success"},
		{"push", "refs/heads/main", "failure"}, {"push", "refs/heads/main", "cancelled"},
		{"push", "refs/heads/main", "skipped"},
	} {
		t.Run(tc.event+"/"+tc.ref+"/"+tc.status, func(t *testing.T) {
			f := newCacheFixture(t)
			f.prepare()
			f.env["GITHUB_EVENT_NAME"], f.env["GITHUB_REF"], f.env["JOB_STATUS"] = tc.event, tc.ref, tc.status
			f.command("publish", true)
			f.load("outputs", f.outputs)
			want := tc.event == "push" && tc.ref == "refs/heads/main" && tc.status == "success"
			for _, kind := range []string{"modules", "build"} {
				_, err := os.Stat(filepath.Join(f.dir, ".freshen-ci-cache", kind+".tar"))
				if (err == nil) != want || (f.outputs[kind] == "true") != want {
					t.Fatalf("%s publication=%v, permission=%s, want %v", kind, err == nil, f.outputs[kind], want)
				}
			}
			t.Logf("publish allowed=%v", want)
		})
	}
}

func TestCICacheIncompatibleInputs(t *testing.T) {
	for _, key := range []string{"FIXTURE_GOVERSION", "RUNNER_OS", "RUNNER_ARCH", "FIXTURE_GOOS", "FIXTURE_GOARCH", "FIXTURE_CGO_ENABLED", "FIXTURE_CC", "FIXTURE_COMPILER", "FIXTURE_CGO_CFLAGS", "FIXTURE_GOFLAGS", "FIXTURE_GOEXPERIMENT", "GITHUB_REPOSITORY", "GITHUB_JOB", "GITHUB_SHA", "FIXTURE_REVISION", "GOMODCACHE", "GOCACHE", "GOPATH", "XDG_CACHE_HOME", "MODULE_PRIMARY", "BUILD_PRIMARY", "MODULE_MATCHED", "BUILD_MATCHED", "MODULE_HIT", "BUILD_HIT"} {
		t.Run(key, func(t *testing.T) {
			f := newCacheFixture(t)
			f.prepare()
			f.env[key] += "incompatible"
			f.command("restore", false)
		})
	}
	for _, name := range []string{"go.mod", "go.sum", "Makefile", ".github/workflows/ci.yml", ".github/scripts/go-cache.sh"} {
		t.Run(name, func(t *testing.T) {
			f := newCacheFixture(t)
			f.prepare()
			file, err := os.OpenFile(filepath.Join(f.dir, name), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n# policy changed\n"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			f.command("restore", false)
		})
	}
}

func TestCICacheCompatibleScopeBoundaries(t *testing.T) {
	for _, changes := range []map[string]string{
		{"FIXTURE_GOVERSION": "go1.26.6"},
		{"RUNNER_ARCH": "ARM64", "FIXTURE_GOARCH": "arm64"},
		{"FIXTURE_CGO_ENABLED": "0"},
		{"FIXTURE_COMPILER": "gcc fixture 2"},
		{"FIXTURE_GOFLAGS": "-trimpath"},
		{"FIXTURE_CGO_CFLAGS": "-O3"},
		{"FIXTURE_GOAMD64": "v3"},
	} {
		var name string
		for key, value := range changes {
			name += key + "=" + value
		}
		t.Run(name, func(t *testing.T) {
			a, b := newCacheFixture(t), newCacheFixture(t)
			a.prepare()
			for key, value := range changes {
				b.env[key] = value
			}
			b.prepare()
			for _, kind := range []string{"modules", "build"} {
				if a.outputs[kind+"-prefix"] == b.outputs[kind+"-prefix"] {
					t.Fatalf("%s compatible prefix ignored changed inputs", kind)
				}
			}
			b.env["BUILD_MATCHED"], b.env["BUILD_HIT"] = a.outputs["build"], "false"
			b.command("restore", false)
		})
	}
}

func TestCICacheIsolatedStores(t *testing.T) {
	a, b := newCacheFixture(t), newCacheFixture(t)
	a.prepare()
	b.env["GITHUB_EVENT_NAME"] = "pull_request"
	b.prepare()
	if a.outputs["build"] != b.outputs["build"] || a.store == b.store {
		t.Fatal("same compatible key must restore into different job-local stores")
	}
	if err := os.WriteFile(filepath.Join(a.store, "build", "fixture"), []byte("job A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.store, "build", "fixture")); !os.IsNotExist(err) {
		t.Fatal("mutable store leaked between jobs")
	}
	b.command("restore", true)
	t.Log("PR/main stores are distinct; cache miss is safe")
}

func (f *cacheFixture) archive(kind string, headers []*tar.Header, contents []string) {
	f.t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for i, header := range headers {
		if header.Typeflag == tar.TypeReg {
			header.Size = int64(len(contents[i]))
		}
		if err := w.WriteHeader(header); err != nil {
			f.t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := w.Write([]byte(contents[i])); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.write(".freshen-ci-cache/"+kind+".tar", buf.Bytes())
}

func TestCICacheArchives(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "build/../../escape", "build/./escape", "build/evil\\path", "build/C:escape", "build//escape", "gopath/bin/tool", "mod/extracted/file", "build/evil\nname"} {
		t.Run(name, func(t *testing.T) {
			f := newCacheFixture(t)
			f.prepare()
			f.env["BUILD_MATCHED"], f.env["BUILD_HIT"] = f.outputs["build"], "true"
			f.archive("build", []*tar.Header{{Name: name, Typeflag: tar.TypeReg, Mode: 0o600}}, []string{"untrusted"})
			f.command("restore", false)
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo} {
		t.Run(string(kind), func(t *testing.T) {
			f := newCacheFixture(t)
			f.prepare()
			f.env["BUILD_MATCHED"], f.env["BUILD_HIT"] = f.outputs["build"], "true"
			f.archive("build", []*tar.Header{{Name: "build/link", Typeflag: kind, Linkname: "../escape", Mode: 0o600}}, []string{""})
			f.command("restore", false)
		})
	}
	for _, variant := range []string{"corrupt", "missing", "stale-miss", "wrong-manifest", "extra-fallback-component", "mutable-symlink", "transport-symlink"} {
		t.Run(variant, func(t *testing.T) {
			f := newCacheFixture(t)
			f.prepare()
			f.env["BUILD_MATCHED"], f.env["BUILD_HIT"] = f.outputs["build"], "true"
			switch variant {
			case "corrupt", "stale-miss":
				f.write(".freshen-ci-cache/build.tar", []byte("broken tar"))
				if variant == "stale-miss" {
					f.env["BUILD_MATCHED"], f.env["BUILD_HIT"] = "", ""
				}
			case "wrong-manifest":
				f.archive("build", []*tar.Header{{Name: "manifest", Typeflag: tar.TypeReg, Mode: 0o600}, {Name: "build/", Typeflag: tar.TypeDir, Mode: 0o700}}, []string{"incompatible", ""})
			case "extra-fallback-component":
				f.env["BUILD_MATCHED"] = f.outputs["build-prefix"] + "incompatible-" + cacheFixtureSHA
				f.env["BUILD_HIT"] = "false"
			case "mutable-symlink", "transport-symlink":
				// Windows may lack symlink privilege; that is unavailable fixture
				// evidence, never a cache-policy pass for that host.
				name := filepath.Join(f.store, "build", "link")
				if variant == "transport-symlink" {
					name = filepath.Join(f.dir, ".freshen-ci-cache", "build.tar")
				}
				if err := os.Symlink(f.dir, name); err != nil {
					t.Skipf("symlink fixture unavailable: %v", err)
				}
			}
			f.command("restore", false)
		})
	}
}

func TestCICacheWarmRestore(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "revision-only-fallback"}[fallback], func(t *testing.T) {
			a, b := newCacheFixture(t), newCacheFixture(t)
			a.prepare()
			if fallback {
				b.env["GITHUB_SHA"], b.env["FIXTURE_REVISION"] = strings.Repeat("a", 40), strings.Repeat("a", 40)
			}
			b.prepare()
			for _, name := range []string{"build/fixture", "mod/cache/download/fixture.zip"} {
				if err := os.WriteFile(filepath.Join(a.store, name), []byte("seed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a.command("publish", true)
			for _, kind := range []string{"modules", "build"} {
				bundle := ".freshen-ci-cache/" + kind + ".tar"
				data, err := os.ReadFile(filepath.Join(a.dir, bundle))
				if err != nil {
					t.Fatal(err)
				}
				b.write(bundle, data)
				prefix := "BUILD"
				if kind == "modules" {
					prefix = "MODULE"
				}
				b.env[prefix+"_MATCHED"] = a.outputs[kind]
				b.env[prefix+"_HIT"] = map[bool]string{false: "true", true: "false"}[fallback]
			}
			b.command("restore", true)
			for _, name := range []string{"build/fixture", "mod/cache/download/fixture.zip"} {
				data, err := os.ReadFile(filepath.Join(b.store, name))
				if err != nil || string(data) != "seed" {
					t.Fatalf("restored %s: %q, %v", name, data, err)
				}
			}
			if err := os.WriteFile(filepath.Join(b.store, "build/fixture"), []byte("mutated"), 0o600); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(a.store, "build/fixture"))
			if err != nil || string(data) != "seed" {
				t.Fatal("restored mutable writes changed seed store")
			}
			b.command("publish", true)
			b.load("outputs", b.outputs)
			for _, kind := range []string{"modules", "build"} {
				if (b.outputs[kind] == "true") != fallback {
					t.Fatalf("%s publication must skip exact hits and permit compatible revision fallback", kind)
				}
			}
			t.Log("both cache archives restored into isolated stores")
		})
	}
}
