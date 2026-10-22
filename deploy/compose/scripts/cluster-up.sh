#!/usr/bin/env bash
# cluster-up.sh — create the Kubernetes resources the compose stack needs.
#
# Called by `make compose-up` before the compose stack starts. It:
#   1. creates the MinIO bucket that backs the JuiceFS filesystem,
#   2. formats the JuiceFS filesystem,
#   3. applies the declarative manifests (namespace, storageclass, secret,
#      PVC, redis NodePort),
#   4. adds the accelerator node labels the controller's node selector
#      and the compatibility matrix rely on.
#
# The values are read from the .env file (via the environment). The
# manifests under deploy/compose/k8s/ are templated with envsubst so the
# secret and the Jobs carry the same credentials the compose stack uses.
#
# Usage: bash deploy/compose/scripts/cluster-up.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
K8S_DIR="${SCRIPT_DIR}/../k8s"
COMPOSE_DIR="${SCRIPT_DIR}/.."
REPO_ROOT="${SCRIPT_DIR}/../../.."

# Load .env if present (the Makefile already exports it, but this script
# is also runnable standalone).
if [[ -f "${REPO_ROOT}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/.env"
  set +a
fi

# ---- Required variables ------------------------------------------------
: "${JUICE_FS_NAME:?JUICE_FS_NAME is required (see .env)}"
: "${JUICE_FS_META_URL:?JUICE_FS_META_URL is required (see .env)}"
: "${JUICE_FS_STORAGE:?JUICE_FS_STORAGE is required (see .env)}"
: "${JUICE_FS_BUCKET:?JUICE_FS_BUCKET is required (see .env)}"
: "${JUICE_FS_ACCESS_KEY:?JUICE_FS_ACCESS_KEY is required (see .env)}"
: "${JUICE_FS_SECRET_KEY:?JUICE_FS_SECRET_KEY is required (see .env)}"

# The MinIO bucket is the last path segment of the JuiceFS bucket URL
# (e.g. http://minio-api.yunzhi:9000/taas -> "taas").
MINIO_BUCKET="${JUICE_FS_BUCKET##*/}"
MINIO_ACCESS_KEY="${JUICE_FS_ACCESS_KEY}"
MINIO_SECRET_KEY="${JUICE_FS_SECRET_KEY}"

# The in-cluster metadata URL (the compose stack reaches the same Redis
# through the NodePort; the Jobs run inside the cluster and use the
# headless service directly).
IN_CLUSTER_META_URL="${JUICE_FS_META_URL//10.86.56.47:30379/yunzhi-redis-headless.yunzhi.svc.cluster.local:6379}"
IN_CLUSTER_BUCKET="${JUICE_FS_BUCKET//10.86.56.47:9000/minio-api.yunzhi:9000}"

echo ">> [cluster-up] creating MinIO bucket '${MINIO_BUCKET}'"
env \
  MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY}" \
  MINIO_SECRET_KEY="${MINIO_SECRET_KEY}" \
  MINIO_BUCKET="${MINIO_BUCKET}" \
  envsubst < "${K8S_DIR}/minio-bucket-job.yaml" | kubectl apply -f - >/dev/null
kubectl -n yunzhi wait --for=condition=complete job/taas-minio-bucket --timeout=120s >/dev/null

echo ">> [cluster-up] formatting JuiceFS filesystem '${JUICE_FS_NAME}'"
env \
  JUICE_FS_NAME="${JUICE_FS_NAME}" \
  JUICE_FS_META_URL="${IN_CLUSTER_META_URL}" \
  JUICE_FS_STORAGE="${JUICE_FS_STORAGE}" \
  JUICE_FS_BUCKET="${IN_CLUSTER_BUCKET}" \
  JUICE_FS_ACCESS_KEY="${JUICE_FS_ACCESS_KEY}" \
  JUICE_FS_SECRET_KEY="${JUICE_FS_SECRET_KEY}" \
  envsubst < "${K8S_DIR}/juicefs-format-job.yaml" | kubectl apply -f - >/dev/null
kubectl -n yunzhi wait --for=condition=complete job/taas-juicefs-format --timeout=180s >/dev/null

echo ">> [cluster-up] applying declarative manifests"
# The secret is templated with the same values.
env \
  JUICE_FS_NAME="${JUICE_FS_NAME}" \
  JUICE_FS_META_URL="${IN_CLUSTER_META_URL}" \
  JUICE_FS_STORAGE="${JUICE_FS_STORAGE}" \
  JUICE_FS_BUCKET="${IN_CLUSTER_BUCKET}" \
  JUICE_FS_ACCESS_KEY="${JUICE_FS_ACCESS_KEY}" \
  JUICE_FS_SECRET_KEY="${JUICE_FS_SECRET_KEY}" \
  envsubst < "${K8S_DIR}/secret.yaml" | kubectl apply -f - >/dev/null
kubectl apply -f "${K8S_DIR}/namespace.yaml" >/dev/null
kubectl apply -f "${K8S_DIR}/storageclass.yaml" >/dev/null
kubectl apply -f "${K8S_DIR}/pvc.yaml" >/dev/null
kubectl apply -f "${K8S_DIR}/redis-nodeport.yaml" >/dev/null

echo ">> [cluster-up] adding accelerator node labels"
kubectl label node --all \
  go-taas.com/accelerator=nvidia \
  go-taas.com/accelerator-type=gpu \
  --overwrite >/dev/null

echo ">> [cluster-up] done"