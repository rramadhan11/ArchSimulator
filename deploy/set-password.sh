#!/usr/bin/env bash
# Sets (or changes) the login password. Run ON the VPS, in a real terminal,
# after the first deploy has built the image:
#
#   ~/projects/archsim/deploy/set-password.sh      (on the VPS)
#
# The password is read here, piped to the server's hash-password command and
# only its bcrypt hash is stored in deploy/.env. Changing it does not sign out existing
# sessions; rotate ARCHSIM_SESSION_SECRET for that.
set -euo pipefail
cd "$(dirname "$0")"

[ -f .env ] || { echo "deploy/.env is missing (see deploy/README.md)" >&2; exit 1; }
[ -t 0 ] || { echo "run this in an interactive terminal" >&2; exit 1; }
read -r -s -p "New password (min 12 characters): " pw; echo
read -r -s -p "Repeat it: " pw2; echo
[ "$pw" = "$pw2" ] || { echo "passwords differ" >&2; exit 1; }
# Piped, not typed into a container TTY: a TTY merges the prompt into the
# captured output.
hash=$(printf '%s\n' "$pw" | docker run --rm -i --network none --entrypoint /app/archsim-api archsim:local hash-password)
unset pw pw2
case $hash in
  '$2'*) ;;
  *) echo "no hash produced" >&2; exit 1 ;;
esac

# Single quotes keep Compose from reading the hash's $ signs as variables.
# Rewritten in place (not replaced) so the file keeps its owner and mode.
tmp=$(mktemp)
grep -v '^ARCHSIM_PASSWORD_HASH=' .env >"$tmp" || true
printf "ARCHSIM_PASSWORD_HASH='%s'\n" "$hash" >>"$tmp"
cat "$tmp" >.env
rm -f "$tmp"

docker compose up -d
echo "password set; ArchSim (re)started"
