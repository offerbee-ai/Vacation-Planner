# GCP Dedicated-VM Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy the planner backend to a single GCE VM (Caddy TLS + app container + colocated persistent Redis), with push-to-main CI deploys via Workload Identity Federation, replacing Heroku.

**Architecture:** One Debian 12 e2-small VM runs a 3-container compose stack: `caddy` (443/80, auto-TLS) → `web` (app image from Artifact Registry, port 10000) → `redis` (AOF-persistent, data on the VM disk, never published to the internet). Secrets live in Secret Manager and are rendered to `/opt/planner/.env` at deploy time by `deploy.sh`. GitHub Actions authenticates via WIF (no JSON keys), builds/pushes the image, and runs `deploy.sh` on the VM over IAP SSH.

**Tech Stack:** Go 1.24, Docker + Compose v2, Caddy 2, Redis 7 (AOF), GCE (Debian 12), Artifact Registry, Secret Manager, IAP, GitHub Actions with `google-github-actions/auth` WIF.

## Global Constraints

- Go version: `1.24.0` (from `go.mod`); build image `golang:1.24-alpine`, runtime image `alpine:3.20`.
- The runtime container MUST include `config/` and `assets/` — read at startup (`main.go:79`, `planner/planner.go:157-158,1808-1809`).
- App listens on `PORT` (default `10000`); graceful shutdown on SIGTERM already implemented.
- Redis is the PRIMARY datastore: `appendonly yes`, `appendfsync everysec`, `maxmemory-policy noeviction`. Never expose it off the VM.
- No new Go dependencies; no application-code changes in this plan.
- All GCP infra created by `deploy/gcp-bootstrap.sh` — idempotent (safe to rerun), no clicking in console except OAuth redirect URI.
- CI auth is WIF only. Never create or download a service-account JSON key.
- Commits: conventional style (`ci:`, `feat:`, `docs:`), each ending with the session trailer used in this repo.
- Shell scripts: `bash`, `set -euo pipefail`, must pass `shellcheck` and `bash -n`.

### Naming registry (used verbatim across all tasks)

```bash
# deployment parameters — the ONLY values the operator edits, set in deploy/gcp-bootstrap.sh header
PROJECT_ID="<your-gcp-project>"          # fill in before Task 5
REGION="us-central1"
ZONE="us-central1-a"
DOMAIN="<api domain, e.g. api.offerbee.ai>"  # fill in before Task 5

# derived / fixed names — never edit
VM_NAME="planner-vm"
AR_REPO="planner"
IMAGE_URI="${REGION}-docker.pkg.dev/${PROJECT_ID}/planner/backend"
RUNTIME_SA="planner-vm-sa@${PROJECT_ID}.iam.gserviceaccount.com"
DEPLOYER_SA="github-deployer@${PROJECT_ID}.iam.gserviceaccount.com"
WIF_POOL="github"
WIF_PROVIDER="github-provider"
STATIC_IP_NAME="planner-ip"
```

- VM paths: stack lives in `/opt/planner/` (`docker-compose.prod.yml`, `Caddyfile`, `deploy.sh`, `env.production`, generated `.env`); Redis data in `/var/lib/planner/redis/`.
- Secret Manager secret names (exact, used by bootstrap AND deploy.sh — the two lists must stay identical):
  `MAPS_CLIENT_API_KEY GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET JWT_SIGNING_SECRET SENDGRID_API_KEY OPENAI_API_KEY GEONAMES_API_KEY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY`
- Non-secret env (committed in `deploy/env.production`): `ENVIRONMENT=production`, `PORT=10000`, `REDIS_URL=redis://redis:6379`, `DOMAIN`, `FRONTEND_URL`, `BACKEND_URL`, `ADMIN_USERS`, `MAILER_EMAIL_ADDRESS`, `BLOB_BUCKET_ID`, `AWS_REGION`.
- GitHub repo variables (Task 8): `GCP_PROJECT`, `GCP_WIF_PROVIDER`, `GCP_DEPLOYER_SA`, `GCP_ZONE`, `GCP_VM`, `GCP_IMAGE`.

---

### Task 1: Multi-stage Dockerfile (unbreak the image)

**Files:**
- Modify: `Dockerfile`
- Test: local `docker build` + compose smoke (no Go test changes)

**Interfaces:**
- Consumes: nothing.
- Produces: image tagged locally as `planner-backend:dev`; binary at `/app/vacation-planner`, workdir `/app` containing `config/` and `assets/`. All later tasks assume this image layout and that the container serves `GET /` with HTTP 200 on port 10000.

- [ ] **Step 1: Branch off fresh main**

```bash
git fetch origin main
git checkout -b feat/gcp-vm-deploy origin/main
```

