#!/usr/bin/env bash
# dev-deploy-server.sh — Build, push Docker image, and update Nomad job for dev testing.
#
# Flow:
#   1. Build Docker image for linux/arm64
#   2. Push to ghcr.io/trustos/hopssh:dev
#   3. Update Nomad job in oci_nomad_cluster repo to use :dev tag
#   4. Commit + push → GH runner deploys to server
#
# Prerequisites:
#   - docker buildx
#   - ghcr.io login: echo $GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin
#
# Usage: make dev-deploy-server
set -euo pipefail

IMAGE="ghcr.io/trustos/hopssh"
TAG="dev-${COMMIT}"
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
NOMAD_REPO="${NOMAD_REPO:-$(dirname "$(pwd)")/oci_nomad_cluster}"
NOMAD_JOB="${NOMAD_REPO}/jobs/hopssh.nomad.hcl"

# Verify Nomad repo exists.
if [ ! -f "${NOMAD_JOB}" ]; then
    echo "ERROR: Nomad job not found at ${NOMAD_JOB}"
    echo "Set NOMAD_REPO to the oci_nomad_cluster directory."
    exit 1
fi

# Step 1: Build and push Docker image.
echo "==> Building ${IMAGE}:${TAG} for linux/arm64 (${VERSION})..."
docker buildx build \
    --platform linux/arm64 \
    --build-arg "VERSION=${VERSION}" \
    --build-arg "COMMIT=${COMMIT}" \
    -t "${IMAGE}:${TAG}" \
    --push \
    .

echo "==> Pushed ${IMAGE}:${TAG}"

# Step 2: Update Nomad job to use dev tag.
CURRENT_TAG=$(grep 'image.*ghcr.io/trustos/hopssh' "${NOMAD_JOB}" | sed 's/.*hopssh:\([^"]*\)".*/\1/' || echo "unknown")
if [ "${CURRENT_TAG}" = "${TAG}" ]; then
    echo "==> Nomad job already uses :${TAG}"
else
    echo "==> Updating Nomad job: :${CURRENT_TAG} → :${TAG}"
    sed -i.bak "s|image *= *\"${IMAGE}:[^\"]*\"|image        = \"${IMAGE}:${TAG}\"|" "${NOMAD_JOB}"
    rm -f "${NOMAD_JOB}.bak"
fi

# Step 3: Commit and push Nomad repo.
echo "==> Pushing Nomad job update..."
cd "${NOMAD_REPO}"
git add jobs/hopssh.nomad.hcl
git commit -m "hopssh: dev deploy ${VERSION} (${COMMIT})" 2>/dev/null || echo "    (no changes to commit)"
git push 2>/dev/null || echo "    WARNING: git push failed — push manually"

echo ""
echo "==> Done."
echo "    Image:  ${IMAGE}:${TAG} (${VERSION})"
echo "    Nomad:  ${NOMAD_JOB}"
echo "    The GH runner will pick up the job and deploy."
