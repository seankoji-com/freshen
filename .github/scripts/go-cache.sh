#!/usr/bin/env bash
# Windows-only source pilot. Transport archives are never mutable Go stores.
set -euo pipefail

fail() { echo 'Go cache policy rejected an input or archive' >&2; exit 1; }
digest() {
  if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi | cut -d ' ' -f 1
}
go_path() {
  if command -v cygpath >/dev/null; then cygpath -w "$1"; else printf '%s\n' "$1"; fi
}
temp_path() {
  if command -v cygpath >/dev/null; then cygpath -u "$RUNNER_TEMP"; else printf '%s\n' "$RUNNER_TEMP"; fi
}
identity() {
  local version os arch cgo compiler flags lock policy scope
  [[ "$RUNNER_OS" == Windows && "$RUNNER_ARCH" =~ ^(X64|ARM64)$ ]] || fail
  [[ "$GITHUB_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ && "$GITHUB_JOB" == test ]] || fail
  [[ "$GITHUB_SHA" =~ ^[0-9a-f]{40}$ && "$(git rev-parse HEAD)" == "$GITHUB_SHA" ]] || fail
  version=$(go env GOVERSION) os=$(go env GOOS) arch=$(go env GOARCH) cgo=$(go env CGO_ENABLED)
  [[ "$version" =~ ^go[0-9]+\.[0-9]+\.[0-9]+$ && "$os" == windows && "$arch" =~ ^(amd64|arm64)$ && "$cgo" =~ ^[01]$ ]] || fail
  [[ "$RUNNER_ARCH/$arch" == X64/amd64 || "$RUNNER_ARCH/$arch" == ARM64/arm64 ]] || fail
  compiler=disabled
  if [[ "$cgo" == 1 ]]; then
    local cc target
    cc=$(go env CC)
    # Reject shell fragments; hosted Go uses a single compiler executable.
    [[ "$cc" =~ ^[A-Za-z0-9_./:-]+$ ]] || fail
    compiler=$("$cc" --version) || fail
    target=$("$cc" -dumpmachine) || fail
    compiler=$(printf '%s\n%s\n' "$compiler" "$target" | digest)
  fi
  flags=$(go env CC CXX CGO_CFLAGS CGO_CPPFLAGS CGO_CXXFLAGS CGO_LDFLAGS GOFLAGS GOAMD64 GOARM64 GOEXPERIMENT GOTOOLCHAIN | digest)
  lock=$( { digest < go.mod; digest < go.sum; } | digest)
  policy=$( { digest < .github/scripts/go-cache.sh; digest < .github/workflows/ci.yml; digest < Makefile; } | digest)
  scope=$(printf '%s/main/test' "$GITHUB_REPOSITORY" | digest)
  printf 'freshen-go-v1-%s-%s-%s-%s-%s-cgo%s-cc%s-flags%s-race-coverage-%s-%s' "$scope" "$version" "$RUNNER_OS" "$RUNNER_ARCH" "$arch" "$cgo" "$compiler" "$flags" "$policy" "$lock"
}
check() {
  local base current kind expected primary matched hit links
  base=$(temp_path)
  [[ "$FRESHEN_GO_ROOT" == "$base"/freshen-go.* && -d "$FRESHEN_GO_ROOT" && ! -L "$FRESHEN_GO_ROOT" ]] || fail
  [[ "$GOMODCACHE" == "$(go_path "$FRESHEN_GO_ROOT/mod")" && "$GOCACHE" == "$(go_path "$FRESHEN_GO_ROOT/build")" && "$GOPATH" == "$(go_path "$FRESHEN_GO_ROOT/gopath")" && "$XDG_CACHE_HOME" == "$(go_path "$FRESHEN_GO_ROOT/xdg")" ]] || fail
  [[ -d .freshen-ci-cache && ! -L .freshen-ci-cache ]] || fail
  links=$(find "$FRESHEN_GO_ROOT" \( -type l -o -type f -links +1 \) -print) || fail
  [[ -z "$links" ]] || fail
  current=$(identity)
  [[ "$current" == "$(< "$FRESHEN_GO_ROOT/identity")" ]] || fail
  for kind in modules build; do
    expected="$current-$kind-$GITHUB_SHA"
    if [[ "$kind" == modules ]]; then
      primary=$MODULE_PRIMARY matched=$MODULE_MATCHED hit=$MODULE_HIT
    else
      primary=$BUILD_PRIMARY matched=$BUILD_MATCHED hit=$BUILD_HIT
    fi
    [[ "$primary" == "$expected" ]] || fail
    if [[ -n "$matched" ]]; then
      [[ "$matched" == "$current-$kind-"* && "${matched#"$current-$kind-"}" =~ ^[0-9a-f]{40}$ ]] || fail
      if [[ "$matched" == "$primary" ]]; then [[ "$hit" == true ]] || fail;
      else [[ "$hit" == false ]] || fail; fi
    else
      [[ "$hit" == '' || "$hit" == false ]] || fail
    fi
  done
}
validate_archive() {
  local bundle=$1 prefix=$2 entry
  [[ -f "$bundle" && ! -L "$bundle" ]] || fail
  # Materialize listings so tar failures cannot disappear in process substitution.
  tar -tvf "$bundle" > "$FRESHEN_GO_ROOT/list.types" || fail
  awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" {exit 1}' "$FRESHEN_GO_ROOT/list.types" || fail
  tar -tf "$bundle" > "$FRESHEN_GO_ROOT/list.names" || fail
  while IFS= read -r entry; do
    [[ "$entry" == manifest || "$entry" == "$prefix" || "$entry" == "$prefix/"* ]] || fail
    [[ "$entry" != *'/../'* && "$entry" != */.. && "$entry" != *\\* && "$entry" != *':'* && "$entry" != *'//'* && "$entry" != *'/./'* && "$entry" != */. && "$entry" != *$'\r'* ]] || fail
  done < "$FRESHEN_GO_ROOT/list.names"
}
case "${1:-}" in
  prepare)
    umask 077
    root=$(mktemp -d "$(temp_path)/freshen-go.XXXXXXXX")
    mkdir -p "$root/mod/cache/download" "$root/build" "$root/gopath" "$root/xdg"
    [[ ! -e .freshen-ci-cache && ! -L .freshen-ci-cache ]] || fail
    mkdir .freshen-ci-cache
    identity > "$root/identity"
    {
      printf 'FRESHEN_GO_ROOT=%s\n' "$root"
      printf 'GOMODCACHE=%s\nGOCACHE=%s\nGOPATH=%s\nXDG_CACHE_HOME=%s\n' "$(go_path "$root/mod")" "$(go_path "$root/build")" "$(go_path "$root/gopath")" "$(go_path "$root/xdg")"
    } >> "$GITHUB_ENV"
    {
      for kind in modules build; do
        printf '%s=%s-%s-%s\n' "$kind" "$(< "$root/identity")" "$kind" "$GITHUB_SHA"
        # Only revision may fall back. Toolchain, locks and source policy stay exact.
        printf '%s-prefix=%s-%s-\n' "$kind" "$(< "$root/identity")" "$kind"
      done
    } >> "$GITHUB_OUTPUT"
    ;;
  check) check ;;
  restore)
    check
    for kind in modules build; do
      bundle=".freshen-ci-cache/$kind.tar"
      matched=$BUILD_MATCHED prefix=build
      [[ "$kind" != modules ]] || { matched=$MODULE_MATCHED; prefix=mod/cache/download; }
      if [[ -n "$matched" ]]; then
        validate_archive "$bundle" "$prefix"
        staging=$(mktemp -d "$FRESHEN_GO_ROOT/import.XXXXXXXX")
        tar -xf "$bundle" -C "$staging" || fail
        [[ "$(< "$staging/manifest")" == "$(< "$FRESHEN_GO_ROOT/identity")-$kind" && -d "$staging/$prefix" ]] || fail
        cp -R "$staging/$prefix/." "$FRESHEN_GO_ROOT/$prefix/"
        rm -rf "$staging"
      else
        [[ ! -e "$bundle" && ! -L "$bundle" ]] || fail
      fi
    done
    ;;
  publish)
    check
    modules=false build=false
    if [[ "$JOB_STATUS" == success && "$GITHUB_EVENT_NAME" == push && "$GITHUB_REF" == refs/heads/main ]]; then
      for kind in modules build; do
        hit=$BUILD_HIT prefix=build
        [[ "$kind" != modules ]] || { hit=$MODULE_HIT; prefix=mod/cache/download; }
        if [[ "$hit" != true ]]; then
          printf '%s-%s\n' "$(< "$FRESHEN_GO_ROOT/identity")" "$kind" > "$FRESHEN_GO_ROOT/manifest"
          tar -cf ".freshen-ci-cache/$kind.tar" -C "$FRESHEN_GO_ROOT" manifest "$prefix"
          [[ "$kind" != modules ]] || modules=true
          [[ "$kind" != build ]] || build=true
        fi
      done
    fi
    printf 'modules=%s\nbuild=%s\n' "$modules" "$build" >> "$GITHUB_OUTPUT"
    ;;
  *) echo 'usage: go-cache.sh prepare|check|restore|publish' >&2; exit 1 ;;
esac
