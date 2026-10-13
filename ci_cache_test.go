package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const cacheFixtureSHA = "0123456789012345678901234567890123456789"

// shimTools are the external tools go-cache.sh probes while it derives the cache
// identity. Every one must resolve to the fixture's own shim: reaching a real
// go/git/gcc would let a host toolchain satisfy a cache-policy fixture.
var shimTools = []string{"go", "git", "gcc"}

// shimSelector is sourced through BASH_ENV, which bash reads for every
// non-interactive shell, including `bash .github/scripts/go-cache.sh <mode>`.
// It defines go/git/gcc as functions that hand off to the extensionless shim
// scripts in bin/.
//
// Shell function lookup happens before any PATH search, and `bash <file>` reads
// the shim without needing an executable bit, so shim selection stops depending
// on how Git-Bash spelled or converted the fixture's PATH entry, on the
// Windows/NTFS executable-bit rules, or on the host path-list separator. That
// combination is what let the earlier PATH-only fixture reach the runner's real
// git.exe on windows-latest ("fatal: not a git repository").
const shimSelector = `FRESHEN_SHIM_DIR=${PWD:-.}
go()  { printf 'go\n'  >> "$FRESHEN_SHIM_DIR/.freshen-shims"; command "${BASH:-bash}" "$FRESHEN_SHIM_DIR/bin/go"  "$@"; }
git() { printf 'git\n' >> "$FRESHEN_SHIM_DIR/.freshen-shims"; command "${BASH:-bash}" "$FRESHEN_SHIM_DIR/bin/git" "$@"; }
gcc() { printf 'gcc\n' >> "$FRESHEN_SHIM_DIR/.freshen-shims"; command "${BASH:-bash}" "$FRESHEN_SHIM_DIR/bin/gcc" "$@"; }
`

type cacheFixture struct {
	t       *testing.T
	dir     string
	env     map[string]string
	outputs map[string]string
	store   string
	// caseInsensitiveKeys mirrors the environment folding os/exec applies on
	// Windows: dedupEnv compares keys case-insensitively and keeps the last
	// value. Portable fixtures force it on to exercise that branch on any host.
	caseInsensitiveKeys bool
	probed              bool
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	return newCacheFixtureIn(t, "")
}

