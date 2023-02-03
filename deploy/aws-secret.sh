#!/bin/sh
#This scripts sets up the AWS Secret Key needed for deployments to CodeArtifact
kubectl create secret generic aws-secrets --from-literal=access-key=$AWS_ACCESS_KEY --from-literal=secret-key=$AWS_SECRET_KEY