- [ ] **Step 2: Verify the current Dockerfile is broken (failing baseline)**

Run: `docker build -t planner-backend:dev .`
Expected: FAIL — `go.mod requires go >= 1.24.0 (running go 1.21.x)`

- [ ] **Step 3: Replace Dockerfile**

```dockerfile
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/vacation-planner .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/vacation-planner ./vacation-planner
COPY config/ ./config/
COPY assets/ ./assets/
EXPOSE 10000
CMD ["./vacation-planner"]
```

- [ ] **Step 4: Verify build passes**

Run: `docker build -t planner-backend:dev .`
Expected: PASS, final image based on alpine:3.20.

- [ ] **Step 5: Smoke test with dev compose (build key not needed for boot)**

```bash
docker compose up -d --build
sleep 3
curl -sf -o /dev/null -w '%{http_code}\n' http://localhost:10000/
docker compose down
```

Expected: `200`.

- [ ] **Step 6: Commit**

```bash
git add Dockerfile
git commit -m "fix(docker): multi-stage build on go 1.24; ship binary + config + assets only"
```

---

### Task 2: Production compose stack + Caddyfile

**Files:**
- Create: `deploy/docker-compose.prod.yml`
- Create: `deploy/Caddyfile`
- Create: `deploy/env.production`

**Interfaces:**
- Consumes: image layout from Task 1.
- Produces: compose file interpolating `${IMAGE_URI}`, `${IMAGE_TAG}`, `${DOMAIN}` from `/opt/planner/.env`; service names `caddy`, `web`, `redis` (hostname `redis` is what `REDIS_URL=redis://redis:6379` resolves to). `deploy.sh` (Task 3) runs it with `docker compose --project-directory /opt/planner -f /opt/planner/docker-compose.prod.yml`.

- [ ] **Step 1: Write `deploy/docker-compose.prod.yml`**

```yaml
services:
  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    environment:
      DOMAIN: ${DOMAIN}
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      - web

  web:
    image: ${IMAGE_URI}:${IMAGE_TAG}
    restart: unless-stopped
    env_file: .env
    depends_on:
      - redis
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:10000/"]
      interval: 30s
      timeout: 5s
      retries: 3

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    command: ["redis-server", "--appendonly", "yes", "--appendfsync", "everysec", "--maxmemory", "768mb", "--maxmemory-policy", "noeviction"]
    volumes:
      - /var/lib/planner/redis:/data

volumes:
  caddy_data:
  caddy_config:
```

Note: no `ports:` on `web` or `redis` — only Caddy touches the internet.

- [ ] **Step 2: Write `deploy/Caddyfile`**

```
{$DOMAIN} {
	reverse_proxy web:10000
}
```

- [ ] **Step 3: Write `deploy/env.production`** (non-secret env; secrets are appended by deploy.sh)

```bash
ENVIRONMENT=production
PORT=10000
REDIS_URL=redis://redis:6379
DOMAIN=<api domain>            # same value as bootstrap header
FRONTEND_URL=https://<frontend domain>
BACKEND_URL=https://<api domain>
ADMIN_USERS=<comma-separated admin emails>
MAILER_EMAIL_ADDRESS=<sender email>
BLOB_BUCKET_ID=<existing S3 bucket name>
AWS_REGION=<S3 bucket region, e.g. us-east-1>
```

(The `<...>` values are operator configuration copied from Heroku `heroku config -s` output in Task 5 — fill them in the same session that runs bootstrap.)

- [ ] **Step 4: Validate compose file**

```bash
cd deploy
printf 'IMAGE_URI=x\nIMAGE_TAG=y\nDOMAIN=localhost\n' > .env
docker compose -f docker-compose.prod.yml config >/dev/null && echo OK
rm .env
cd ..
```

Expected: `OK`.

- [ ] **Step 5: Local end-to-end smoke (Caddy internal cert on localhost)**

```bash
cd deploy
cat > .env <<EOF
IMAGE_URI=planner-backend
IMAGE_TAG=dev
DOMAIN=localhost
ENVIRONMENT=production
PORT=10000
REDIS_URL=redis://redis:6379
EOF
docker compose -f docker-compose.prod.yml up -d
sleep 5
curl -skfL -o /dev/null -w '%{http_code}\n' https://localhost/
docker compose -f docker-compose.prod.yml down
rm .env
cd ..
```

Expected: `200` (Caddy serves a self-signed localhost cert; `-k` accepts it).

- [ ] **Step 6: Commit**

```bash
git add deploy/docker-compose.prod.yml deploy/Caddyfile deploy/env.production
git commit -m "feat(deploy): production compose stack (caddy + web + persistent redis)"
```

---

### Task 3: VM scripts — `deploy.sh` and `startup-script.sh`

