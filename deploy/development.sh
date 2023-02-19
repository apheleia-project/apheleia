#!/bin/sh

DIR=`dirname $0`

if [ -z "$QUAY_USERNAME" ] || [ -z "$AWS_MAVEN_REPO" ]; then
    echo "Set QUAY_USERNAME and AWS_MAVEN_REPO"
    exit 1
fi
if [ -z "$AWS_SECRET_KEY" ] || [ -z "$AWS_ACCESS_KEY" ]; then
    echo "Set AWS_SECRET_KEY and AWS_ACCESS_KEY so that the aws-secret may be created"
    exit 1
fi

IMAGE=$(kubectl get deployment -n jvm-build-service hacbs-jvm-operator -o jsonpath='{.spec.template.spec.containers[0].image}')
if [ -z "$IMAGE" ]; then
    echo "JVM Build Service Not Installed. Run ./deploy-external.sh first."
    exit 1
fi

oc new-project apheleia-workspace
kubectl config set-context --current --namespace=apheleia-workspace
kubectl delete secret jvm-build-image-secrets --ignore-not-found
kubectl create secret generic jvm-build-image-secrets --from-file=.dockerconfigjson=$HOME/.docker/config.json --type=kubernetes.io/dockerconfigjson
kubectl delete secret aws-secrets --ignore-not-found
kubectl create secret generic aws-secrets --from-literal=access-key=$AWS_ACCESS_KEY --from-literal=secret-key=$AWS_SECRET_KEY

echo "Installing the Operator"
rm -r $DIR/overlays/development
find $DIR -name dev-template -exec cp -r {} {}/../development \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_USERNAME/${QUAY_USERNAME}/ {} \;
find $DIR -path \*development\*.yaml -exec sed -i "s#AWS_MAVEN_REPO#${AWS_MAVEN_REPO}#" {} \;
if [ -n "$AWS_DOMAIN" ]; then
    find $DIR -path \*development\*.yaml -exec sed -i s/AWS_DOMAIN/${AWS_DOMAIN}/ {} \;
else
    find $DIR -path \*development\*.yaml -exec sed -i s/AWS_DOMAIN/$(yq '.data.aws-domain' user-namespace/apheleia-config.yaml)/ {} \;
fi
if [ -n "$AWS_OWNER" ]; then
    find $DIR -path \*development\*.yaml -exec sed -i s/AWS_OWNER/${AWS_OWNER}/ {} \;
else
    find $DIR -path \*development\*.yaml -exec sed -i s/AWS_OWNER/$(yq '.data.aws-owner' user-namespace/apheleia-config.yaml)/ {} \;
fi

kubectl apply -k $DIR/overlays/development

kubectl rollout restart deployment jvm-build-workspace-artifact-cache
kubectl rollout restart deployment -n jvm-build-service apheleia-operator
