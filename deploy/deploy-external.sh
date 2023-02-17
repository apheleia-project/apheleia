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

if [ -z "$AWS_SECRET_KEY" ] || [ -z "$AWS_ACCESS_KEY" ]; then
    echo "Set AWS_SECRET_KEY and AWS_ACCESS_KEY so that the aws-secret may be created"
    exit 1
fi
kubectl delete secret aws-secrets --ignore-not-found
kubectl create secret generic aws-secrets --from-literal=access-key=$AWS_ACCESS_KEY --from-literal=secret-key=$AWS_SECRET_KEY

echo "Applying tekton"
kubectl apply -k $DIR/tekton
echo "Now applying build-operator"
kubectl apply -k $DIR/build-operator