> **Amendment (2026-08-11, review ruling):** the committed `deploy/deploy.sh` deviates from the snippet below in four reviewer-driven, human-approved ways: atomic `.env` render (`mktemp` beside the target + `chmod 600` + `mv` instead of `install` from `/tmp`); automatic rollback to the previous `IMAGE_TAG` when the post-deploy verify fails; `curl -sfS` everywhere with the failing secret's name printed to stderr; `docker image prune ... || true`. The committed file is authoritative.

> **Final-review amendments (2026-08-11):** deploy.sh's verify now gates on an app-level check (`compose exec web wget` against :10000) with a 60s retry loop and a re-verified rollback; `deploy/env.production` and the env contract gained `AWS_REGION`; the bootstrap grants the deployer SA `roles/iam.serviceAccountUser` on the runtime SA (required for CI `gcloud compute ssh`); all three compose services carry json-file log rotation (10m×3).

**Files:**
- Create: `deploy/deploy.sh`
- Create: `deploy/startup-script.sh`

**Interfaces:**
- Consumes: compose file + `env.production` from Task 2; secret names from the registry.
- Produces: `deploy.sh <IMAGE_TAG>` — the single entrypoint CI (Task 8) and manual deploys (Task 6) call on the VM, run as root from `/opt/planner`. `startup-script.sh` is attached to the VM at create time by bootstrap (Task 4).

- [ ] **Step 1: Write `deploy/deploy.sh`**

```bash
#!/usr/bin/env bash
# Runs ON the VM as root. Usage: deploy.sh <IMAGE_TAG>
set -euo pipefail

IMAGE_TAG="${1:?usage: deploy.sh <image-tag>}"
PLANNER_DIR="/opt/planner"
METADATA="http://metadata.google.internal/computeMetadata/v1"
PROJECT_ID="$(curl -sf -H 'Metadata-Flavor: Google' "$METADATA/project/project-id")"
REGION="us-central1"
IMAGE_URI="${REGION}-docker.pkg.dev/${PROJECT_ID}/planner/backend"
SECRET_NAMES=(MAPS_CLIENT_API_KEY GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET \
  JWT_SIGNING_SECRET SENDGRID_API_KEY OPENAI_API_KEY GEONAMES_API_KEY \
  AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY)

token() {
  curl -sf -H 'Metadata-Flavor: Google' \
    "$METADATA/instance/service-accounts/default/token" | jq -r .access_token
}

# 1. registry login with the VM service account
token | docker login -u oauth2accesstoken --password-stdin "https://${REGION}-docker.pkg.dev"

# 2. render .env = committed non-secret env + secrets from Secret Manager
ENV_FILE="${PLANNER_DIR}/.env"
TMP_ENV="$(mktemp)"
trap 'rm -f "$TMP_ENV"' EXIT
cat "${PLANNER_DIR}/env.production" > "$TMP_ENV"
{
  echo "IMAGE_URI=${IMAGE_URI}"
  echo "IMAGE_TAG=${IMAGE_TAG}"
} >> "$TMP_ENV"
ACCESS_TOKEN="$(token)"
for name in "${SECRET_NAMES[@]}"; do
  value="$(curl -sf -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    "https://secretmanager.googleapis.com/v1/projects/${PROJECT_ID}/secrets/${name}/versions/latest:access" \
    | jq -r .payload.data | base64 -d)"
  echo "${name}=${value}" >> "$TMP_ENV"
done
install -m 600 "$TMP_ENV" "$ENV_FILE"

# 3. pull and swap the web container (redis/caddy untouched unless their config changed)
cd "$PLANNER_DIR"
docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml pull web
docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml up -d --remove-orphans

# 4. verify
sleep 5
docker compose --project-directory "$PLANNER_DIR" -f docker-compose.prod.yml ps
curl -sf -o /dev/null http://localhost:80 || { echo 'DEPLOY VERIFY FAILED'; exit 1; }
docker image prune -af --filter "until=168h"
echo "deployed ${IMAGE_URI}:${IMAGE_TAG}"
```

- [ ] **Step 2: Write `deploy/startup-script.sh`** (runs on every boot; idempotent)

```bash
#!/usr/bin/env bash
# GCE startup script: install docker + jq + ops agent once; ensure dirs exist.
set -euo pipefail

mkdir -p /opt/planner /var/lib/planner/redis

if ! command -v docker >/dev/null 2>&1; then
  apt-get update
  apt-get install -y ca-certificates curl gnupg jq unattended-upgrades
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
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
```

- [ ] **Step 3: Lint both scripts**

```bash
bash -n deploy/deploy.sh deploy/startup-script.sh
shellcheck deploy/deploy.sh deploy/startup-script.sh
```

Expected: no output (clean). If shellcheck is missing: `brew install shellcheck`.

