#!/usr/bin/env bash
# Ships the working tree (tracked and new files, minus .gitignore'd ones) to
# the VPS, builds the image there and (re)starts ArchSim. Run on your machine:
#
#   deploy/deploy.sh
#
# The VPS needs no GitHub access: the code goes over SSH as a tar stream.
# deploy/.env and deploy/data/ on the VPS are not in git, so a deploy never
# touches them.
set -euo pipefail

HOST=${ARCHSIM_HOST:-sumoagent}           # ssh config alias
DIR=${ARCHSIM_DIR:-projects/archsim}      # relative to the remote home
URL=${ARCHSIM_URL:-https://archsim.gemps.my.id}

# A dropped connection fails within a minute instead of hanging.
ssh() { command ssh -o ServerAliveInterval=15 -o ServerAliveCountMax=4 "$@"; }

cd "$(git rev-parse --show-toplevel)"
rev=$(git rev-parse --short HEAD)
[ -z "$(git status --porcelain)" ] || rev="$rev+uncommitted"

echo "shipping $rev to $HOST:$DIR"
# Files deleted locally but not yet committed are skipped. COPYFILE_DISABLE
# keeps macOS tar from adding ._ metadata files.
git ls-files -z --cached --others --exclude-standard |
  while IFS= read -r -d '' f; do [ ! -e "$f" ] || printf '%s\0' "$f"; done |
  COPYFILE_DISABLE=1 tar --null -T - -cf - |
  ssh "$HOST" "mkdir -p '$DIR' && tar -x -C '$DIR' --warning=no-unknown-keyword && echo '$rev' >'$DIR/REVISION'"

ssh "$HOST" "DIR='$DIR' bash -s" <<'REMOTE'
set -euo pipefail
cd "$DIR/deploy"
# The container runs as distroless nonroot (65532) and owns the data.
mkdir -p data && sudo -n chown 65532:65532 data
docker compose build
if ! grep -q '^ARCHSIM_PASSWORD_HASH=..' .env 2>/dev/null; then
  echo "built, but not started: set the login first with deploy/set-password.sh on the VPS" >&2
  exit 3
fi
docker compose up -d
docker image prune -f >/dev/null
REMOTE

for _ in $(seq 1 30); do
  if curl -fsS -o /dev/null "$URL/healthz"; then
    echo "up: $URL"
    exit 0
  fi
  sleep 2
done
echo "deployed, but $URL/healthz does not answer yet (check: ssh $HOST 'cd $DIR/deploy && docker compose logs')" >&2
exit 1
