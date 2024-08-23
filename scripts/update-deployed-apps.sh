#!/usr/bin/env bash

set -e

DIR=$(dirname "$0")
CONFIGURED_APPS=$(ls ${DIR}/../k8s-apps | xargs)
EXCLUDED_APPS=$(for app in ${CONFIGURED_APPS}; do
  # if deployed-apps:exclude is in the app's Chart.yaml, exclude it from the list
  if grep -q "deployed-apps:exclude" ${DIR}/../k8s-apps/${app}/Chart.yaml; then
    echo ${app}
  fi
done)
# Remove excluded apps from the list of configured apps
CONFIGURED_APPS=$(echo ${CONFIGURED_APPS} ${EXCLUDED_APPS} | tr ' ' '\n' | sort | uniq -u)
DEPLOYED_APPS=$(yq -r '.spec.generators[0].list.elements[].name' ${DIR}/../argocd-apps/applicationset.yaml | xargs)

# Create markdown table with the list of configured apps and a checkmark if the app is deployed
echo "| App | Description | Is deployed |" > ${DIR}/../deployed-apps.md
echo "| --- | --- | --- |" >> ${DIR}/../deployed-apps.md
for app in ${CONFIGURED_APPS}; do
  DESCRIPTION=$(yq -r '.description' ${DIR}/../k8s-apps/${app}/Chart.yaml)
  if [[ " ${DEPLOYED_APPS} " =~ " ${app} " ]]; then
    echo "| [${app}](${DIR}/../k8s-apps/${app}) | ${DESCRIPTION} | ✅ |" >> ${DIR}/../deployed-apps.md
  else
    echo "| [${app}](${DIR}/../k8s-apps/${app}) | ${DESCRIPTION} | ❌ |" >> ${DIR}/../deployed-apps.md
  fi
done

# Replace the content between the markers (BEGIN/END table) in the README.md file with multiline table deployed-apps.md
sed -i -e "/<!-- BEGIN deployed-apps -->/,/<!-- END deployed-apps -->/{ /<!-- BEGIN deployed-apps -->/{p; r ${DIR}/../deployed-apps.md
}; /<!-- END deployed-apps -->/p; d }" ${DIR}/../README.md

rm ${DIR}/../deployed-apps.md
