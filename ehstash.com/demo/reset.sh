#!/usr/bin/env bash
# Nuke the demo state — drops the docker volume and removes extracted thumbs.

set -euo pipefail

cd "$(dirname "$0")"

echo "==> stopping & removing docker services + volumes"
docker compose down -v

echo "==> removing extracted thumbs"
rm -rf thumbs

echo "==> done. Run ./setup.sh to rebuild."
