#!/usr/bin/env bash
# End-to-end test for sumomo: bring up Mastodon (via magout on a kind
# cluster) with sumomo as its LibreTranslate translation backend, post an
# English status, and verify that Mastodon's translate API returns a
# Japanese translation through sumomo.
#
# Usage: make test-e2e   (or: nix develop .#e2e -c bash scripts/e2e.sh)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${CLUSTER:-sumomo-e2e}"
NS=mastodon
MODEL_REL=plamo-3-translate-0.8b-Q4_K_M.gguf
MODEL_HOST_DIR="${MODEL_HOST_DIR:-$ROOT/models}"
KIND_NODE_IMAGE=kindest/node:v1.36.4
MASTODON_VERSION=v4.3.23
MAGOUT_REPO=https://ushitora-anqou.github.io/magout
USER_NAME=sumomo_e2e
USER_EMAIL=sumomo.e2e@gmail.com
WORK="$(mktemp -d)"
export KUBECONFIG="$WORK/kubeconfig"

cleanup() {
  if [ -n "${PF_PID:-}" ]; then kill "$PF_PID" 2>/dev/null || true; fi
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

log()  { printf '\033[1;34m[e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[e2e] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

# Wait until a Deployment exists (the operator creates them asynchronously
# after install) and its pods are all Ready.
wait_ready() {
  local deploy="$1" timeout="${2:-900}"
  local deadline=$((SECONDS + timeout))
  until kubectl get deployment "$deploy" -n "$NS" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$deadline" ] || return 1
    sleep 5
  done
  kubectl rollout status "deployment/$deploy" -n "$NS" \
    --timeout="$((deadline - SECONDS))s" >/dev/null
}

# --- 0. prerequisites --------------------------------------------------
command -v docker >/dev/null || fail "docker is required"
command -v kind >/dev/null || fail "kind is required (run via: nix develop .#e2e)"
command -v kubectl >/dev/null || fail "kubectl is required"
command -v helm >/dev/null || fail "helm is required"

if [ ! -f "$MODEL_HOST_DIR/$MODEL_REL" ]; then
  log "model not found; downloading to $MODEL_HOST_DIR"
  (cd "$ROOT" && nix run .#download-model)
fi
[ -f "$MODEL_HOST_DIR/$MODEL_REL" ] || fail "model download failed"

# --- 1. kind cluster with the model mounted -----------------------------
log "creating kind cluster '$CLUSTER'"
cat > "$WORK/kind-config.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $CLUSTER
networking:
  apiServerAddress: "127.0.0.1"
  apiServerPort: 0
nodes:
- role: control-plane
  extraMounts:
  - hostPath: $MODEL_HOST_DIR
    containerPath: /mnt/sumomo-models
    readOnly: true
EOF
kind create cluster --config "$WORK/kind-config.yaml" --image "$KIND_NODE_IMAGE" >/dev/null
kubectl wait --for=condition=Ready node --all --timeout=300s >/dev/null

# --- 2. sumomo image ----------------------------------------------------
log "building and loading the sumomo image"
IMAGE=ghcr.io/ushitora-anqou/sumomo:latest
IMG=$(nix build "$ROOT#sumomo-image" --print-out-paths --no-link | tail -1)
[ -n "$IMG" ] || fail "nix build .#sumomo-image failed"
docker load < "$IMG" >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null

# --- 3. sumomo + postgres + redis --------------------------------------
log "deploying sumomo, postgres, redis"
kubectl create namespace "$NS" >/dev/null 2>&1 || true
kubectl apply -f "$ROOT/k8s/sumomo.yaml" >/dev/null
kubectl apply -f "$ROOT/k8s/postgres.yaml" >/dev/null
helm repo add bitnami https://charts.bitnami.com/bitnami >/dev/null 2>&1 || true
helm repo update bitnami >/dev/null
helm install mastodon-redis bitnami/redis -n "$NS" \
  --set auth.enabled=false --set architecture=standalone \
  --set master.persistence.enabled=false \
  --set master.resources.requests.memory=128Mi \
  --set master.resources.limits.memory=256Mi \
  --wait --timeout 600s >/dev/null
kubectl wait --for=condition=ready pod/mastodon-pg-0 -n "$NS" --timeout=600s >/dev/null
kubectl wait --for=condition=ready pod -l app=sumomo --timeout=600s >/dev/null

# --- 4. Mastodon via magout --------------------------------------------
log "deploying Mastodon $MASTODON_VERSION via magout"
kubectl -n "$NS" create secret generic sumomo-mastodon-env \
  --from-literal=LOCAL_DOMAIN=localhost:8080 \
  --from-literal=ALTERNATE_DOMAINS=localhost,localhost:8080 \
  --from-literal=SECRET_KEY_BASE="$(openssl rand -hex 64)" \
  --from-literal=OTP_SECRET="$(openssl rand -hex 32)" \
  --from-literal=ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY="$(openssl rand -base64 32)" \
  --from-literal=ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY="$(openssl rand -base64 32)" \
  --from-literal=ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT="$(openssl rand -base64 32)" \
  --from-literal=DB_HOST=mastodon-pg.$NS.svc.cluster.local \
  --from-literal=DB_PORT=5432 \
  --from-literal=DB_NAME=mastodon \
  --from-literal=DB_USER=mastodon \
  --from-literal=DB_PASS=mastodonpw \
  --from-literal=REDIS_HOST=mastodon-redis-master.$NS.svc.cluster.local \
  --from-literal=REDIS_PORT=6379 \
  --from-literal=LIBRE_TRANSLATE_ENDPOINT=http://sumomo.default.svc.cluster.local:5000 \
  --from-literal=ALLOWED_PRIVATE_ADDRESSES=10.0.0.0/8 \
  --from-literal=SINGLE_USER_MODE=true >/dev/null

helm install magout-cluster-wide --repo "$MAGOUT_REPO" magout-cluster-wide >/dev/null
cat > "$WORK/magout-values.yaml" <<EOF
serverName: localhost:8080
mastodonVersion:
  image: ghcr.io/mastodon/mastodon:$MASTODON_VERSION
  streamingImage: ghcr.io/mastodon/mastodon-streaming:$MASTODON_VERSION
mastodonServer:
  web:
    envFrom:
    - secretRef: {name: sumomo-mastodon-env}
  sidekiq:
    envFrom:
    - secretRef: {name: sumomo-mastodon-env}
  streaming:
    envFrom:
    - secretRef: {name: sumomo-mastodon-env}
EOF
helm install magout -n "$NS" --repo "$MAGOUT_REPO" -f "$WORK/magout-values.yaml" magout >/dev/null

log "waiting for Mastodon to become ready (this pulls images and runs migrations)"
for d in magout-web magout-streaming magout-sidekiq; do
  wait_ready "$d" 1200 || fail "deployment $d did not become ready"
done

# --- 5. user + access token ---------------------------------------------
log "creating test user and access token"
kubectl exec -n "$NS" deploy/magout-web -- \
  bin/tootctl accounts create "$USER_NAME" --email "$USER_EMAIL" --confirmed >/dev/null
kubectl exec -n "$NS" deploy/magout-web -- \
  bin/tootctl accounts approve "$USER_NAME" >/dev/null
TOKEN=$(kubectl exec -i -n "$NS" deploy/magout-web -- bash -c \
  "cat > /tmp/mk.rb && E2E_USER_EMAIL=$USER_EMAIL bin/rails runner /tmp/mk.rb" \
  < "$ROOT/scripts/make_token.rb" | grep -o 'TOKEN=.*' | cut -d= -f2 | tr -d '\r')
[ -n "$TOKEN" ] || fail "could not mint an access token"

# --- 6. post + translate -------------------------------------------------
log "posting an English status and translating it to Japanese"
kubectl -n "$NS" port-forward svc/magout-gateway 8080:8080 >"$WORK/pf.log" 2>&1 &
PF_PID=$!
for i in $(seq 1 30); do
  curl -s http://localhost:8080/health | grep -q OK && break
  sleep 1
done

STATUS_JSON=$(curl -s -X POST http://localhost:8080/api/v1/statuses \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"status":"The meeting has been rescheduled for next Monday. Thank you for your understanding.","language":"en"}')
STATUS_ID=$(echo "$STATUS_JSON" | jq -r '.id // empty')
[ -n "$STATUS_ID" ] || fail "could not post a status: $STATUS_JSON"

TRANSLATION=$(curl -s -X POST "http://localhost:8080/api/v1/statuses/$STATUS_ID/translate?lang=ja" \
  -H "Authorization: Bearer $TOKEN")
echo "$TRANSLATION" | jq -e '.provider == "LibreTranslate"' >/dev/null \
  || fail "unexpected provider: $TRANSLATION"
echo "$TRANSLATION" | jq -e '.content | test("月曜日")' >/dev/null \
  || fail "translation does not look right: $TRANSLATION"

CONTENT=$(echo "$TRANSLATION" | jq -r '.content')
log "translated: $CONTENT"
log "PASS: Mastodon translated a post through sumomo"