- [ ] **Step 4: Commit**

```bash
git add deploy/deploy.sh deploy/startup-script.sh
git commit -m "feat(deploy): VM deploy script (AR login, secret render, compose swap) and boot provisioner"
```

---

### Task 4: `deploy/gcp-bootstrap.sh` — all GCP infra as one idempotent script

**Files:**
- Create: `deploy/gcp-bootstrap.sh`

**Interfaces:**
- Consumes: `deploy/startup-script.sh` (attached to VM at create).
- Produces: every cloud resource in the naming registry; prints `STATIC_IP` and the full WIF provider resource name — Task 8 copies both into GitHub variables.

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# One-time GCP infra bootstrap. Idempotent: every create is guarded by a describe.
# Edit the header, then run: bash deploy/gcp-bootstrap.sh
set -euo pipefail

PROJECT_ID="<your-gcp-project>"   # EDIT ME
DOMAIN="<api domain>"             # EDIT ME (informational; used in final output)
REGION="us-central1"
ZONE="us-central1-a"
VM_NAME="planner-vm"
AR_REPO="planner"
RUNTIME_SA_NAME="planner-vm-sa"
DEPLOYER_SA_NAME="github-deployer"
WIF_POOL="github"
WIF_PROVIDER="github-provider"
STATIC_IP_NAME="planner-ip"
GITHUB_REPO="offerbee-ai/Vacation-Planner"
SECRET_NAMES=(MAPS_CLIENT_API_KEY GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET \
  JWT_SIGNING_SECRET SENDGRID_API_KEY OPENAI_API_KEY GEONAMES_API_KEY \
  AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY)

gcloud config set project "$PROJECT_ID"
RUNTIME_SA="${RUNTIME_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
DEPLOYER_SA="${DEPLOYER_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"

echo '--- 1. APIs'
gcloud services enable compute.googleapis.com artifactregistry.googleapis.com \
  secretmanager.googleapis.com iam.googleapis.com iamcredentials.googleapis.com \
  iap.googleapis.com sts.googleapis.com

echo '--- 2. Artifact Registry'
gcloud artifacts repositories describe "$AR_REPO" --location="$REGION" >/dev/null 2>&1 || \
  gcloud artifacts repositories create "$AR_REPO" --location="$REGION" --repository-format=docker

echo '--- 3. Secrets (empty shells; add values separately)'
for name in "${SECRET_NAMES[@]}"; do
  gcloud secrets describe "$name" >/dev/null 2>&1 || \
    gcloud secrets create "$name" --replication-policy=automatic
done

echo '--- 4. Runtime service account'
gcloud iam service-accounts describe "$RUNTIME_SA" >/dev/null 2>&1 || \
  gcloud iam service-accounts create "$RUNTIME_SA_NAME" --display-name='planner VM runtime'
for role in roles/secretmanager.secretAccessor roles/artifactregistry.reader \
            roles/logging.logWriter roles/monitoring.metricWriter; do
  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${RUNTIME_SA}" --role="$role" --condition=None >/dev/null
done

echo '--- 5. Deployer SA + Workload Identity Federation'
gcloud iam service-accounts describe "$DEPLOYER_SA" >/dev/null 2>&1 || \
  gcloud iam service-accounts create "$DEPLOYER_SA_NAME" --display-name='GitHub Actions deployer'
gcloud iam workload-identity-pools describe "$WIF_POOL" --location=global >/dev/null 2>&1 || \
  gcloud iam workload-identity-pools create "$WIF_POOL" --location=global --display-name='GitHub'
gcloud iam workload-identity-pools providers describe "$WIF_PROVIDER" \
  --location=global --workload-identity-pool="$WIF_POOL" >/dev/null 2>&1 || \
  gcloud iam workload-identity-pools providers create-oidc "$WIF_PROVIDER" \
    --location=global --workload-identity-pool="$WIF_POOL" \
    --issuer-uri='https://token.actions.githubusercontent.com' \
    --attribute-mapping='google.subject=assertion.sub,attribute.repository=assertion.repository' \
    --attribute-condition="assertion.repository=='${GITHUB_REPO}'"
gcloud iam service-accounts add-iam-policy-binding "$DEPLOYER_SA" \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${WIF_POOL}/attribute.repository/${GITHUB_REPO}" >/dev/null
for role in roles/artifactregistry.writer roles/compute.osAdminLogin \
            roles/iap.tunnelResourceAccessor roles/compute.viewer; do
  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${DEPLOYER_SA}" --role="$role" --condition=None >/dev/null
done

echo '--- 6. Network: static IP + firewall'
gcloud compute addresses describe "$STATIC_IP_NAME" --region="$REGION" >/dev/null 2>&1 || \
  gcloud compute addresses create "$STATIC_IP_NAME" --region="$REGION"
