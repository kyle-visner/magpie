#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH OUTPUT_DIR" >&2
  exit 2
fi

readonly version="$1"
readonly output_dir="$2"
readonly minimum_go_major=1
readonly minimum_go_minor=26
readonly minimum_go_patch=5

if [[ ! "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "release version must use vMAJOR.MINOR.PATCH" >&2
  exit 2
fi

go_version="$(go env GOVERSION)"
go_version="${go_version#go}"
IFS=. read -r go_major go_minor go_patch_extra <<<"${go_version}"
go_patch="${go_patch_extra%%[^0-9]*}"
if ((go_major < minimum_go_major ||
     (go_major == minimum_go_major && go_minor < minimum_go_minor) ||
     (go_major == minimum_go_major && go_minor == minimum_go_minor && go_patch < minimum_go_patch))); then
  echo "Go 1.26.5 or later is required; found $(go env GOVERSION)" >&2
  exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
  echo "release builds require a clean working tree" >&2
  exit 1
fi

if [[ "${MAGPIE_ALLOW_UNTAGGED:-0}" != "1" ]]; then
  exact_tag="$(git describe --tags --exact-match 2>/dev/null || true)"
  if [[ "${exact_tag}" != "${version}" ]]; then
    echo "HEAD must be tagged ${version}" >&2
    exit 1
  fi
fi

if [[ -e "${output_dir}" ]] && [[ -n "$(find "${output_dir}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "output directory must not exist or must be empty: ${output_dir}" >&2
  exit 1
fi
mkdir -p "${output_dir}"

tar_command="${TAR_CMD:-tar}"
if ! "${tar_command}" --version 2>/dev/null | head -1 | grep -q "GNU tar"; then
  echo "GNU tar is required; on macOS set TAR_CMD=gtar" >&2
  exit 1
fi

build_root="$(mktemp -d "${TMPDIR:-/tmp}/magpie-release.XXXXXX")"
cleanup() {
  case "${build_root}" in
    "${TMPDIR:-/tmp}"/magpie-release.*) rm -rf -- "${build_root}" ;;
    *) echo "refusing unsafe cleanup target: ${build_root}" >&2; exit 1 ;;
  esac
}
trap cleanup EXIT

readonly source_date_epoch="$(git show -s --format=%ct HEAD)"
readonly release_name="magpie_${version#v}"
targets=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
)

for target in "${targets[@]}"; do
  read -r target_os target_arch <<<"${target}"
  archive_name="${release_name}_${target_os}_${target_arch}.tar.gz"
  stage_dir="${build_root}/${target_os}-${target_arch}"
  mkdir -p "${stage_dir}"

  CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" \
    go build -trimpath -ldflags="-s -w" \
    -o "${stage_dir}/magpie" ./cmd/magpie
  install -m 0644 LICENSE README.md llm.md "${stage_dir}/"
  install -d \
    "${stage_dir}/docs/assets" \
    "${stage_dir}/scripts" \
    "${stage_dir}/.github/workflows"
  install -m 0644 docs/SECURITY.md "${stage_dir}/docs/"
  install -m 0644 docs/assets/magpie-logo.png "${stage_dir}/docs/assets/"
  install -m 0755 scripts/build-release.sh "${stage_dir}/scripts/"
  install -m 0644 .github/workflows/release.yml "${stage_dir}/.github/workflows/"
  printf '%s\n' "${version}" >"${stage_dir}/VERSION"

  "${tar_command}" \
    --sort=name \
    --mtime="@${source_date_epoch}" \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    -C "${stage_dir}" \
    -cf - . |
    gzip -n >"${output_dir}/${archive_name}"
done

(
  cd "${output_dir}"
  sha256sum ./*.tar.gz | LC_ALL=C sort -k2 >SHA256SUMS
)

echo "built ${version} release archives in ${output_dir}"
