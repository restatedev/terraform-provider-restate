#!/usr/bin/env bash
# Runs a module's real region_grant.tf with the restate provider built from this repository, against
# a fake Restate Cloud API (server.mjs), and checks the order of the calls. The cloud's own resources are
# replaced by terraform_data stubs that post an event to the fake API when they are created and
# destroyed, so the log shows when the account would have changed relative to Restate Cloud.
#
# The module's region_grant.tf is rewritten with <cloud>.sed to refer to the stubs. Nothing here
# reaches a cloud or a real Restate API.
#
# Usage: tests/integration/run.sh [aws|gcp|azure ...]
# Environment: TERRAFORM (default terraform), MODULE_ROOT (default: the repository root), KEEP=1
# keeps the working directories.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=${MODULE_ROOT:-$(cd "$here/../.." && pwd)}
tf=${TERRAFORM:-terraform}

# The provider is not installed from a registry: it is built from this repository into a filesystem
# mirror that only it is served from.
mirror=$(mktemp -d)
platform=$(go env GOOS)_$(go env GOARCH)
mkdir -p "$mirror/registry.terraform.io/restatedev/restate/0.0.1/$platform"
(cd "$root/../.." && go build -o "$mirror/registry.terraform.io/restatedev/restate/0.0.1/$platform/terraform-provider-restate_v0.0.1" .)
cat > "$mirror/cli.tfrc" <<EOF
provider_installation {
  filesystem_mirror {
    path    = "$mirror"
    include = ["registry.terraform.io/restatedev/restate"]
  }
  direct {
    exclude = ["registry.terraform.io/restatedev/restate"]
  }
}
EOF
export TF_CLI_CONFIG_FILE=$mirror/cli.tfrc

for name in $(env | grep -E '^(AWS_|GOOGLE_|ARM_)' | cut -d= -f1); do unset "$name"; done

if [ "$#" -eq 0 ]; then
  set -- aws gcp azure
fi

servers=()
workdirs=()
cleanup() {
  [ "${#servers[@]}" -eq 0 ] || kill "${servers[@]}" 2>/dev/null || true
  [ -n "${KEEP:-}" ] || { [ "${#workdirs[@]}" -eq 0 ] || rm -rf "${workdirs[@]}"; }
}
trap cleanup EXIT
cleanup_mirror() { cleanup; rm -r -f "${mirror:?}"; }
trap cleanup_mirror EXIT

fail() {
  echo "FAIL ($cloud): $*" >&2
  echo "--- fake API log" >&2
  cat "$work/server.log" >&2
  exit 1
}

# line_of <log> <regex>: the number of the first line that matches, or nothing.
line_of() {
  grep -nE -- "$2" "$1" | head -n 1 | cut -d: -f1 || true
}

# before <log> <regex> <regex>: the first match of the first regex comes before the first match of the second.
before() {
  local a b
  a=$(line_of "$1" "$2")
  b=$(line_of "$1" "$3")
  [ -n "$a" ] || fail "no log line matches: $2"
  [ -n "$b" ] || fail "no log line matches: $3"
  [ "$a" -lt "$b" ] || fail "expected '$2' before '$3' (lines $a and $b)"
}

# has <log> <regex> <message>
has() {
  [ -n "$(line_of "$1" "$2")" ] || fail "$3"
}

count() {
  grep -cE -- "$2" "$1" || true
}

t() {
  local sub=$1
  shift
  "$tf" -chdir="$module" "$sub" -no-color -input=false "$@"
}