STATIC_IP="$(gcloud compute addresses describe "$STATIC_IP_NAME" --region="$REGION" --format='value(address)')"
gcloud compute firewall-rules describe allow-planner-web >/dev/null 2>&1 || \
  gcloud compute firewall-rules create allow-planner-web \
    --allow=tcp:80,tcp:443 --target-tags=planner-web --source-ranges=0.0.0.0/0
gcloud compute firewall-rules describe allow-iap-ssh >/dev/null 2>&1 || \
  gcloud compute firewall-rules create allow-iap-ssh \
    --allow=tcp:22 --source-ranges=35.235.240.0/20

echo '--- 7. VM'
gcloud compute instances describe "$VM_NAME" --zone="$ZONE" >/dev/null 2>&1 || \
  gcloud compute instances create "$VM_NAME" \
    --zone="$ZONE" --machine-type=e2-small \
    --image-family=debian-12 --image-project=debian-cloud \
    --boot-disk-size=30GB --boot-disk-type=pd-balanced \
    --tags=planner-web \
    --address="$STATIC_IP" \
    --service-account="$RUNTIME_SA" --scopes=cloud-platform \
    --metadata=enable-oslogin=TRUE \
    --metadata-from-file=startup-script=deploy/startup-script.sh

echo '--- 8. Daily disk snapshots (7-day retention)'
gcloud compute resource-policies describe planner-daily-snap --region="$REGION" >/dev/null 2>&1 || \
  gcloud compute resource-policies create snapshot-schedule planner-daily-snap \
    --region="$REGION" --max-retention-days=7 --daily-schedule --start-time=09:00
gcloud compute disks describe "$VM_NAME" --zone="$ZONE" \
  --format='value(resourcePolicies)' | grep -q planner-daily-snap || \
  gcloud compute disks add-resource-policies "$VM_NAME" --zone="$ZONE" \
    --resource-policies=planner-daily-snap

WIF_PROVIDER_NAME="$(gcloud iam workload-identity-pools providers describe "$WIF_PROVIDER" \
  --location=global --workload-identity-pool="$WIF_POOL" --format='value(name)')"
cat <<SUMMARY

============================================================
Bootstrap complete.
  Static IP:      ${STATIC_IP}   <- point DNS A record for ${DOMAIN} here
  WIF provider:   ${WIF_PROVIDER_NAME}
  Deployer SA:    ${DEPLOYER_SA}
  Image path:     ${REGION}-docker.pkg.dev/${PROJECT_ID}/planner/backend
Next: add secret values (Task 5, step 3), then first deploy (Task 6).
============================================================
SUMMARY
```

- [ ] **Step 2: Lint**

```bash
bash -n deploy/gcp-bootstrap.sh && shellcheck deploy/gcp-bootstrap.sh
```

Expected: clean.

- [ ] **Step 3: Commit, push, PR**

```bash
git add deploy/gcp-bootstrap.sh docs/superpowers/plans/2026-08-11-gcp-vm-deployment.md
git commit -m "feat(deploy): idempotent GCP bootstrap script (VM, WIF, secrets, network, snapshots)"
git push -u origin feat/gcp-vm-deploy
gh pr create --base main --title "feat(deploy): GCP VM deployment scaffolding" \
  --body "Dockerfile multi-stage fix + prod compose stack + VM/bootstrap scripts per docs/superpowers/plans/2026-08-11-gcp-vm-deployment.md. No CI deploy workflow yet (lands after infra exists)."
```

Expected: PR opens, Go CI runs and passes (no Go changes). Merge before Task 5.

---

### Task 5: Run bootstrap + load secrets  *(operator task — needs `gcloud auth login` as tim@uare.ai and Heroku access)*

**Files:** none (cloud state only)

**Interfaces:**
- Consumes: `deploy/gcp-bootstrap.sh` from merged main.
- Produces: live infra + populated secrets + filled `deploy/env.production` values (committed).

- [ ] **Step 1: Edit script header** — set `PROJECT_ID` and `DOMAIN` in `deploy/gcp-bootstrap.sh`; also fill the `<...>` values in `deploy/env.production` from `heroku config -s` output (including `AWS_REGION` — the S3 SDK has no region source on GCE; without it every blob call fails at request time). Commit directly to a small PR or with the next task's PR.

- [ ] **Step 2: Run it**

```bash
gcloud auth login
bash deploy/gcp-bootstrap.sh
```

Expected: ends with the `Bootstrap complete` summary block. Rerun is safe.

- [ ] **Step 3: Load secret values from Heroku**

```bash
heroku config -s -a <heroku-app-name> > /tmp/heroku.env   # then, per secret:
for name in MAPS_CLIENT_API_KEY GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET \
            JWT_SIGNING_SECRET SENDGRID_API_KEY OPENAI_API_KEY GEONAMES_API_KEY \
            AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY; do
  val="$(grep "^${name}=" /tmp/heroku.env | cut -d= -f2- | tr -d \"'\")"
  [ -n "$val" ] && printf '%s' "$val" | gcloud secrets versions add "$name" --data-file=-