// newCacheFixtureIn roots the fixture in a subdirectory named dirName, which
// lets a test exercise the spellings real runner paths take, such as
// "Program Files (x86)".
func newCacheFixtureIn(t *testing.T, dirName string) *cacheFixture {
	t.Helper()
	dir := t.TempDir()
	if dirName != "" {
		dir = filepath.Join(dir, dirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := &cacheFixture{t: t, dir: dir, caseInsensitiveKeys: runtime.GOOS == "windows", env: map[string]string{
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
	f.write("shims.bash", []byte(shimSelector))
	f.env["BASH_ENV"] = f.posixPath(filepath.Join(f.dir, "shims.bash"))
	f.env["RUNNER_TEMP"] = filepath.Join(f.dir, "temp")
	f.write("temp/.keep", nil)
	f.env["GITHUB_ENV"] = filepath.Join(f.dir, "env")
	f.env["GITHUB_OUTPUT"] = filepath.Join(f.dir, "outputs")
	f.write("env", nil)
	f.write("outputs", nil)
	// A native parent hands Git-Bash a Win32 path list, which the Cygwin runtime
	// converts to POSIX form when the process starts (Cygwin User's Guide,
	// "Additional Path-related Information"). Entries stay verbatim so drive
	// letters, backslashes and spaces survive that conversion.
	f.env["PATH"] = pathList(append([]string{filepath.Join(f.dir, "bin")}, filepath.SplitList(os.Getenv("PATH"))...), string(os.PathListSeparator))
	return f
}

// pathList renders a PATH-style list, dropping empty entries so a stray
// separator cannot silently inject the working directory.
func pathList(entries []string, sep string) string {
	kept := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry != "" {
			kept = append(kept, entry)
		}
	}
	return strings.Join(kept, sep)
}

// posixPath returns the POSIX spelling of p that Git-Bash uses. go-cache.sh
// converts RUNNER_TEMP the same way through `cygpath -u`; a value the fixture
// hands to bash (BASH_ENV) is not one of the variables the runtime converts by
// itself, so the fixture converts it explicitly. On POSIX hosts it is a no-op.
func (f *cacheFixture) posixPath(p string) string {
	f.t.Helper()
	if runtime.GOOS != "windows" {
		return p
	}
	cmd := exec.Command("bash", "-c", `if command -v cygpath >/dev/null 2>&1; then cygpath -u "$1"; else printf '%s\n' "$1"; fi`, "freshen-fixture", p)
	cmd.Dir = f.dir
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("cygpath -u %q: %v", p, err)
	}
	return strings.TrimRight(string(out), "\r\n")
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

// envKey folds environment keys the way os/exec's dedupEnv does on Windows:
// keys are compared case-insensitively and the last value wins. Windows runners
// carry `Path` and `GoCache` spellings beside a fixture's `PATH`/`GOCACHE`, so
// the fixture folds the same way instead of relying on an exact-case lookup.
func (f *cacheFixture) envKey(key string) string {
	if f.caseInsensitiveKeys {
		return strings.ToUpper(key)
	}
	return key
}

// childEnv renders cmd.Env. Inherited entries are kept except the Go/toolchain
// values and the cache-policy values the fixture redefines, so a fixture can
// never read the host's mutable stores or resolve a host toolchain through
// them. Fixture keys are appended last, in a stable order, and the key folding
// above matches os/exec's Windows dedup so exactly one spelling of each key
// reaches the child.
func (f *cacheFixture) childEnv() []string {
	f.t.Helper()
	overridden := make(map[string]bool, len(f.env))
	for key := range f.env {
		overridden[f.envKey(key)] = true
	}
	inheritedBlocked := func(key string) bool {
		folded := strings.ToUpper(key)
		if overridden[f.envKey(key)] {
			return true
		}
		// FRESHEN_GO_ROOT and XDG_CACHE_HOME name host mutable stores that
		// go-cache.sh also reads; the fixture must never resolve either through
		// the inherited environment, before or after prepare publishes its own.
		switch folded {
		case "FRESHEN_GO_ROOT", "XDG_CACHE_HOME":
			return true
		}
		return strings.HasPrefix(folded, "GO") || strings.HasPrefix(folded, "MODULE_") || strings.HasPrefix(folded, "BUILD_")
	}
	env := make([]string, 0, len(f.env)+len(os.Environ()))
	seen := make(map[string]bool, len(f.env))
	add := func(entry string) {
		key, _, ok := strings.Cut(entry, "=")
		// Entries with a single leading "=" (Windows drive-relative CWD values)
		// carry no name to fold, so they pass through untouched.
		if !ok || key == "" {
			env = append(env, entry)
			return
		}
		folded := f.envKey(key)
		if seen[folded] {
			return
		}
		seen[folded] = true
		env = append(env, entry)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "" && inheritedBlocked(key) {
			continue
		}
		add(entry)
	}
	keys := make([]string, 0, len(f.env))
	for key := range f.env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		add(key + "=" + f.env[key])
	}
	return env
}

// shimProbe runs every shim through the fixture environment and compares the
// output with the values the fixture published. A real toolchain, or a selector
// that never loaded, produces different output, so the probe fails instead of
// letting a host binary satisfy a cache assertion. It never skips.
func (f *cacheFixture) shimProbe() error {
	f.t.Helper()
	const script = `set -u
for tool in go git gcc; do printf 'which-%s=%s\n' "$tool" "$(command -v "$tool")"; done
printf 'go-env=%s\n' "$(go env GOVERSION)"
printf 'git-rev=%s\n' "$(git rev-parse HEAD)"
printf 'gcc-ver=%s\n' "$(gcc --version)"
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = f.dir
	cmd.Env = f.childEnv()
	out, err := cmd.CombinedOutput()
	observed := make(map[string]string, len(shimTools)+3)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			observed[key] = value
		}
	}
	var problems []string
	if err != nil {
		problems = append(problems, fmt.Sprintf("probe exited with %v", err))
	}
	for _, tool := range shimTools {
		resolved := observed["which-"+tool]
		if resolved != tool && !strings.HasSuffix(filepath.ToSlash(resolved), "/bin/"+tool) {
			problems = append(problems, fmt.Sprintf("%s resolved to %q, not a fixture shim", tool, resolved))
		}
	}
	for _, expect := range []struct{ field, key string }{
		{"FIXTURE_GOVERSION", "go-env"},
		{"FIXTURE_REVISION", "git-rev"},
		{"FIXTURE_COMPILER", "gcc-ver"},
	} {
		if observed[expect.key] != f.env[expect.field] {
			problems = append(problems, fmt.Sprintf("%s=%q, want %q from %s", expect.key, observed[expect.key], f.env[expect.field], expect.field))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s (BASH_ENV=%q PATH=%q): %s", strings.Join(problems, "; "), f.env["BASH_ENV"], f.env["PATH"], out)
}

// verifyShims asserts, once per fixture, that go-cache.sh will resolve go, git
// and gcc to the fixture shims. A failure is fatal: it must never be reported as
// a pass, and it is never skipped on a platform where the shims do not resolve.
func (f *cacheFixture) verifyShims() {
	f.t.Helper()
	if f.probed {
		return
	}
	f.probed = true
	if err := f.shimProbe(); err != nil {
		f.t.Fatalf("fixture shims are not selectable: %v", err)
	}
}

func (f *cacheFixture) command(mode string, wantSuccess bool) {
	f.t.Helper()
	f.verifyShims()
	cmd := exec.Command("bash", ".github/scripts/go-cache.sh", mode)
	cmd.Dir = f.dir
	cmd.Env = f.childEnv()
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

// TestCICacheShimIdentity shows that prepare resolves, and actually runs, the
// fixture's own go/git/gcc. The shims append to .freshen-shims, so a real host
// toolchain that happened to satisfy the policy would leave the marker empty and
// fail here instead of passing on a technicality.
func TestCICacheShimIdentity(t *testing.T) {
	f := newCacheFixture(t)
	f.prepare()
	marker, err := os.ReadFile(filepath.Join(f.dir, ".freshen-shims"))
	if err != nil {
		t.Fatalf("prepare never ran a fixture shim (%v); a real toolchain must not satisfy this fixture", err)
	}
	ran := make(map[string]bool)
	for _, tool := range strings.Fields(string(marker)) {
		ran[tool] = true
	}
	for _, tool := range shimTools {
		if !ran[tool] {
			t.Fatalf("prepare did not exercise the %s shim; shims run: %v", tool, ran)
		}
	}
	t.Logf("prepare ran only fixture shims: %v", ran)
}

// TestCICacheShimSelectorOnly reproduces the position the windows-latest runner
// was in: no fixture directory on PATH, so only the sourced selector can supply
// go/git/gcc. Shim selection, and therefore the whole policy flow, must still
// hold.
func TestCICacheShimSelectorOnly(t *testing.T) {
	f := newCacheFixture(t)
	f.env["PATH"] = pathList(filepath.SplitList(os.Getenv("PATH")), string(os.PathListSeparator))
	if err := f.shimProbe(); err != nil {
		t.Fatalf("selector alone did not select the shims: %v", err)
	}
	f.prepare()
	if _, err := os.Stat(filepath.Join(f.dir, ".freshen-ci-cache")); err != nil {
		t.Fatalf("prepare did not complete without a fixture PATH entry: %v", err)
	}
	t.Log("selector alone drove prepare onto the fixture shims")
}

// TestCICacheShimSelectorRequired is the deliberate bad case: with the fixture
// PATH entry gone and the selector missing, empty or defining foreign tools, a
// real binary is reachable and the probe must fail rather than pass.
func TestCICacheShimSelectorRequired(t *testing.T) {
	for _, tc := range []struct {
		name    string
		disable func(fixture *cacheFixture)
	}{
		{"missing", func(fixture *cacheFixture) {
			fixture.env["BASH_ENV"] = fixture.posixPath(filepath.Join(fixture.dir, "absent.bash"))
		}},
		{"empty", func(fixture *cacheFixture) {
			fixture.write("empty.bash", nil)
			fixture.env["BASH_ENV"] = fixture.posixPath(filepath.Join(fixture.dir, "empty.bash"))
		}},
		{"foreign", func(fixture *cacheFixture) {
			fixture.write("foreign.bash", []byte("go() { printf 'go9.9.9\\n'; }\ngit() { printf 'not-a-shim\\n'; }\ngcc() { printf 'not-a-shim\\n'; }\n"))
			fixture.env["BASH_ENV"] = fixture.posixPath(filepath.Join(fixture.dir, "foreign.bash"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCacheFixture(t)
			f.env["PATH"] = pathList(filepath.SplitList(os.Getenv("PATH")), string(os.PathListSeparator))
			tc.disable(f)
			if err := f.shimProbe(); err == nil {
				t.Fatal("probe passed without the fixture selector; a real tool could satisfy this fixture")
			} else {
				t.Logf("probe rejected the bad case: %v", err)
			}
		})
	}
}

// TestCICacheSpacedPaths runs the publish/warm-restore round trip from
// directories spelled the way real runner roots are, with spaces, parentheses
// and shell metacharacters. Both the PATH entry and the selector path carry
// those characters now, so the fixture proves they survive intact.
func TestCICacheSpacedPaths(t *testing.T) {
	for _, dirName := range []string{"freshen cache", "freshen cache (x86)", "freshen cache & data=1"} {
		t.Run(dirName, func(t *testing.T) {
			a, b := newCacheFixtureIn(t, dirName), newCacheFixtureIn(t, dirName)
			if !strings.Contains(a.dir, " ") {
				t.Fatalf("fixture dir %q does not exercise spaces", a.dir)
			}
			a.prepare()
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
				b.env[prefix+"_HIT"] = "true"
			}
			b.command("restore", true)
			for _, name := range []string{"build/fixture", "mod/cache/download/fixture.zip"} {
				data, err := os.ReadFile(filepath.Join(b.store, name))
				if err != nil || string(data) != "seed" {
					t.Fatalf("restored %s: %q, %v", name, data, err)
				}
			}
			t.Logf("round trip survived fixture path %q", a.dir)
		})
	}
}

// TestCICachePathListSpellings pins the PATH rendering. A native parent hands
// Git-Bash a Win32 path list, so drive letters, backslashes and spaces must stay
// verbatim for the runtime's own Win32-to-POSIX conversion to map each entry.
func TestCICachePathListSpellings(t *testing.T) {
	win32 := []string{`C:\Program Files\freshen\bin`, `C:\actions-runner\_work\_temp`, `D:\a\_temp`}
	if got, want := pathList(win32, ";"), `C:\Program Files\freshen\bin;C:\actions-runner\_work\_temp;D:\a\_temp`; got != want {
		t.Fatalf("win32 path list = %q, want %q", got, want)
	}
	if got, want := pathList([]string{"/opt/freshen/bin", "/usr/local/bin"}, ":"), "/opt/freshen/bin:/usr/local/bin"; got != want {
		t.Fatalf("posix path list = %q, want %q", got, want)
	}
	if got, want := pathList([]string{"/a", "", "/b"}, ":"), "/a:/b"; got != want {
		t.Fatalf("path list with an empty entry = %q, want %q", got, want)
	}
}

// TestCICacheEnvironmentKeyCase pins the folding os/exec applies on Windows
// (dedupEnv compares keys case-insensitively and the last value wins). A runner
// can carry `Path` and `GoCache` spellings beside the fixture's `PATH` and
// `GOCACHE`, and an exact-case override would leave the host's real toolchain
// lookup and mutable stores in the child environment.
func TestCICacheEnvironmentKeyCase(t *testing.T) {
	// Build the fixture first: it composes its own PATH from the process PATH, and
	// on Windows `Path` and `PATH` are one variable, so installing the host
	// spellings first would fold them into the fixture value instead of standing
	// beside it.
	f := newCacheFixture(t)
	f.caseInsensitiveKeys = true
	hostTools := filepath.Join(t.TempDir(), "host-tools")
	hostCache := filepath.Join(t.TempDir(), "host-go-cache")
	t.Setenv("Path", hostTools)
	t.Setenv("GoCache", hostCache)
	t.Setenv("FRESHEN_FIXTURE_UNRELATED", "keep")

	env := f.childEnv()
	paths, unrelated := 0, false
	for _, entry := range env {
		if strings.Contains(entry, hostTools) || strings.Contains(entry, hostCache) {
			t.Fatalf("host override survived the fixture environment: %q", entry)
		}
		if entry == "FRESHEN_FIXTURE_UNRELATED=keep" {
			unrelated = true
		}
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PATH") {
			paths++
			if value != f.env["PATH"] {
				t.Fatalf("PATH entry %q is not the fixture value %q", value, f.env["PATH"])
			}
		}
	}
	if paths != 1 {
		t.Fatalf("child environment carries %d PATH keys, want exactly 1", paths)
	}
	if !unrelated {
		t.Fatal("unrelated inherited variable was dropped along with the folded overrides")
	}
}
