#!/usr/bin/env bash
set -euo pipefail

image="${1:-mt-server:ci}"
port="${MT_MIGRATION_SMOKE_PORT:-18081}"
run_id="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-$$"
container="mt-server-migration-${run_id}"
volume="mt-server-migration-${run_id}"
helper_image="mt-server-testfixture:${run_id}"
fixture_directory="$(mktemp -d)"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  docker image rm "$helper_image" >/dev/null 2>&1 || true
  rm -rf -- "$fixture_directory"
}
trap cleanup EXIT

CGO_ENABLED=0 go build -trimpath -o "$fixture_directory/testfixture" \
  ./internal/platform/state/testfixture
tar -C "$fixture_directory" -c testfixture | docker import \
  --change 'ENTRYPOINT ["/testfixture"]' - "$helper_image" >/dev/null
docker volume create "$volume" >/dev/null
docker run --rm --mount "type=volume,source=$volume,target=/state" \
  "$helper_image" write-volume /state

docker run -d --name "$container" \
  --read-only \
  --user 65532:65532 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -p "127.0.0.1:${port}:8080" \
  -e MT_ADMIN_ALLOW_INSECURE_HTTP=true \
  -e MT_STATE_DIR=/var/lib/mt-server \
  --mount "type=volume,source=$volume,target=/var/lib/mt-server" \
  "$image" >/dev/null

for _ in $(seq 1 30); do
  if curl --fail --silent "http://127.0.0.1:${port}/health/ready" >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent "http://127.0.0.1:${port}/health/ready" >/dev/null

docker run --rm --mount "type=volume,source=$volume,target=/state" \
  "$helper_image" verify-migrated /state

docker stop "$container" >/dev/null
docker run --rm \
  --read-only \
  --user 65532:65532 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -e MT_STATE_DIR=/var/lib/mt-server \
  --mount "type=volume,source=$volume,target=/var/lib/mt-server" \
  "$image" state restore-v3-backup >/dev/null

docker run --rm --mount "type=volume,source=$volume,target=/state" \
  "$helper_image" verify-restored /state
