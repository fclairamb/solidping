#!/usr/bin/env bash
# Upgrade-path test (spec 2026-10-17-01): a released version fills a database,
# then the HEAD binary starts on the database it left behind, the way an
# operator upgrades.
#
#   scripts/upgrade-test.sh <sqlite|postgres> <from-tag>   run one leg
#   scripts/upgrade-test.sh --matrix                       print "<engine> <from-tag>" lines
#
# Env:
#   HEAD_BIN   the HEAD server binary (default: ./solidping, see `make test-upgrade`)
#   SKIP_TAGS  space-separated tags left out of the matrix (default: SKIP_TAGS below)
#   PG_IMAGE   Postgres image for the old and new side (default: docker-compose.yml's)
#
# Needs: bash, docker, curl, jq, git (matrix), sqlite3 (sqlite engine).

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

# Tags that are known bad and left out of the matrix. v0.38.4 edited the released
# migration 027; the recovery is covered by migration_028_split_027_test.go.
SKIP_TAGS_DEFAULT="v0.38.4"
read -r -a skip_tags <<<"${SKIP_TAGS-$SKIP_TAGS_DEFAULT}"

readonly IMAGE_REPO="ghcr.io/fclairamb/solidping"
readonly WARMUP_SECONDS=30
readonly HEALTH_TIMEOUT=60
readonly RESULT_TIMEOUT=90

usage() {
    echo "usage: $0 <sqlite|postgres> <from-tag> | --matrix" >&2
    exit 2
}

# matrix prints the tags to upgrade from: the 3 most recent, plus the newest tag
# of the previous minor (a longer jump), minus the skip list.
matrix() {
    local tags tag minor newest_minor prev
    tags=$(git -C "$root" tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V)
    for tag in "${skip_tags[@]}"; do
        tags=$(grep -vxF "$tag" <<<"$tags" || true)
    done
    [[ -n "$tags" ]] || { echo "upgrade-test: no tag (shallow clone? fetch tags first)" >&2; exit 2; }

    local picked
    picked=$(tail -n 3 <<<"$tags")
    newest_minor=$(tail -n 1 <<<"$tags" | cut -d. -f1,2)
    prev=""
    while read -r tag; do
        minor=$(cut -d. -f1,2 <<<"$tag")
        [[ "$minor" != "$newest_minor" ]] && prev="$tag"
    done <<<"$tags"
    # The tag list is sorted, so the last line of another minor is the newest of the previous one.
    picked=$(printf '%s\n%s\n' "$prev" "$picked" | sed '/^$/d' | sort -uV)

    local engine
    while read -r tag; do
        for engine in sqlite postgres; do
            echo "$engine $tag"
        done
    done <<<"$picked"
}

if [[ "${1:-}" == "--matrix" ]]; then
    matrix
    exit 0
fi