done
rm /tmp/heroku.env
```

Expected: one `Created version [1]` per secret. Any secret Heroku doesn't have (e.g. new JWT secret): generate — `openssl rand -base64 48 | tr -d '\n' | gcloud secrets versions add JWT_SIGNING_SECRET --data-file=-`.

- [ ] **Step 4: Verify VM provisioned**

```bash
gcloud compute ssh planner-vm --zone=us-central1-a --tunnel-through-iap \
  --command='docker --version && jq --version && ls -ld /opt/planner /var/lib/planner/redis'
```

Expected: docker + jq versions print; both directories exist. (Startup script needs ~2 min after VM create; retry once if docker missing.)

---

### Task 6: First manual deploy

**Files:** none (uses merged main)

**Interfaces:**
- Consumes: everything above.
- Produces: app serving on `https://<static-ip>` (cert pending until DNS in Task 7); proves the exact command sequence CI will run.

- [ ] **Step 1: Build and push the image**

```bash
git checkout main && git pull
SHA="$(git rev-parse HEAD)"
gcloud auth configure-docker us-central1-docker.pkg.dev --quiet
docker build -t "us-central1-docker.pkg.dev/${PROJECT_ID}/planner/backend:${SHA}" .
docker push "us-central1-docker.pkg.dev/${PROJECT_ID}/planner/backend:${SHA}"
```

- [ ] **Step 2: Ship deploy files and run deploy.sh (same 3 commands CI uses)**

```bash
gcloud compute ssh planner-vm --zone=us-central1-a --tunnel-through-iap \
  --command='mkdir -p /tmp/planner-deploy'
gcloud compute scp deploy/docker-compose.prod.yml deploy/Caddyfile deploy/env.production \
  deploy/deploy.sh planner-vm:/tmp/planner-deploy/ --zone=us-central1-a --tunnel-through-iap
gcloud compute ssh planner-vm --zone=us-central1-a --tunnel-through-iap \
  --command="sudo bash -c 'cp /tmp/planner-deploy/* /opt/planner/ && bash /opt/planner/deploy.sh ${SHA}'"
```

Expected: ends with `deployed ...:<sha>`; `docker compose ps` shows caddy/web/redis all `Up`.

- [ ] **Step 3: Verify from outside**

```bash
STATIC_IP="$(gcloud compute addresses describe planner-ip --region=us-central1 --format='value(address)')"
curl -s -o /dev/null -w '%{http_code}\n' "http://${STATIC_IP}/"
```

Expected: `308` or `200` (Caddy redirects HTTP→HTTPS for the configured domain; any HTTP response proves the stack is up. Full HTTPS check comes after DNS).

---

### Task 7: DNS + OAuth cutover prerequisites  *(operator task)*

**Files:** none

- [ ] **Step 1: DNS** — create an `A` record: `<api domain>` → static IP from Task 6. Verify: `dig +short <api domain>` returns the IP.

- [ ] **Step 2: Wait for cert** — Caddy auto-issues once DNS resolves. Verify: `curl -sfL -o /dev/null -w '%{http_code}\n' https://<api domain>/` → `200`.

- [ ] **Step 3: Google OAuth** — in Google Cloud Console → APIs & Services → Credentials → the existing OAuth client: add authorized redirect URI `https://<api domain>/v1/callback-google` (route: `planner/planner.go:1843`). Keep the Heroku URI until Task 9 cutover completes.

- [ ] **Step 4: Verify login flow** — browser: `https://<api domain>/v1/login-google` completes Google sign-in and lands back on the site.

---

### Task 8: CI deploy workflow

**Files:**
- Modify: `.github/workflows/go.yml` (add `workflow_call` trigger)
- Create: `.github/workflows/deploy.yml`

**Interfaces:**
- Consumes: WIF provider name + deployer SA (bootstrap summary), `deploy.sh` contract (`deploy.sh <tag>`).
- Produces: push-to-main auto-deploy; manual redeploy/rollback via `workflow_dispatch` with a `tag` input.

