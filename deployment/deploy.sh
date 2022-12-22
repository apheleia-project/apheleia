#!/bin/sh

DIR=`dirname $0`

kubectl apply -k $DIR/tekton
kubectl apply -k $DIR/crds
kubectl apply -k $DIR/build-operator
kubectl apply -k $DIR/apheleia-operator

#restart the operators
kubectl rollout restart deployment -n jvm-build-service hacbs-jvm-operator apheleia-operator
