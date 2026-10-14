#!/usr/bin/env bash
# Fails when a migration file that shipped in a release tag (vX.Y.Z) differs
# from the copy in that first tag, or was deleted. bun keys applied migrations
# on the numeric prefix alone, so an edit never runs on a database that already
# applied the file. v0.38.4 edited 027 after v0.38.1 shipped it, and every
# upgrade from v0.38.1..v0.38.3 failed to boot. Add a new migration instead.
#
# Needs the tags: in CI, check out with fetch-depth: 0.

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

readonly DIRS=(server/internal/db/postgres/migrations server/internal/db/sqlite/migrations)
# Tags before this one predate the freeze rule and rewrote released files.
readonly FIRST_TAG="${FROZEN_MIGRATIONS_FROM:-v0.38.0}"

tags=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V | awk -v first="$FIRST_TAG" '$0 == first { on = 1 } on')
if [[ -z "$tags" ]]; then
    echo "check-frozen-migrations: no tag from $FIRST_TAG on (shallow clone? fetch tags first)" >&2
    exit 2
fi

declare -A first_blob first_tag
for tag in $tags; do
    while read -r _ _ blob path; do
        [[ "$path" == *.sql ]] || continue
        if [[ -z "${first_blob[$path]:-}" ]]; then
            first_blob[$path]=$blob
            first_tag[$path]=$tag
        fi
    done < <(git ls-tree -r "$tag" -- "${DIRS[@]}")
done

errors=0
for path in "${!first_blob[@]}"; do
    if [[ ! -f "$path" ]]; then
        echo "error: $path shipped in ${first_tag[$path]} and was deleted" >&2
        errors=$((errors + 1))
        continue
    fi
    if [[ "$(git hash-object "$path")" != "${first_blob[$path]}" ]]; then
        echo "error: $path shipped in ${first_tag[$path]} and was edited since. Revert it (git show ${first_tag[$path]}:$path) and put the change in a new migration." >&2
        errors=$((errors + 1))
    fi
done

if [[ $errors -gt 0 ]]; then
    echo "check-frozen-migrations: $errors released migration file(s) changed" >&2
    exit 1
fi

echo "check-frozen-migrations: ${#first_blob[@]} released migration files unchanged"