- [ ] **Step 1: Make go.yml callable** — in `.github/workflows/go.yml`, change the `on:` block to:

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  workflow_call:
```

- [ ] **Step 2: Set GitHub repo variables** (values from bootstrap summary)

```bash
gh variable set GCP_PROJECT --body "<your-gcp-project>"
gh variable set GCP_WIF_PROVIDER --body "projects/<project-number>/locations/global/workloadIdentityPools/github/providers/github-provider"
gh variable set GCP_DEPLOYER_SA --body "github-deployer@<your-gcp-project>.iam.gserviceaccount.com"
gh variable set GCP_ZONE --body "us-central1-a"
gh variable set GCP_VM --body "planner-vm"
gh variable set GCP_IMAGE --body "us-central1-docker.pkg.dev/<your-gcp-project>/planner/backend"
```

- [ ] **Step 3: Write `.github/workflows/deploy.yml`**

```yaml
name: Deploy

on:
  push:
    branches: [main]
  workflow_dispatch:
    inputs:
      tag:
        description: "Existing image tag to (re)deploy; empty = build current SHA"
        required: false

permissions:
  contents: read
  id-token: write

concurrency:
  group: deploy-production
  cancel-in-progress: false

jobs:
  test:
    uses: ./.github/workflows/go.yml

  deploy:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      - uses: google-github-actions/auth@v3
        with:
          workload_identity_provider: ${{ vars.GCP_WIF_PROVIDER }}
          service_account: ${{ vars.GCP_DEPLOYER_SA }}

      - uses: google-github-actions/setup-gcloud@v3

      - name: Resolve tag
        id: tag
        run: echo "tag=${{ inputs.tag != '' && inputs.tag || github.sha }}" >> "$GITHUB_OUTPUT"

      - name: Build and push image
        if: inputs.tag == ''
        run: |
          gcloud auth configure-docker us-central1-docker.pkg.dev --quiet
          docker build -t "${{ vars.GCP_IMAGE }}:${{ steps.tag.outputs.tag }}" .
          docker push "${{ vars.GCP_IMAGE }}:${{ steps.tag.outputs.tag }}"

      - name: Deploy to VM
        run: |
          gcloud compute ssh "${{ vars.GCP_VM }}" --zone "${{ vars.GCP_ZONE }}" --tunnel-through-iap \
            --command='mkdir -p /tmp/planner-deploy'
          gcloud compute scp deploy/docker-compose.prod.yml deploy/Caddyfile deploy/env.production \
            deploy/deploy.sh "${{ vars.GCP_VM }}":/tmp/planner-deploy/ \
            --zone "${{ vars.GCP_ZONE }}" --tunnel-through-iap
          gcloud compute ssh "${{ vars.GCP_VM }}" --zone "${{ vars.GCP_ZONE }}" --tunnel-through-iap \
            --command="sudo bash -c 'cp /tmp/planner-deploy/* /opt/planner/ && bash /opt/planner/deploy.sh ${{ steps.tag.outputs.tag }}'"
```

- [ ] **Step 4: Lint workflows**

```bash
actionlint .github/workflows/deploy.yml .github/workflows/go.yml
```

Expected: clean (`brew install actionlint` if missing).

- [ ] **Step 5: Commit, PR, merge, watch first CI deploy**

```bash
git checkout -b ci/deploy-workflow origin/main
git add .github/workflows/go.yml .github/workflows/deploy.yml
git commit -m "ci: push-to-main deploy to GCE via WIF + IAP; make go.yml callable"
git push -u origin ci/deploy-workflow
gh pr create --base main --title "ci: automated deploy to GCP VM" --body "Adds deploy.yml (WIF auth, image build/push, IAP deploy). go.yml gains workflow_call so deploys gate on lint+test."
```

After merge: `gh run watch` the Deploy run on main. Expected: test job green, deploy job green, `https://<api domain>/` still 200 and serving the new SHA.

- [ ] **Step 6: Rollback drill (proves the escape hatch)**

```bash
gh workflow run deploy.yml -f tag=<previous-sha>
```

Expected: deploy job green — redeploys the older image without building.

---

### Task 9: Migrate Redis data from Heroku and cut over  *(operator task; maintenance window)*

**Files:** none (runbook)

Order matters throughout this task — read each step fully before running it.

- [ ] **Step 1: Announce/enable maintenance** — `heroku maintenance:on -a <heroku-app-name>`. Heroku app stops taking writes.

- [ ] **Step 2: Capture the dataset.** Option A (try first):

```bash
redis-cli --tls -u "$(heroku config:get REDIS_TLS_URL -a <heroku-app-name>)" \
  --insecure --rdb /tmp/heroku-dump.rdb
redis-cli --tls -u "$(heroku config:get REDIS_TLS_URL -a <heroku-app-name>)" --insecure DBSIZE
```

Record the DBSIZE number. If Heroku blocks `SYNC` (Option A errors), use Option B — live key copy with RIOT:

