#!/usr/bin/env bash
# Run the notification real-persistence suite against a real InnoDB server
# (MySQL 8.0 or MariaDB 10.11+) using independent TCP connections.
#
# The suite is skipped by `go test` unless GIGMATCH_TEST_MYSQL=1 is set.
# No SQLite, mocks, single-connection pools or in-memory doubles are used.
#
# Usage:
#   # Against the project's own MySQL 8.0 container (recommended):
#   docker compose up -d db
#   GIGMATCH_TEST_MYSQL_DSN='root:root_pwd@tcp(127.0.0.1:33301)/?parseTime=true&charset=utf8mb4&loc=Local' \
#     backend/scripts/run_real_persistence_tests.sh
#
#   # Or any running MySQL-compatible server:
#   GIGMATCH_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/?parseTime=true&charset=utf8mb4&loc=Local' \
#     backend/scripts/run_real_persistence_tests.sh
set -euo pipefail

export GIGMATCH_TEST_MYSQL=1
export GIGMATCH_TEST_MYSQL_DSN="${GIGMATCH_TEST_MYSQL_DSN:-root:@tcp(127.0.0.1:3310)/?parseTime=true&charset=utf8mb4&loc=Local}"

cd "$(dirname "$0")/.."
echo "Using DSN: ${GIGMATCH_TEST_MYSQL_DSN}"
go test -race -count=1 -v ./internal/service/ -run TestRealMySQL
