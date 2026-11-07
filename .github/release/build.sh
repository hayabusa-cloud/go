#!/usr/bin/env bash
set -euo pipefail

recipe_root=$(cd "$(dirname "$0")" && pwd)
artifact_dir=$(cd "$1" && pwd)
target_os=$2
target_arch=$3
driver=$4
tag=$(jq -r .tag "$artifact_dir/candidate.json")
expected=$(jq -r .source_sha256 "$artifact_dir/candidate.json")
printf '%s  %s\n' "$expected" "$artifact_dir/$tag.src.tar.gz" |
  shasum -a 256 --check

build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
tar -xzf "$artifact_dir/$tag.src.tar.gz" -C "$build_dir"
go_root="$build_dir/go"
bootstrap_root=$(go env GOROOT)
host_os=$(go env GOOS)
host_arch=$(go env GOARCH)
export GOTOOLCHAIN=local GOWORK=off
export GOARM64=v8.1,lse
export GOROOT_BOOTSTRAP="$bootstrap_root"
(
  cd "$go_root/src"
  GOOS="$target_os" GOARCH="$target_arch" ./make.bash -distpack
)

# The host go command is retained by cross builds; distpack selects target tools.
export GOROOT="$go_root"
go_cmd="$go_root/bin/go"
if [[ "$("$go_cmd" env GOVERSION)" != "$tag" ]]; then
  echo "Built toolchain does not report candidate version $tag" >&2
  exit 1
fi
if [[ "$target_os/$target_arch" == "$host_os/$host_arch" ]]; then
  "$go_cmd" -C "$go_root/src" test -p 1 -short ./cmd/compile/...
fi
bash "$recipe_root/check.sh" "$go_root" "$target_os" "$target_arch"
cp "$go_root/pkg/distpack/$tag.$target_os-$target_arch.tar.gz" "$artifact_dir/$tag.$target_os-$target_arch.tar.gz"
"$driver" record -root "$recipe_root/../.." -out "$artifact_dir" -platform "$target_os-$target_arch"
