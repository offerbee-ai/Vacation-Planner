#!/usr/bin/env bash
# Runs ON the VM as root. Usage: deploy.sh <IMAGE_TAG>
set -euo pipefail

IMAGE_TAG="${1:?usage: deploy.sh <image-tag>}"
PLANNER_DIR="/opt/planner"
METADATA="http://metadata.google.internal/computeMetadata/v1"
PROJECT_ID="$(curl -sfS -H 'Metadata-Flavor: Google' "$METADATA/project/project-id")"
REGION="us-west1"
IMAGE_URI="${REGION}-docker.pkg.dev/${PROJECT_ID}/planner/backend"
SECRET_NAMES=(MAPS_CLIENT_API_KEY GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET \
  JWT_SIGNING_SECRET SENDGRID_API_KEY OPENAI_API_KEY GEONAMES_API_KEY \
  AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY APPLE_MAPS_PRIVATE_KEY)

token() {
  curl -sfS -H 'Metadata-Flavor: Google' \
    "$METADATA/instance/service-accounts/default/token" | jq -r .access_token
}

# 1. registry login with the VM service account
token | docker login -u oauth2accesstoken --password-stdin "https://${REGION}-docker.pkg.dev"

# 2. render .env = committed non-secret env + secrets from Secret Manager
ENV_FILE="${PLANNER_DIR}/.env"
PREV_TAG="$(grep -s '^IMAGE_TAG=' "$ENV_FILE" | cut -d= -f2- || true)"
TMP_ENV="$(mktemp "${PLANNER_DIR}/.env.XXXXXX")"
trap 'rm -f "$TMP_ENV"' EXIT
cat "${PLANNER_DIR}/env.production" > "$TMP_ENV"
{
  echo "IMAGE_URI=${IMAGE_URI}"
  echo "IMAGE_TAG=${IMAGE_TAG}"
} >> "$TMP_ENV"
ACCESS_TOKEN="$(token)"
for name in "${SECRET_NAMES[@]}"; do
  if ! value="$(curl -sfS -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    "https://secretmanager.googleapis.com/v1/projects/${PROJECT_ID}/secrets/${name}/versions/latest:access" \
    | jq -r .payload.data | base64 -d)"; then
    echo "failed to fetch secret: ${name}" >&2
    exit 1
  fi
  echo "${name}=${value}" >> "$TMP_ENV"
done
chmod 600 "$TMP_ENV"
# keep the last-known-good env until this deploy verifies, so rollback can
# restore configuration (not just the image tag)
if [ -f "$ENV_FILE" ]; then
  cp -p "$ENV_FILE" "${ENV_FILE}.prev"
fi
mv "$TMP_ENV" "$ENV_FILE"

# 3. pull and swap the web container (redis/caddy untouched unless their config changed)
cd "$PLANNER_DIR"
docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml pull web
docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml up -d --remove-orphans

# 4. verify
docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml ps

verify_web() {
  docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml \
    exec -T web wget -qO- http://localhost:10000/healthz >/dev/null 2>&1
}

# verify_with_retry ATTEMPTS: poll verify_web every 5s, fail after ATTEMPTS
verify_with_retry() {
  local max="$1" tries=0
  until verify_web; do
    tries=$((tries + 1))
    if [ "$tries" -ge "$max" ]; then
      return 1
    fi
    sleep 5
  done
}

echo "waiting for web to pass app-level verify..."
if ! verify_with_retry 12; then
  echo "DEPLOY VERIFY FAILED for ${IMAGE_URI}:${IMAGE_TAG}" >&2
  if [ -n "$PREV_TAG" ] && [ "$PREV_TAG" != "$IMAGE_TAG" ]; then
    echo "rolling back to ${PREV_TAG}" >&2
    # restore the complete last-known-good env (config and secrets, not just
    # the tag) — a failed deploy may have been caused by changed env values
    if [ -f "${ENV_FILE}.prev" ]; then
      cp -p "${ENV_FILE}.prev" "$ENV_FILE"
    else
      sed -i "s|^IMAGE_TAG=.*|IMAGE_TAG=${PREV_TAG}|" "$ENV_FILE"
    fi
    docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml up -d --remove-orphans
    if verify_with_retry 6; then
      echo "rollback to ${PREV_TAG} verified healthy" >&2
    else
      echo "ROLLBACK VERIFY FAILED - manual intervention required" >&2
    fi
  fi
  exit 1
fi
rm -f "${ENV_FILE}.prev"
docker image prune -af --filter "until=168h" || true
echo "deployed ${IMAGE_URI}:${IMAGE_TAG}"
