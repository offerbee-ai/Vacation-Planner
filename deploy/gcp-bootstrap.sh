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
gcloud iam service-accounts add-iam-policy-binding "$RUNTIME_SA" \
  --member="serviceAccount:${DEPLOYER_SA}" --role=roles/iam.serviceAccountUser >/dev/null

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
