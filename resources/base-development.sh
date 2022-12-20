#!/bin/sh



DIR=`dirname $0`
kubectl apply -f $DIR/namespace.yaml
kubectl config set-context --current --namespace=hacbs-demo
kubectl create secret generic jvm-build-image-secrets --from-file=.dockerconfigjson=$HOME/.docker/config.json --type=kubernetes.io/dockerconfigjson

$DIR/patch-yaml.sh
kubectl apply -k $DIR/overlays/development

kubectl rollout restart deployment jvm-build-workspace-artifact-cache
