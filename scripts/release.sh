#!/usr/bin/env bash
# Release every module of the repository: scripts/release.sh v0.1.0
set -euo pipefail

version=${1:?usage: scripts/release.sh v0.1.0}
module=github.com/yaboylino/gonestly
libraries=(validator sql jwt jobs password)

fail() { echo "release: $*" >&2; exit 1; }
step() { printf '\n==> %s (%ss)\n' "$1" "$SECONDS"; }

cd "$(git rev-parse --show-toplevel)"
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "$version is not a version such as v0.1.0"
[[ $(git branch --show-current) == main ]] || fail "release from the main branch"
[[ -z $(git status --porcelain) ]] || fail "the working tree has uncommitted changes"
git fetch --quiet --tags origin main
[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || fail "main differs from origin/main"
[[ -z $(git tag --list "$version") ]] || fail "$version is already tagged"
grep -q '^## \[Unreleased\]$' CHANGELOG.md || fail "CHANGELOG.md has no Unreleased section"

step "Checking"
make check

step "Releasing the framework and ${libraries[*]}"
date=$(date -u +%Y-%m-%d)
perl -0pi -e "s/^## \[Unreleased\]\$/## [Unreleased]\n\n## [${version#v}] - $date/m" CHANGELOG.md
for library in "${libraries[@]}"; do
	(cd "$library" && go mod edit -require="$module@$version")
done
(cd jobs && go mod edit -require="$module/sql@$version")
git commit --quiet --all --message "chore: release $version"
tags=("$version")
git tag --annotate "$version" --message "$version"
for library in "${libraries[@]}"; do
	git tag --annotate "$library/$version" --message "$library/$version"
	tags+=("$library/$version")
done
git push --quiet origin main "${tags[@]}"

# The CLI is installed with go install, which refuses replace directives, so it requires the
# versions that were just published.
step "Waiting for the module proxy"
for path in "$module" "$module/sql" "$module/validator" "$module/jobs"; do
	until GOWORK=off GOFLAGS=-mod=mod GOPROXY=https://proxy.golang.org go list -m "$path@$version" >/dev/null 2>&1; do
		sleep 5
	done
done

step "Releasing the CLI and the example"
(
	cd cmd/gonestly
	go mod edit -dropreplace="$module" -dropreplace="$module/sql" -dropreplace="$module/jobs" -require="$module@$version" -require="$module/sql@$version" -require="$module/jobs@$version"
	GOWORK=off go mod tidy
	GOWORK=off go build ./...
)
(
	cd examples/tasks
	go mod edit -require="$module@$version" -require="$module/sql@$version" -require="$module/validator@$version"
	GOWORK=off go mod tidy
)
git commit --quiet --all --message "chore(cli): release $version"
git tag --annotate "cmd/gonestly/$version" --message "cmd/gonestly/$version"
git push --quiet origin main "cmd/gonestly/$version"

step "Released $version; the release workflow now publishes the binaries"
