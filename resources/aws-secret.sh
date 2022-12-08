#!/bin/sh

kubectl create secret generic aws-secrets --from-literal=access-key=$AWS_ACCESS_KEY --from-literal=secret-key=$AWS_SECRET_KEY
