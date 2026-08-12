#!/usr/bin/env bash
# GCE startup script: install docker + jq + ops agent once; ensure dirs exist.
set -euo pipefail

mkdir -p /opt/planner /var/lib/planner/redis

# base tools install independently of docker, so a preinstalled docker can
# never mask a missing jq or unattended-upgrades
apt-get update
apt-get install -y ca-certificates curl gnupg jq unattended-upgrades

if ! command -v docker >/dev/null 2>&1; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  # shellcheck disable=SC1091
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
    https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
  systemctl enable --now docker
fi

if [ ! -f /etc/google-cloud-ops-agent/config.yaml ]; then
  curl -sSO https://dl.google.com/cloudagents/add-google-cloud-ops-agent-repo.sh
  bash add-google-cloud-ops-agent-repo.sh --also-install || true
  rm -f add-google-cloud-ops-agent-repo.sh
fi
