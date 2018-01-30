#!/bin/bash -e
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

OUTPUT_DIR="$(cd "${OUTPUT_DIR:=$DIR/..}" && pwd)"
: "${MANIFESTS_DIR:=$OUTPUT_DIR/manifests}"

(
  cat <(find "$MANIFESTS_DIR/custom-resource-definitions" -type f -name '*.yaml' -exec sh -c 'cat $0 && echo ---' {}  \;)
  echo ---
  cat "$MANIFESTS_DIR/alm/chargeback.clusterserviceversion.yaml"
)> "${MANIFESTS_DIR}/alm/install.yaml"