# Deploying ArchSim

ArchSim runs as one container (`archsim:local`, built from the root
`Dockerfile`) on a VPS that already has a Caddy container terminating TLS for
other sites. The container joins Caddy's Docker network as `archsim:8080` and
publishes no host port.

| What | Where (on the VPS) |
| --- | --- |
| Code (shipped by `deploy.sh`) | `~/projects/archsim` |
| Login config (not in git) | `~/projects/archsim/deploy/.env` |
| Saved designs | `~/projects/archsim/deploy/data/` |
| Backups | `~/backups/archsim/` |

## First time

1. On the VPS, create `deploy/.env` (mode 600):

   ```sh
   umask 077
   cat > ~/projects/archsim/deploy/.env <<EOF
   CADDY_NETWORK=medic-quickcomm_default
   ARCHSIM_USERNAME=rizky
   ARCHSIM_SESSION_SECRET=$(openssl rand -hex 32)
   EOF
   ```

   `CADDY_NETWORK` is the network of the existing Caddy container
   (`docker inspect <caddy> --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}'`).

2. From your machine: `deploy/deploy.sh`. It builds the image and stops
   before starting, since there is no password yet.
3. Set the password: on the VPS, run `~/projects/archsim/deploy/set-password.sh`.
4. Add `Caddyfile.snippet` to the existing Caddy and reload it. That Caddyfile
   is bind-mounted as a single file, so append to it in place (`cat >>`): an
   editor that replaces the file leaves the container reading the old one.

   ```sh
   cd ~/medic-quickcomm
   cp Caddyfile Caddyfile.bak-$(date +%F-%H%M)
   cat ~/projects/archsim/deploy/Caddyfile.snippet >> Caddyfile
   docker exec medic-quickcomm-caddy-1 caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
   docker exec medic-quickcomm-caddy-1 caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
   ```

## Updating

`deploy/deploy.sh` from the repo root. It ships the working tree (including
uncommitted changes, which it notes in `~/projects/archsim/REVISION`), rebuilds
and restarts, then waits for `/healthz`. The build uses the VPS's CPU for a few
minutes.

## Backups

`deploy/backup.sh` archives `data/` into `~/backups/archsim` and keeps the newest
14. Daily at 03:00 UTC (`crontab -e` on the VPS):

```
0 3 * * * $HOME/projects/archsim/deploy/backup.sh >/dev/null
```

Restore: stop the container, extract an archive in `deploy/` with `sudo tar -xzf`,
start it again. Copy archives off the VPS now and then; a backup on the same disk
does not survive losing the VPS.
