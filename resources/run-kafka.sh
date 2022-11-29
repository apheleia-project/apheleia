#!/bin/sh

# This command runs the sample-component-build pipeline to build
# the Quarkus getting-started quickstart: https://github.com/quarkusio/quarkus-quickstarts/tree/main/getting-started

DIR=`dirname "$0"`

echo
echo "👉 Registering sample pipeline:"
echo

kubectl apply -f $DIR/gradle-pipeline.yaml

echo
echo "👉 Running the pipeline with a sample project:"
echo

kubectl create -f $DIR/run-kafka.yaml

echo
echo "🎉 Done! You can watch logs now with the following command: tkn pr logs --last -f"
