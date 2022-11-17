#!/bin/sh


function cleanAllArtifacts() {
     kubectl delete namespaces apheleia
}

kubectl delete deployments.apps jvm-build-workspace-artifact-cache
if [ "$1" = "--clean" ]; then
    cleanAllArtifacts
fi

DIR=`dirname $0`
kubectl apply -f $DIR/namespace.yaml
kubectl config set-context --current --namespace=apheleia
$DIR/patch-yaml.sh
kubectl apply -k $DIR/overlays/development

