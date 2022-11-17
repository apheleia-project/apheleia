#!/bin/sh
DIR=`dirname $0`
kubectl apply -f $DIR/openshift-specific-rbac.yaml
$DIR/base-development.sh $1