[[ $# -eq 2 ]] || usage
engine=$1
from_tag=$2
[[ "$engine" == "sqlite" || "$engine" == "postgres" ]] || usage

head_bin="${HEAD_BIN:-$root/solidping}"
[[ -x "$head_bin" ]] || { echo "upgrade-test: HEAD binary $head_bin not found (make test-upgrade builds it)" >&2; exit 2; }
if [[ "$engine" == "sqlite" ]]; then
    command -v sqlite3 >/dev/null || { echo "upgrade-test: sqlite3 is required" >&2; exit 2; }
fi

pg_image="${PG_IMAGE:-$(sed -n 's/^ *image: *\(postgres:[^ ]*\).*/\1/p' "$root/docker-compose.yml" | head -n 1)}"
# 18.6 is what docker-compose.yml and docker-compose.build.yml run (no Helm chart
# in this repo); the fallback only matters if the sed finds nothing.
pg_image="${pg_image:-postgres:18.6}"

suffix="$$-$RANDOM"
workdir=$(mktemp -d)
network="sp-upgrade-$suffix"
old_name="sp-upgrade-old-$suffix"
pg_name="sp-upgrade-pg-$suffix"
new_pid=""
new_log="$workdir/new.log"

cleanup() {
    local code=$?
    [[ -n "$new_pid" ]] && kill "$new_pid" 2>/dev/null || true
    docker rm -f "$old_name" "$pg_name" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    rm -rf "$workdir"
    exit "$code"
}
trap cleanup EXIT

step() { echo "==> [$engine $from_tag] $*"; }

fail() {
    echo "FAIL [$engine $from_tag]: $*" >&2
    if [[ -s "$new_log" ]]; then
        echo "--- last 100 lines of the HEAD server log ---" >&2
        tail -n 100 "$new_log" >&2
    fi
    exit 1
}

# wait_health <port> <seconds>: succeeds once /api/mgmt/health answers ok.
wait_health() {
    local port=$1 deadline=$((SECONDS + $2))
    while ((SECONDS < deadline)); do
        if [[ "$(curl -sf "http://127.0.0.1:$port/api/mgmt/health" 2>/dev/null | jq -r .status 2>/dev/null)" == "ok" ]]; then
            return 0
        fi
        sleep 1
    done
    return 1
}

free_port() {
    local port
    for _ in $(seq 50); do
        port=$((20000 + RANDOM % 10000))
        if ! (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
            echo "$port"
            return 0
        fi
    done
    return 1
}

api() { # api <port> <token> <path>
    curl -sf -H "Authorization: Bearer $2" "http://127.0.0.1:$1/api/v1/orgs/test/$3"
}

# integrations is the current path; older tags knew it as channels or connections.
count_integrations() {
    local port=$1 token=$2 path out
    for path in integrations channels connections; do
        if out=$(api "$port" "$token" "$path?limit=1000" 2>/dev/null); then
            jq '.data | length' <<<"$out"
            return 0
        fi
    done
    echo 0
}

# snapshot <port> prints "name=count" lines plus the newest raw result.
snapshot() {
    local port=$1 token name
    token=$(curl -sf -X POST -H 'Content-Type: application/json' \
        -d '{"org":"test","email":"test@test.com","password":"test"}' \
        "http://127.0.0.1:$port/api/v1/auth/login" | jq -r .accessToken)
    [[ -n "$token" && "$token" != null ]] || { echo "login failed" >&2; return 1; }
    for name in checks incidents status-pages members; do
        echo "$name=$(api "$port" "$token" "$name?limit=1000" | jq '.data | length')"
    done
    echo "integrations=$(count_integrations "$port" "$token")"
    api "$port" "$token" "results?periodType=raw&limit=1" |
        jq -r '.data[0] | "lastCheck=\(.checkUid)\nlastPeriodStart=\(.periodStart)"'
}

latest_period_start() { # latest_period_start <port> <check uid>
    local token
    token=$(curl -sf -X POST -H 'Content-Type: application/json' \
        -d '{"org":"test","email":"test@test.com","password":"test"}' \
        "http://127.0.0.1:$1/api/v1/auth/login" | jq -r .accessToken)
    api "$1" "$token" "results?checkUid=$2&periodType=raw&limit=1" | jq -r '.data[0].periodStart // empty'
}

# Env shared by the old and the new side. Test mode would otherwise pick
# sqlite-memory; SP_DB_RESET is never set.
common_env=(
    SP_RUNMODE=test
    SP_DB_TYPE="$engine"
    SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE=0
    SP_SERVER_RATE_LIMITING_MAX_CONCURRENT=0
)

old_args=()
new_env=()
if [[ "$engine" == "sqlite" ]]; then
    datadir="$workdir/data"
    mkdir -p "$datadir"
    # The distroless image runs as uid 65532; run as the caller so the HEAD
    # binary can open the files the old one created.
    old_args+=(--user "$(id -u):$(id -g)" -v "$datadir:/data" -e "SP_DB_DIR=/data")
    new_env+=("SP_DB_DIR=$datadir" "SP_FILESTORAGE_LOCAL_ROOT=$datadir/files")
else
    step "starting $pg_image"
    docker network create "$network" >/dev/null
    docker run -d --name "$pg_name" --network "$network" -p 127.0.0.1::5432 \
        -e POSTGRES_USER=solidping -e POSTGRES_PASSWORD=solidping -e POSTGRES_DB=solidping \
        "$pg_image" >/dev/null
    # Probe over TCP: the image's init phase runs a temporary server that listens
    # on the unix socket only, so a socket probe succeeds just before it restarts.
    for _ in $(seq 60); do
        docker exec "$pg_name" pg_isready -h 127.0.0.1 -U solidping -d solidping >/dev/null 2>&1 && break
        sleep 1
    done
    docker exec "$pg_name" pg_isready -h 127.0.0.1 -U solidping -d solidping >/dev/null || fail "postgres did not start"
    pg_port=$(docker port "$pg_name" 5432/tcp | head -n 1 | sed 's/.*://')
    old_args+=(--network "$network" -e "SP_DB_URL=postgres://solidping:solidping@$pg_name:5432/solidping?sslmode=disable")
    new_env+=("SP_DB_URL=postgres://solidping:solidping@127.0.0.1:$pg_port/solidping?sslmode=disable")
fi

# ---- Old side -------------------------------------------------------------
step "starting $IMAGE_REPO:${from_tag#v}"
old_env_args=()
for kv in "${common_env[@]}"; do old_env_args+=(-e "$kv"); done
docker run -d --name "$old_name" -p 127.0.0.1::4000 "${old_args[@]}" "${old_env_args[@]}" \
    "$IMAGE_REPO:${from_tag#v}" >/dev/null
old_port=$(docker port "$old_name" 4000/tcp | head -n 1 | sed 's/.*://')
wait_health "$old_port" "$HEALTH_TIMEOUT" || {
    docker logs --tail 100 "$old_name" >&2 || true
    fail "old version $from_tag did not become healthy"
}
step "letting $from_tag run its checks for ${WARMUP_SECONDS}s"
sleep "$WARMUP_SECONDS"

step "snapshot"
before=$(snapshot "$old_port") || fail "could not snapshot the old version"
echo "$before"
last_check=$(sed -n 's/^lastCheck=//p' <<<"$before")
last_start=$(sed -n 's/^lastPeriodStart=//p' <<<"$before")
[[ -n "$last_check" && "$last_check" != null ]] || fail "the old version wrote no result"

step "stopping the old version (SIGTERM)"
docker stop "$old_name" >/dev/null

# ---- New side -------------------------------------------------------------
new_port=$(free_port)
step "starting HEAD on :$new_port"
(
    env "${common_env[@]}" "${new_env[@]}" SP_SERVER_LISTEN="127.0.0.1:$new_port" \
        "$head_bin" serve >"$new_log" 2>&1
) &
new_pid=$!

wait_health "$new_port" "$HEALTH_TIMEOUT" || {
    # A migration that failed must not be recorded as applied: show the tail.
    echo "--- bun_migrations (last 5) after the failed HEAD boot ---" >&2
    if [[ "$engine" == "sqlite" ]]; then
        sqlite3 "$(find "$datadir" -maxdepth 1 -name '*.db' | head -n 1)" "select name, group_id from bun_migrations order by id desc limit 5" >&2 || true
    else
        docker exec "$pg_name" psql -U solidping -d solidping -Atc "select name, group_id from bun_migrations order by id desc limit 5" >&2 || true
    fi
    fail "HEAD did not become healthy within ${HEALTH_TIMEOUT}s"
}

for needle in "half-migrated" "migration content changed"; do
    if grep -qi "$needle" "$new_log"; then
        fail "the HEAD log contains \"$needle\""
    fi
done

# ---- Assertions -----------------------------------------------------------
step "every HEAD migration is recorded"
mig_dir="$root/server/internal/db/$engine/migrations"
head_migs=$(find "$mig_dir" -name '*.up.sql' -exec basename {} \; | sed -n 's/^\([0-9][0-9]*\)_.*\.up\.sql$/\1/p' | sort -u)
if [[ "$engine" == "sqlite" ]]; then
    db=$(find "$datadir" -maxdepth 1 -name '*.db' | head -n 1)
    applied=$(sqlite3 "$db" "select name from bun_migrations")
else
    applied=$(docker exec "$pg_name" psql -U solidping -d solidping -Atc "select name from bun_migrations")
fi
for mig in $head_migs; do
    grep -qx "$mig" <<<"$applied" || fail "migration $mig is not listed in bun_migrations"
done

step "counts are unchanged"
after=$(snapshot "$new_port") || fail "could not snapshot HEAD"
diff <(grep -v '^last' <<<"$before") <(grep -v '^last' <<<"$after") ||
    fail "the counts changed across the upgrade (before < > after)"

step "the scheduler writes new results"
deadline=$((SECONDS + RESULT_TIMEOUT))
fresh=""
while ((SECONDS < deadline)); do
    now=$(latest_period_start "$new_port" "$last_check" || true)
    if [[ -n "$now" && "$now" != "$last_start" ]]; then
        fresh=$now
        break
    fi
    sleep 3
done
[[ -n "$fresh" ]] || fail "no result newer than $last_start for check $last_check within ${RESULT_TIMEOUT}s"

if [[ "$engine" == "sqlite" ]]; then
    old_max=$(git -C "$root" ls-tree --name-only "$from_tag" server/internal/db/sqlite/migrations/ |
        sed -n 's#.*/\([0-9][0-9]*\)_.*\.up\.sql$#\1#p' | sort -u | tail -n 1)
    head_max=$(tail -n 1 <<<"$head_migs")
    if [[ "$head_max" > "$old_max" ]]; then
        step "pre-upgrade backup"
        compgen -G "$datadir/*.pre-*.bak" >/dev/null || fail "no solidping.db.pre-<migration>.bak in the data dir"
    fi
fi

echo "OK [$engine $from_tag]"
