#!/bin/sh

DIR=`dirname $0`

IMAGE=$(kubectl get deployment -n jvm-build-service hacbs-jvm-operator -o jsonpath='{.spec.template.spec.containers[0].image}')
if [ -z "$IMAGE" ]; then
    echo "JVM Build Service Not Installed. Run ./deploy-external.sh first."
    exit 1
fi

oc new-project apheleia-workspace
kubectl config set-context --current --namespace=apheleia-workspace
kubectl create secret generic jvm-build-image-secrets --from-file=.dockerconfigjson=$HOME/.docker/config.json --type=kubernetes.io/dockerconfigjson

echo "Installing the Operator"

rm -r $DIR/overlays/development
find $DIR -name dev-template -exec cp -r {} {}/../development \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_TOKEN/${QUAY_TOKEN}/ {} \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_USERNAME/${QUAY_USERNAME}/ {} \;

kubectl apply -k $DIR/overlays/development

kubectl rollout restart deployment jvm-build-workspace-artifact-cache
kubectl rollout restart deployment -n jvm-build-service apheleia-operator
