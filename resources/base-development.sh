#!/bin/sh

DIR=`dirname $0`

#First install the dependencies
kubectl apply -k $DIR/system/tekton
kubectl apply -k $DIR/system/build-operator
kubectl apply -k $DIR/system/crds




find $DIR -name development -exec rm -r {} \;
find $DIR -name dev-template -exec cp -r {} {}/../development \;
find $DIR -path \*development\*.yaml -exec sed -i s%jvm-build-service-image%${JVM_BUILD_SERVICE_IMAGE}% {} \;
find $DIR -path \*development\*.yaml -exec sed -i s%jvm-build-service-cache-image%${JVM_BUILD_SERVICE_CACHE_IMAGE}% {} \;
find $DIR -path \*development\*.yaml -exec sed -i s%jvm-build-service-reqprocessor-image%${JVM_BUILD_SERVICE_REQPROCESSOR_IMAGE}% {} \;
find $DIR -path \*development\*.yaml -exec sed -i s/dev-template/development/ {} \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_TOKEN/${QUAY_TOKEN}/ {} \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_USERNAME/${QUAY_USERNAME}/ {} \;


kubectl apply -f $DIR/namespace.yaml
kubectl config set-context --current --namespace=hacbs-demo
kubectl create secret generic jvm-build-image-secrets --from-file=.dockerconfigjson=$HOME/.docker/config.json --type=kubernetes.io/dockerconfigjson

$DIR/patch-yaml.sh
kubectl apply -k $DIR/overlays/development

kubectl rollout restart deployment jvm-build-workspace-artifact-cache