```bash
brew install riot
# expose VM redis to your laptop only, via IAP tunnel + temporary localhost publish:
gcloud compute ssh planner-vm --zone=us-central1-a --tunnel-through-iap -- -N -L 6380:localhost:6379 &
# on the VM, temporarily add `ports: ["127.0.0.1:6379:6379"]` to the redis service, `docker compose up -d redis`
riot replicate --mode snapshot "$(heroku config:get REDIS_TLS_URL -a <heroku-app-name>)" redis://localhost:6380
# remove the temporary ports mapping afterwards and `docker compose up -d redis`
```

If Option B was used, skip to Step 5 (data already in the VM Redis); the DBSIZE check in Step 5 still applies.

- [ ] **Step 3: Load the RDB into the VM Redis (Option A path).** On the VM (`gcloud compute ssh planner-vm --zone=us-central1-a --tunnel-through-iap` after `gcloud compute scp /tmp/heroku-dump.rdb planner-vm:/tmp/ ...`), run exactly this sequence:

```bash
cd /opt/planner
docker compose -f docker-compose.prod.yml stop web    # stop writers first
docker compose -f docker-compose.prod.yml stop redis
sudo rm -rf /var/lib/planner/redis/*
sudo cp /tmp/heroku-dump.rdb /var/lib/planner/redis/dump.rdb
# start redis WITHOUT AOF so it loads the RDB file:
sudo docker run -d --name redis-restore -v /var/lib/planner/redis:/data redis:7-alpine \
  redis-server --appendonly no
sudo docker exec redis-restore redis-cli DBSIZE      # must match the number from Step 2
# turn AOF on so the dataset is rewritten into the AOF the compose config expects:
sudo docker exec redis-restore redis-cli CONFIG SET appendonly yes
sudo docker exec redis-restore redis-cli BGREWRITEAOF
sleep 5
sudo docker exec redis-restore redis-cli INFO persistence | grep aof_rewrite_in_progress
# wait until aof_rewrite_in_progress:0, then:
sudo docker rm -f redis-restore
docker compose -f docker-compose.prod.yml up -d      # normal stack, AOF on
docker compose -f docker-compose.prod.yml exec redis redis-cli DBSIZE   # must match again
```

- [ ] **Step 4: Smoke test with real data** — log in as an existing user at `https://<api domain>`, open a saved plan, run one search.

- [ ] **Step 5: Cut over** — point the frontend/DNS at the new backend: update `FRONTEND_URL`-facing config, any frontend `BACKEND_URL` references, and if the public domain previously pointed at Heroku, flip its DNS to the static IP. Remove the Heroku redirect URI from the OAuth client.

- [ ] **Step 6: Verify DBSIZE parity + traffic in logs**

```bash
gcloud compute ssh planner-vm --zone=us-central1-a --tunnel-through-iap \
  --command='cd /opt/planner && docker compose -f docker-compose.prod.yml logs --since 10m web | tail -20'
```

Expected: real request logs, no error spam.

---

### Task 10: Decommission Heroku + record the decision

**Files:**
- Delete: `Procfile`, `heroku.yml`
- Modify: `docs/gcp-deployment-strategy.md` (record chosen path)

- [ ] **Step 1: Confirm new stack healthy for at least a few days** (operator judgement; snapshots exist either way).

- [ ] **Step 2: Scale down / delete the Heroku app** — `heroku ps:scale web=0 -a <heroku-app-name>`; delete the app + Heroku Redis add-on once comfortable.

- [ ] **Step 3: Remove Heroku artifacts from repo**

```bash
git checkout -b chore/remove-heroku origin/main
git rm Procfile heroku.yml
```

- [ ] **Step 4: Update strategy doc** — in `docs/gcp-deployment-strategy.md`, replace the "Recommended target: Cloud Run" section heading with "Chosen target: dedicated GCE VM (see docs/superpowers/plans/2026-08-11-gcp-vm-deployment.md)" and add one paragraph: e2-small VM chosen for cost (~$22/mo vs ~$100+) and colocated durable Redis; Cloud Run section retained as the scale-up path.

- [ ] **Step 5: Commit + PR**

```bash
git add -A
git commit -m "chore: decommission Heroku; record GCE VM as chosen deploy target"
git push -u origin chore/remove-heroku
gh pr create --base main --title "chore: remove Heroku deploy artifacts" --body "Backend now serves from GCE VM (planner-vm) with CI deploys. Heroku app scaled to zero."
```

Expected: CI green (deploy workflow runs on merge and redeploys — harmless no-op change).

---

## Deferred (intentionally out of scope)

- Cloud Monitoring uptime check + alerting policy (needs notification channel setup; add after cutover).
- GCS migration for blobs (S3 client has no endpoint override; keep S3 until a code change is scheduled).
- `/health` endpoint (using `GET /` for probes today).
- Zero-downtime blue/green on the VM (current swap gap is a few seconds; revisit if it matters).
