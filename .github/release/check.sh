#!/usr/bin/env bash
set -euo pipefail

recipe_root=$(cd "$(dirname "$0")" && pwd)
export GOROOT=$1 GOTOOLCHAIN=local GOWORK=off GOARM64=v8.1,lse
target_os=$2
target_arch=$3
go_cmd="$GOROOT/bin/go"
check_dir=$(mktemp -d)
trap 'rm -rf "$check_dir"' EXIT

if [[ "$target_os/$target_arch" == "$("$go_cmd" env GOHOSTOS)/$("$go_cmd" env GOHOSTARCH)" ]]; then
  "$go_cmd" -C "$recipe_root/smoke" test -timeout 120s ./...
fi
if ! GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 \
  "$go_cmd" -C "$recipe_root/smoke" test -c -timeout 120s -gcflags=code.hybscloud.com/atomix=-S \
  -o "$check_dir/smoke.test" . 2> "$check_dir/codegen.txt"; then
  cat "$check_dir/codegen.txt" >&2
  exit 1
fi

# Inspect only the wrappers; test-generated functions may legitimately call.
awk '/^[^[:space:]]/ {show=($0 ~ /^code.hybscloud.com\/atomix\.[[:alnum:]_]+ STEXT/)} show' \
  "$check_dir/codegen.txt" > "$check_dir/intrinsics.txt"
test -s "$check_dir/intrinsics.txt"
if grep -E '[[:space:]]CALL[[:space:]]' "$check_dir/intrinsics.txt"; then
  echo 'Atomix intrinsic wrapper contains a call' >&2
  exit 1
fi

# Variable operands exercise native RMW lowering. Go's disassembler does
# not decode every ARM64 LSE instruction, so use the compiler assembly listing.
for check in \
  Add:XADDQ:LDADDALD Add32:XADDL:LDADDALW \
  Or:CMPXCHGQ:LDORALD Or32:CMPXCHGL:LDORALW \
  Xor:CMPXCHGQ:LDEORALD Xor32:CMPXCHGL:LDEORALW \
  And:CMPXCHGQ:LDCLRALD And32:CMPXCHGL:LDCLRALW; do
  IFS=: read -r name amd64_instruction arm64_instruction <<< "$check"
  awk -v name="code.hybscloud.com/atomix.$name" \
    '$1 == name && $2 == "STEXT" {show=1; next} show && /^[^[:space:]]/ {exit} show' \
    "$check_dir/intrinsics.txt" > "$check_dir/operation.txt"
  case "$target_arch" in
    amd64) expected=(LOCK "$amd64_instruction") ;;
    arm64) expected=("$arm64_instruction") ;;
    *) echo "Unsupported intrinsic target: $target_arch" >&2; exit 1 ;;
  esac
  for instruction in "${expected[@]}"; do
    if ! grep -Eq "[[:space:]]$instruction([[:space:]]|$)" "$check_dir/operation.txt"; then
      echo "$name: missing $instruction on $target_os/$target_arch" >&2
      cat "$check_dir/operation.txt" >&2
      exit 1
    fi
  done
done

# Preserve identity constant folding as a single load and return. This also
# rejects missing wrappers, calls, RMW sequences, and added fences.
for check in \
  AddZero:MOVQ:LDAR AddZero32:MOVL:LDARW \
  OrZero:MOVQ:LDAR OrZero32:MOVL:LDARW \
  XorZero:MOVQ:LDAR XorZero32:MOVL:LDARW \
  AndAllOnes:MOVQ:LDAR AndAllOnes32:MOVL:LDARW; do
  IFS=: read -r name amd64_instruction arm64_instruction <<< "$check"
  awk -v name="code.hybscloud.com/atomix.$name" \
    '$1 == name && $2 == "STEXT" {show=1; next} show && /^[^[:space:]]/ {exit} show' \
    "$check_dir/intrinsics.txt" > "$check_dir/operation.txt"
  case "$target_arch" in
    amd64) expected=$amd64_instruction ;;
    arm64) expected=$arm64_instruction ;;
  esac
  instructions=$(awk -F '\t' '$3 != "" && $3 !~ /^(TEXT|FUNCDATA|PCDATA)$/ {print $3}' \
    "$check_dir/operation.txt")
  if [[ "$instructions" != "$expected"$'\nRET' ]]; then
    echo "$name: identity folding must emit only $expected and RET on $target_os/$target_arch" >&2
    cat "$check_dir/operation.txt" >&2
    exit 1
  fi
done
