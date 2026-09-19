#!/usr/bin/env bash

set -e

DIR=$(dirname "$0")
CONFIGURED_APPS=$(ls ${DIR}/../k8s-apps | xargs)
EXCLUDED_APPS=$(for app in ${CONFIGURED_APPS}; do
  # if deployed-apps:exclude is in the app's Chart.yaml, exclude it from the list
  if grep -q "deployed-apps:exclude" ${DIR}/../k8s-apps/${app}/Chart.yaml 2>/dev/null; then
    echo ${app}
  fi
done)
EXCLUDED_APPS="${EXCLUDED_APPS} 0_template archive"
# Remove excluded apps from the list of configured apps
CONFIGURED_APPS=$(echo ${CONFIGURED_APPS} ${EXCLUDED_APPS} | tr ' ' '\n' | sort | uniq -u | xargs)
DEPLOYED_APPS=$(yq -r '.spec.generators[0].list.elements[].name' ${DIR}/../argocd-apps/applicationset.yaml | xargs)

# Fail on drift between k8s-apps directories and applicationset entries
DRIFT=0
for app in ${CONFIGURED_APPS}; do
  if [[ ! " ${DEPLOYED_APPS} " =~ " ${app} " ]]; then
    echo "k8s-apps/${app} has no applicationset entry" >&2
    DRIFT=1
  fi
done
for app in ${DEPLOYED_APPS}; do
  if [[ ! " ${CONFIGURED_APPS} " =~ " ${app} " ]]; then
    echo "applicationset entry ${app} has no k8s-apps directory" >&2
    DRIFT=1
  fi
done
if [[ ${DRIFT} -eq 1 ]]; then
  exit 1
fi

# Create markdown table with the list of configured apps
echo "| App | Description |" > ${DIR}/../deployed-apps.md
echo "| --- | --- |" >> ${DIR}/../deployed-apps.md
for app in ${CONFIGURED_APPS}; do
  DESCRIPTION=$(yq -r '.description' ${DIR}/../k8s-apps/${app}/Chart.yaml)
  echo "| [${app}](${DIR}/../k8s-apps/${app}) | ${DESCRIPTION} |" >> ${DIR}/../deployed-apps.md
done

# Replace the content between the markers (BEGIN/END table) in the README.md file with multiline table deployed-apps.md
sed -i -e "/<!-- BEGIN deployed-apps -->/,/<!-- END deployed-apps -->/{ /<!-- BEGIN deployed-apps -->/{p; r ${DIR}/../deployed-apps.md
}; /<!-- END deployed-apps -->/p; d }" ${DIR}/../README.md

rm ${DIR}/../deployed-apps.md
