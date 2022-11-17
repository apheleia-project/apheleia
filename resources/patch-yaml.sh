#!/bin/sh

echo "Executing patch-yaml.sh"

DIR=`dirname $0`
find $DIR -name development -exec rm -r {} \;
find $DIR -name dev-template -exec cp -r {} {}/../development \;
find $DIR -path \*development\*.yaml -exec sed -i s/dev-template/development/ {} \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_TOKEN/${QUAY_TOKEN}/ {} \;
find $DIR -path \*development\*.yaml -exec sed -i s/QUAY_USERNAME/${QUAY_USERNAME}/ {} \;
