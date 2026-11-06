#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -eq 0 ]; then
    set -- make test-postgres
fi

if [ -n "${ATLANTIS_TEST_POSTGRES_DSN:-}" ]; then
    exec "$@"
fi

container_id=""
cleanup() {
    test_status=$?
    trap - EXIT
    if [ -n "$container_id" ]; then
        docker rm --force --volumes "$container_id" >/dev/null || echo "Could not remove test PostgreSQL container $container_id" >&2
    fi
    exit "$test_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Match the PostgreSQL service image in .github/workflows/test.yml.
postgres_image="postgres:17@sha256:3b0cda93be7c8dba2ed484d19393d93845dc3dafb88973e248ba8e6d99d784e1"
container_id=$(docker create \
    --label atlantis.test=postgres \
    --env POSTGRES_PASSWORD=postgres \
    --env POSTGRES_DB=atlantis_test \
    --publish 127.0.0.1::5432 \
    --health-cmd 'pg_isready -h 127.0.0.1 -U postgres -d atlantis_test' \
    --health-interval 1s --health-timeout 3s --health-retries 30 \
    "$postgres_image")
docker start "$container_id" >/dev/null

for ((attempt = 0; attempt < 60; attempt++)); do
    health=$(docker inspect --format '{{.State.Health.Status}}' "$container_id")
    case "$health" in
        healthy)
            binding=$(docker port "$container_id" 5432/tcp)
            export ATLANTIS_TEST_POSTGRES_DSN="postgresql://postgres:postgres@127.0.0.1:${binding##*:}/atlantis_test?sslmode=disable"
            "$@"
            exit 0
            ;;
        unhealthy) break ;;
    esac
    sleep 1
done

echo "Test PostgreSQL did not become ready" >&2
docker logs "$container_id" >&2
exit 1
