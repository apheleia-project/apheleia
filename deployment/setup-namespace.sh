#!/bin/sh
set -euo pipefail
DIR=$(dirname $0)

if [ "$1" = "" ]; then
  echo "Usage: ./setup-namespace.sh namespace"
  exit 1
fi
oc project $1
kubectl apply -k $DIR/namespace
