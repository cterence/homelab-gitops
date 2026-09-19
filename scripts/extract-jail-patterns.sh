#!/usr/bin/env bash
# Generates the fail2ban-derived section of the traefik-jail pattern list
# (the patterns.txt block in k8s-apps/traefik/templates/traefik-jail.yaml).
#
# Source: fail2ban's botsearch filters (apache-botsearch / nginx-botsearch,
# both backed by botsearch-common.conf). The [Init] definitions are expanded
# into the `block` alternation, which is then split into branches at top-level
# `|`. Each branch is anchored to the path start and given a token-boundary
# suffix so prefix collisions cannot false-positive (e.g. /pma matches but
# /pmarticles does not).
#
# apache-noscript.conf is deliberately NOT imported: its script regex matches
# any path containing php/asp/exe/pl, which under the patternWeight scheme
# would jail legitimate clients after a handful of normal requests.
#
# Patterns are security-sensitive: a bad pattern costs patternWeight errors
# per hit. Always review this script's output before copying it into the
# ConfigMap template, and drop curated lines that the derived patterns make
# redundant.
#
# Usage: extract-jail-patterns.sh
# Env:   FAIL2BAN_REF  fail2ban commit to pin (default below)
# Requires network access (curl) and GNU-compatible sed.

set -euo pipefail

FAIL2BAN_REF="${FAIL2BAN_REF:-7212404e8f937762a80325ba28b402a1f55535b0}"
BASE_URL="https://raw.githubusercontent.com/fail2ban/fail2ban/${FAIL2BAN_REF}/config/filter.d/botsearch-common.conf"
TEMPLATE="$(dirname "$0")/../k8s-apps/traefik/templates/traefik-jail.yaml"
BOUNDARY='(?:/|[-._0-9]|$)'

conf=$(curl -fsSL "$BASE_URL")

# Collect the [Init] definitions (name = value)
defs=$(printf '%s\n' "$conf" | sed -n 's/^\([a-zA-Z_]*\) = \(.*\)$/\1\t\2/p')

block=$(printf '%s\n' "$conf" | sed -n 's/^block = //p' | head -1)
if [ -z "$block" ]; then
  echo "error: no 'block' definition found in botsearch-common.conf" >&2
  exit 1
fi

# Strip the leading \/? and the trailing [^,]*: we anchor ourselves
core=$(printf '%s' "$block" | sed -e 's|^\\\/?||' -e 's|\[\^,\]\*$||')
# Strip the outer parens: (<alternation>)
core="${core#(}"
core="${core%)}"

# Expand <name> references from the [Init] definitions
while IFS=$'\t' read -r name value; do
  core="${core//<$name>/$value}"
done <<<"$defs"

# Split at top-level `|` (paren depth 0). The botsearch definitions contain
# no escaped parens, so character-level depth tracking is safe here.
branches=$(printf '%s' "$core" | awk '{
  depth = 0; out = ""
  n = split($0, chars, "")
  for (i = 1; i <= n; i++) {
    c = chars[i]
    if (c == "(") depth++
    if (c == ")") depth--
    if (c == "|" && depth == 0) { print out; out = "" } else { out = out c }
  }
  if (out != "") print out
}')

# Emit the merged file: curated section from the template (cut at the
# fail2ban-derived marker so repeated runs are idempotent), then the
# fail2ban-derived section with provenance.
sed -n '/^  patterns.txt: |$/,/^  go.mod: |$/{/^  go.mod: |$/d; /^  patterns.txt: |$/d; p;}' "$TEMPLATE" |
  sed 's/^    //' |
  awk '/^# --- fail2ban-derived:/{exit} {print}' |
  sed -e '${/^$/d}'
echo ""
echo "# --- fail2ban-derived: https://github.com/fail2ban/fail2ban"
echo "# --- pinned to ${FAIL2BAN_REF}, from config/filter.d/botsearch-common.conf"
echo "# --- regenerate with scripts/extract-jail-patterns.sh and review the diff"
while IFS= read -r branch; do
  [ -n "$branch" ] || continue
  printf '(?i)^/%s%s\n' "$branch" "$BOUNDARY"
done <<<"$branches"
