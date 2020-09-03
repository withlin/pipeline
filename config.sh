

#!/usr/bin/env bash
set -e

# Set your target namespace here
TARGET_NAMESPACE=wwithlin-dev

ko resolve -f config | sed -e '/kind: Namespace/!b;n;n;s/:.*/: '"${TARGET_NAMESPACE}"'/' | \
    sed "s/namespace: tekton-pipelines$/namespace: ${TARGET_NAMESPACE}/" | \
    kubectl apply -f-
kubectl set env deployments --all SYSTEM_NAMESPACE=${TARGET_NAMESPACE} -n ${TARGET_NAMESPACE}
