#!/bin/sh

DIR=`dirname $0`
kubectl apply -f https://github.com/tektoncd/pipeline/releases/download/v0.41.1/release.yaml
while ! oc get pods -n tekton-pipelines | grep tekton-pipelines-controller | grep Running; do
    sleep 1
done

echo "Now applying build-operator"

if ! kubectl apply -k $DIR/build-operator;
then
  #CRD's sometimes don't apply in time, install is racey
  sleep 5
  kubectl apply -k $DIR/build-operator
fi

kubectl create sa pipeline

kubectl apply -f $DIR/ci-namespace.yaml

kubectl config set-context --current --namespace=apheleia-ci

echo "Now applying minikube config"
kubectl apply -k $DIR/overlays/minikube

timeout=60
endTime=$(( $(date +%s) + timeout ))

while ! oc get pods -n jvm-build-service | grep apheleia-operator | grep Running; do
    sleep 1
    if [ $(date +%s) -gt $endTime ]; then
        exit 1
    fi
done
echo "Apheleia Running"
