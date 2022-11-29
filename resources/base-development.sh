#!/bin/sh


function cleanAllArtifacts() {
     kubectl delete namespaces apheleia
}

DIR=`dirname $0`
kubectl create secret generic jvm-build-secrets --from-file=.dockerconfigjson=$HOME/.docker/config.json --type=kubernetes.io/dockerconfigjson
kubectl apply -f $DIR/namespace.yaml
kubectl config set-context --current --namespace=apheleia
$DIR/patch-yaml.sh
kubectl apply -k $DIR/overlays/development

kubectl rollout restart deployment jvm-build-workspace-artifact-cache