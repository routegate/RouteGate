#!/usr/bin/env bash
# Runs only inside the disposable PostgreSQL container created by CI.
set -Eeuo pipefail
[[ ${ROUTEGATE_DISPOSABLE_INSTALLER_TEST:-} == 1 && $(id -u) == 0 ]] || exit 2
# shellcheck source=install.sh
source /test/install.sh
[[ $(runuser -u postgres -- psql -X -qAt -c "SELECT count(*) FROM pg_database WHERE datname='routegate'") == 0 ]] || exit 2
ROUTEGATE_DB_PASSWORD=$(printf 'a%.0s' {1..64})
ROUTEGATE_RUNTIME_DB_PASSWORD=$(printf 'b%.0s' {1..64})
ROUTEGATE_INSTALL_PROMETHEUS=0
ROUTEGATE_DOMAIN=installer.invalid
ROUTEGATE_ADMIN_EMAIL=admin@installer.invalid
install -d -m 0755 /etc/routegate
runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 >/dev/null <<SQL
CREATE ROLE routegate LOGIN PASSWORD '${ROUTEGATE_DB_PASSWORD}';
CREATE DATABASE routegate OWNER routegate;
REVOKE ALL ON DATABASE routegate FROM PUBLIC;
SQL
# Refuse a fixture that accepts a wrong password via TCP (initdb's default
# localhost trust rules otherwise bypass the role's SCRAM password entirely).
PGPASSWORD="$ROUTEGATE_DB_PASSWORD" psql -X -h 127.0.0.1 -U routegate -d routegate -w -qAt -c 'SELECT 1' >/dev/null
if PGPASSWORD=deliberately-wrong psql -X -h 127.0.0.1 -U routegate -d routegate -w -qAt -c 'SELECT 1' >/dev/null 2>&1; then
  printf 'Disposable PostgreSQL accepts an incorrect TCP password; authentication fixture is invalid.\n' >&2
  exit 1
fi
printf 'Disposable PostgreSQL enforces TCP password authentication.\n'
initialize_manager_schema "$ROUTEGATE_DB_PASSWORD"
prepare_manager_runtime_login
write_manager_environment "$ROUTEGATE_DB_PASSWORD" 'disposable-admin-password-for-ci-only'
[[ $(stat -c '%u:%a' "$ROUTEGATE_MIGRATOR_ENV") == 0:600 ]]
if grep -q "$ROUTEGATE_DB_PASSWORD" "$ROUTEGATE_MANAGER_ENV"; then exit 1; fi
role_oid=$(runuser -u postgres -- psql -X -qAt -c "SELECT oid FROM pg_roles WHERE rolname='routegate_runtime'")
prepare_manager_runtime_login
[[ $(runuser -u postgres -- psql -X -qAt -c "SELECT oid FROM pg_roles WHERE rolname='routegate_runtime'") == "$role_oid" ]]
# Wrong preserved credentials must not rotate/adopt the existing login.
if (ROUTEGATE_RUNTIME_DB_PASSWORD=$(printf 'c%.0s' {1..64}); prepare_manager_runtime_login) >/dev/null 2>&1; then exit 1; fi
prepare_manager_runtime_login
cd /opt/routegate-manager
# Read only the installer-generated Manager environment, not the owner file.
set -a
# shellcheck disable=SC1090
source "$ROUTEGATE_MANAGER_ENV"
set +a
/usr/local/bin/routegate-manager >/tmp/manager.log 2>&1 &
manager_pid=$!
trap 'kill "$manager_pid" 2>/dev/null || true' EXIT
healthy=0
for _ in {1..60}; do
  if (exec 3<>/dev/tcp/127.0.0.1/8080
      printf 'GET /api/admin/health HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3
      IFS= read -r -t 3 response <&3
      [[ $response == *' 200 '* ]]) 2>/dev/null; then
    healthy=1
    break
  fi
  kill -0 "$manager_pid" || { cat /tmp/manager.log; exit 1; }
  sleep 1
done
[[ $healthy == 1 ]] || { cat /tmp/manager.log; exit 1; }
printf 'Installer database lifecycle passed: fresh roles, resume, wrong-secret refusal, restricted Manager health.\n'
