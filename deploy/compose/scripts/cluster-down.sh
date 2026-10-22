#!/usr/bin/env bash
# cluster-down.sh — remove the Kubernetes resources the compose stack
# created.
#
# Called by `make compose-down` after the compose stack is stopped. It:
#   1. removes the accelerator node labels,
#   2. deletes the declarative manifests (namespace, storageclass, secret,
#      PVC, redis NodePort),
#   3. destroys the JuiceFS filesystem,
#   4. deletes the MinIO bucket.
#
# The values are read from the .env file (via the environment).
#
# Usage: bash deploy/compose/scripts/cluster-down.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
K8S_DIR="${SCRIPT_DIR}/../k8s"
REPO_ROOT="${SCRIPT_DIR}/../../.."

# Load .env if present.
if [[ -f "${REPO_ROOT}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/.env"
  set +a
fi

: "${JUICE_FS_NAME:?JUICE_FS_NAME is required (see .env)}"
: "${JUICE_FS_META_URL:?JUICE_FS_META_URL is required (see .env)}"
: "${JUICE_FS_ACCESS_KEY:?JUICE_FS_ACCESS_KEY is required (see .env)}"
: "${JUICE_FS_SECRET_KEY:?JUICE_FS_SECRET_KEY is required (see .env)}"

MINIO_BUCKET="${JUICE_FS_BUCKET##*/}"
MINIO_ACCESS_KEY="${JUICE_FS_ACCESS_KEY}"
MINIO_SECRET_KEY="${JUICE_FS_SECRET_KEY}"
IN_CLUSTER_META_URL="${JUICE_FS_META_URL//10.86.56.47:30379/yunzhi-redis-headless.yunzhi.svc.cluster.local:6379}"

echo ">> [cluster-down] removing accelerator node labels"
# Remove the current prefix and the two legacy prefixes (taas.go-taas.github.io
# and go-taas.io) so a cluster labelled before the go-taas.com rename is cleaned
# up as well.
kubectl label node --all \
  go-taas.com/accelerator- \
  go-taas.com/accelerator-type- \
  taas.go-taas.github.io/accelerator- \
  taas.go-taas.github.io/accelerator-type- \
  go-taas.io/accelerator- >/dev/null 2>&1 || true

echo ">> [cluster-down] deleting inference resources in the taas namespace"
# The controller may have created deployments/services/HPAs in the taas
# namespace; they reference the model-weights PVC, so they must be removed
# before the PVC and the namespace can be deleted.
kubectl delete deployment,service,horizontalpodautoscaler,pod,replicaset \
  -n taas --all --ignore-not-found --wait=false >/dev/null 2>&1 || true
kubectl delete pvc -n taas model-weights --ignore-not-found --wait=false >/dev/null 2>&1 || true

echo ">> [cluster-down] deleting declarative manifests"
kubectl delete -f "${K8S_DIR}/pvc.yaml" --ignore-not-found >/dev/null
kubectl delete -f "${K8S_DIR}/storageclass.yaml" --ignore-not-found >/dev/null
kubectl delete -f "${K8S_DIR}/redis-nodeport.yaml" --ignore-not-found >/dev/null
kubectl delete -f "${K8S_DIR}/namespace.yaml" --ignore-not-found >/dev/null
# The secret lives in the yunzhi namespace (not the taas namespace), so
# delete it explicitly.
kubectl delete secret juicefs-taas-models-secret -n yunzhi --ignore-not-found >/dev/null

echo ">> [cluster-down] destroying JuiceFS filesystem '${JUICE_FS_NAME}'"
# The destroy Job reads the filesystem UUID from the metadata engine's
# Redis "setting" key and destroys it. It runs inside the cluster so it
# can reach the headless Redis and MinIO directly. The connection details
# are passed as env vars; the python `$` are escaped so the shell does not
# expand them, and CRLF is built with chr() to avoid YAML mangling.
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: taas-juicefs-destroy
  namespace: yunzhi
  labels:
    app.kubernetes.io/part-of: go-taas
    go-taas.com/role: model-weights
spec:
  backoffLimit: 2
  ttlSecondsAfterFinished: 120
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: juicefs
          image: juicedata/mount:ce-v1.4.1
          command:
            - /bin/sh
            - -c
            - |
              set -e
              UUID=\$(python3 -c 'import socket,re,json,os; CR=chr(13)+chr(10); u=os.environ["IN_CLUSTER_META_URL"]; m=re.match(r"redis://([^@/]+)(?:/(\d+))?\$",u); h,p=m.group(1).split(":"); db=m.group(2) or "0"; s=socket.create_connection((h,int(p)),timeout=10); cmd=lambda *a: (s.sendall((("*%d"+CR)%len(a)+"".join(("$%d"+CR+"%s"+CR)%(len(x.encode()),x) for x in a)).encode()), s.recv(65536))[1]; cmd("SELECT",db); r=cmd("GET","setting"); print(json.loads(r.split(CR.encode(),1)[1].rsplit(CR.encode(),1)[0])["UUID"])')
              juicefs destroy --force "\$IN_CLUSTER_META_URL" "\$UUID"
              echo "filesystem \$JUICE_FS_NAME destroyed"
          env:
            - name: IN_CLUSTER_META_URL
              value: ${IN_CLUSTER_META_URL}
            - name: JUICE_FS_NAME
              value: ${JUICE_FS_NAME}
EOF
kubectl -n yunzhi wait --for=condition=complete job/taas-juicefs-destroy --timeout=120s >/dev/null
kubectl -n yunzhi delete job taas-juicefs-destroy --ignore-not-found >/dev/null

echo ">> [cluster-down] deleting MinIO bucket '${MINIO_BUCKET}'"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: taas-minio-bucket-delete
  namespace: yunzhi
  labels:
    app.kubernetes.io/part-of: go-taas
    go-taas.com/role: model-weights
spec:
  backoffLimit: 2
  ttlSecondsAfterFinished: 120
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: mc
          image: hub.sudoinfotech.com/library/mc:RELEASE.2025-08-13T08-35-41Z
          command:
            - /bin/sh
            - -c
            - |
              set -e
              mc alias set local http://minio-api.yunzhi:9000 ${MINIO_ACCESS_KEY} ${MINIO_SECRET_KEY}
              mc rb --force local/${MINIO_BUCKET}
              echo "bucket ${MINIO_BUCKET} deleted"
          env:
            - name: MINIO_ACCESS_KEY
              value: ${MINIO_ACCESS_KEY}
            - name: MINIO_SECRET_KEY
              value: ${MINIO_SECRET_KEY}
            - name: MINIO_BUCKET
              value: ${MINIO_BUCKET}
EOF
kubectl -n yunzhi wait --for=condition=complete job/taas-minio-bucket-delete --timeout=120s >/dev/null
kubectl -n yunzhi delete job taas-minio-bucket-delete --ignore-not-found >/dev/null

echo ">> [cluster-down] done"