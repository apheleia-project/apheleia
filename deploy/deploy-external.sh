#!/bin/bash

DIR=`dirname $0`

#Deploy the external services that Apheleia depends on
#If you are just working on Aphelia you should run this script
#If you are also modifying the JVM build service this script will replace
#The development version of the operator

if [ "$1" != "--force" ]; then
    IMAGE=$(kubectl get deployment -n jvm-build-service hacbs-jvm-operator -o jsonpath='{.spec.template.spec.containers[0].image}')
    if [[ "$IMAGE" == *":dev"* ]]; then
        echo "Development version of JVM Build service detected, image $IMAGE is a development image, use --force to continue"
        exit 1
    fi
fi
echo "Applying tekton"
kubectl apply -k $DIR/tekton
echo "Now applying build-operator"
kubectl apply -k $DIR/build-operator
