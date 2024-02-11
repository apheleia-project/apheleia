#!/bin/sh

# This command runs the sample-component-build pipeline to build
# https://github.com/stuartwdouglas/shaded-java-app - the "smaller" app picked to run in constrained openshift CI clusters

DIR=`dirname "$0"`

echo
echo "👉 Registering sample pipeline:"
echo

kubectl apply -f $DIR/maven.yaml
kubectl apply -f $DIR/git-clone.yaml

kubectl apply -f $DIR/pipeline.yaml

echo
echo "👉 Running the pipeline with the smaller repo"
echo

kubectl create -f $DIR/run-e2e-shaded-app.yaml

echo
echo "🎉 Done! You can watch logs now with the following command: tkn pr logs --last -f"
