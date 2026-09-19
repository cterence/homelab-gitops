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
DEPLOYED_APPS=$(ls ${DIR}/../k8s-apps/*/appset.yaml | xargs -n1 dirname | xargs -n1 basename | xargs)

# Fail on charts without an appset.yaml (they would not be deployed)
# or on unknown keys in an appset.yaml (a typo silently disables its flag)
DRIFT=0
ALLOWED_KEYS=$(yq -r '.properties | keys | .[]' ${DIR}/../scripts/appset.schema.json | xargs)
for f in ${DIR}/../k8s-apps/*/appset.yaml; do
  for key in $(yq -r 'to_entries | .[] | .key' "$f" | xargs); do
    if [[ ! " ${ALLOWED_KEYS} " =~ " ${key} " ]]; then
      echo "unknown key '${key}' in ${f} (allowed: ${ALLOWED_KEYS})" >&2
      DRIFT=1
    fi
  done
done
for app in ${CONFIGURED_APPS}; do
  if [[ ! " ${DEPLOYED_APPS} " =~ " ${app} " ]]; then
    echo "k8s-apps/${app} has no appset.yaml — deploy it (add appset.yaml) or archive it (move to k8s-apps/archive/)" >&2
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
