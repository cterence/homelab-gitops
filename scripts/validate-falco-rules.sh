#!/usr/bin/env bash
# Validate k8s-apps/falco/values.yaml customRules against the stock falco
# ruleset using the pinned falco image. Requires docker and yq.
set -euo pipefail
repo=${1:-$(git rev-parse --show-toplevel)}
values="$repo/k8s-apps/falco/values.yaml"
falco_image="docker.io/falcosecurity/falco:0.45.2"
falcoctl_image="docker.io/falcosecurity/falcoctl:0.14.3"

tmp=$(mktemp -d "$repo/.falco-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/plugins" "$tmp/rules"

for k in $(yq '.falco.customRules | keys[]' "$values"); do
  yq ".falco.customRules[\"$k\"]" "$values" > "$tmp/rules/$k"
done

docker run --rm -v "$tmp/plugins:/dst" --entrypoint sh "$falco_image" \
  -c 'cp /usr/share/falco/plugins/libcontainer.so /dst/'
docker run --rm --user root -v "$tmp/plugins:/usr/share/falco/plugins" \
  --entrypoint falcoctl "$falcoctl_image" artifact install k8smeta >/dev/null

cat > "$tmp/falco-test.yaml" <<'EOF'
plugins:
  - name: container
    library_path: /usr/share/falco/plugins/libcontainer.so
    init_config:
      label_max_len: 100
      with_size: false
  - name: k8smeta
    library_path: /usr/share/falco/plugins/libk8smeta.so
    init_config:
      collectorHostname: localhost
      collectorPort: 45000
      nodeName: test-node
EOF

fargs=(-c /etc/falco/falco-test.yaml -V /etc/falco/falco_rules.yaml)
for f in "$tmp/rules"/*.yaml; do
  fargs+=(-V "/etc/falco/rules.d/$(basename "$f")")
done

docker run --rm \
  -v "$tmp/rules:/etc/falco/rules.d" \
  -v "$tmp/plugins:/usr/share/falco/plugins:ro" \
  -v "$tmp/falco-test.yaml:/etc/falco/falco-test.yaml:ro" \
  --entrypoint falco "$falco_image" "${fargs[@]}"