run_cloud() {
  cloud=$1
  [ -f "$here/$cloud.sed" ] || { echo "unknown cloud: $cloud" >&2; exit 2; }

  work=$(mktemp -d)
  module=$work/module
  mkdir "$module"
  workdirs+=("$work")
  : > "$work/server.log"
  node "$here/server.mjs" "$work/server.log" "$work/port" &
  servers+=($!)

  local waited=0
  until [ -s "$work/port" ]; do
    kill -0 "${servers[-1]}" 2>/dev/null || fail "the fake API did not start"
    waited=$((waited + 1))
    [ "$waited" -lt 100 ] || fail "the fake API did not write its port within 10 seconds"
    sleep 0.1
  done
  local port stubs
  port=$(cat "$work/port")
  stubs=$(cut -d" " -f1 "$here/$cloud.stubs")

  sed -f "$here/common.sed" -f "$here/$cloud.sed" "$root/$cloud/region_grant.tf" > "$module/region_grant.tf"
  cp "$here/common.tf" "$here/$cloud.tf" "$module/"
  # Each line of <cloud>.stubs is a stub and the stubs it depends on, as the cloud's resources do.
  local line stub deps depends
  while read -r line; do
    stub=${line%% *}
    deps=
    [ "$stub" = "$line" ] || deps=${line#* }
    depends=
    for dep in $deps; do
      depends="${depends:+$depends, }terraform_data.$dep"
    done
    cat >> "$module/stubs.tf" <<EOF
resource "terraform_data" "$stub" {
  input            = "$stub-\${restate_cloud_region_grant.this[0].id}"
  triggers_replace = ["http://127.0.0.1:\${var.port}/__evt/$stub"]
  depends_on       = [$depends]

  provisioner "local-exec" {
    command = "curl -fsS -X POST \${self.triggers_replace[0]}-created"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "curl -fsS -X POST \${self.triggers_replace[0]}-destroyed"
  }
}

EOF
  done < "$here/$cloud.stubs"

  for display_name in first second; do
    printf '{"port":%s,"region_grant":{"owner":"ACCOUNT","display_name":"%s"}}\n' \
      "$port" "$display_name" > "$work/$display_name.json"
  done

  local mark plog=$work/phase.log
  mark_log() {
    mark=$(wc -l < "$work/server.log")
  }
  capture() {
    tail -n +"$((mark + 1))" "$work/server.log" > "$plog"
  }

  echo "== $cloud: init"
  t init -backend=false > "$work/init.log" 2>&1 || { cat "$work/init.log" >&2; fail "init"; }

  echo "== $cloud: apply"
  mark_log
  t apply -auto-approve -var-file="$work/first.json" > "$work/apply1.log" 2>&1 || { cat "$work/apply1.log" >&2; fail "first apply"; }
  capture
  [ "$(count "$plog" '^REQ CreateRegionGrant')" -eq 1 ] || fail "one apply must create one grant"
  for stub in $stubs; do
    before "$plog" '^RES CreateRegionGrant 200' "^EVT $stub-created"
    before "$plog" "^EVT $stub-created" '^REQ ConfirmRegionGrant'
  done
  before "$plog" '^REQ CreateRegionGrant' '^REQ ConfirmRegionGrant'
  has "$plog" '^RES ConfirmRegionGrant 200' "Restate Cloud must answer the confirmation"
  [ "$("$tf" -chdir="$module" output -raw status)" = READY ] || fail "the status output must be READY"
  local first_id
  first_id=$("$tf" -chdir="$module" output -raw grant_id)

  echo "== $cloud: refresh and plan"
  mark_log
  t plan -detailed-exitcode -var-file="$work/first.json" > "$work/plan.log" 2>&1 || { cat "$work/plan.log" >&2; fail "a plan after the apply must show no change"; }
  capture
  [ "$(count "$plog" '^REQ CreateRegionGrant')" -eq 0 ] || fail "a refresh must never create a grant"

  echo "== $cloud: change a setting"
  mark_log
  t apply -auto-approve -refresh=false -var-file="$work/second.json" > "$work/apply2.log" 2>&1 || { cat "$work/apply2.log" >&2; fail "second apply"; }
  capture
  local second_id
  second_id=$("$tf" -chdir="$module" output -raw grant_id)
  [ "$first_id" != "$second_id" ] || fail "a changed display name must replace the grant"
  has "$plog" '^REQ CreateRegionGrant .*"displayName":"second"' "the new settings must create a grant"
  has "$plog" "^REQ DeleteRegionGrant .*$first_id" "the old grant must be deleted when it is replaced"
  before "$plog" '^REQ CreateRegionGrant .*"displayName":"second"' "^REQ ConfirmRegionGrant .*$second_id"

  echo "== $cloud: destroy"
  mark_log
  t apply -destroy -auto-approve -refresh=false -var-file="$work/second.json" > "$work/destroy.log" 2>&1 || { cat "$work/destroy.log" >&2; fail "destroy"; }
  capture
  [ "$(count "$plog" "^REQ DeleteRegionGrant .*$first_id")" -eq 0 ] || fail "the destroy must delete the new grant only"
  for stub in $stubs; do
    before "$plog" "^RES DeleteRegionGrant 200 .*$second_id" "^EVT $stub-destroyed"
  done
  curl -fsS -X POST "http://127.0.0.1:$port/cloud/acc_1/DescribeRegionGrant" -d "{\"grantId\":\"$second_id\"}" | grep -q DELETED ||
    fail "the grant must be DELETED at Restate Cloud after the destroy"

  echo "== $cloud: ok"
}

for cloud in "$@"; do
  run_cloud "$cloud"
done
