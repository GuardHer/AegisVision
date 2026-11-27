#!/usr/bin/env bash
set -euo pipefail

# Build all AegisVision platform & agent Docker images and export a single offline bundle
# for air-gapped factory intranet deployment.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_FILE="${1:-aegisvision-offline-images.tar.gz}"

echo "[1/3] Building platform Docker images..."
docker compose -f "${SCRIPT_DIR}/docker-compose.yml" build

echo "[2/3] Building compute-host aegis-agent image..."
docker compose -f "${SCRIPT_DIR}/../agent/docker-compose.yml" build

IMAGES=(
  "aegisvision/nginx:latest"
  "aegisvision/aegis-api:latest"
  "aegisvision/aegis-scheduler:latest"
  "aegisvision/aegis-ingest:latest"
  "aegisvision/aegis-ws:latest"
  "aegisvision/aegis-dispatcher:latest"
  "aegisvision/aegis-agent:latest"
  "postgres:16-alpine"
  "redis:7-alpine"
  "rabbitmq:3.13-management-alpine"
  "minio/minio:RELEASE.2025-04-22T22-12-26Z"
  "minio/mc:RELEASE.2025-04-16T18-13-26Z"
  "bluenviron/mediamtx:1.12.2"
)

echo "[3/3] Exporting ${#IMAGES[@]} images to ${OUT_FILE}..."
docker save "${IMAGES[@]}" | gzip > "${OUT_FILE}"
echo "Done. On air-gapped target server run: gunzip -c ${OUT_FILE} | docker load"
