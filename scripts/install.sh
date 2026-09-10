#!/bin/sh
set -eu

REPOSITORY="duongnvt2110/grin"
VERSION=""
INSTALL_DIR="${HOME:-}/.local/bin"

usage() {
    cat <<'EOF'
Usage: install.sh [--version VERSION] [--dir DIRECTORY]

Install the latest Grin binary, or a specific release version.
EOF
}

fail() {
    printf 'grin installer: %s\n' "$1" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || fail "--version requires a value"
            VERSION="$2"
            shift 2
            ;;
        --dir)
            [ "$#" -ge 2 ] || fail "--dir requires a value"
            INSTALL_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "unknown argument: $1"
            ;;
    esac
done

[ -n "$INSTALL_DIR" ] || fail "install directory is empty"

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v awk >/dev/null 2>&1 || fail "awk is required"
command -v mktemp >/dev/null 2>&1 || fail "mktemp is required"
command -v install >/dev/null 2>&1 || fail "install is required"

case "$(uname -s)" in
    Darwin) os="Darwin" ;;
    Linux)  os="Linux" ;;
    *)      fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
    arm64|aarch64) arch="arm64" ;;
    amd64|x86_64)  arch="amd64" ;;
    *)             fail "unsupported architecture: $(uname -m)" ;;
esac

archive="grin_${os}_${arch}.tar.gz"
release_label="latest"
if [ -n "$VERSION" ]; then
    case "$VERSION" in
        v*) tag="$VERSION" ;;
        *)  tag="v$VERSION" ;;
    esac
    base_url="https://github.com/$REPOSITORY/releases/download/$tag"
    release_label="$tag"
else
    base_url="https://github.com/$REPOSITORY/releases/latest/download"
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/grin-install.XXXXXX")
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup 0
trap 'exit 1' 1 2 15

curl -fsSL --retry 3 "$base_url/$archive" -o "$tmp_dir/$archive" || \
    fail "could not download $archive for $release_label"
curl -fsSL --retry 3 "$base_url/checksums.txt" -o "$tmp_dir/checksums.txt" || \
    fail "could not download checksums for $release_label"

expected=$(awk -v name="$archive" '$2 == name { print $1 "  " $2; found=1; exit } END { if (!found) exit 1 }' "$tmp_dir/checksums.txt") || \
    fail "checksum entry for $archive was not found"
printf '%s\n' "$expected" >"$tmp_dir/selected.checksum"

if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp_dir" && sha256sum -c selected.checksum) || fail "checksum verification failed"
elif command -v shasum >/dev/null 2>&1; then
    (cd "$tmp_dir" && shasum -a 256 -c selected.checksum) || fail "checksum verification failed"
else
    fail "sha256sum or shasum is required for checksum verification"
fi

tar -xzf "$tmp_dir/$archive" -C "$tmp_dir" || fail "could not extract $archive"
[ -f "$tmp_dir/grin" ] || fail "release archive did not contain the grin binary"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp_dir/grin" "$INSTALL_DIR/grin"

printf 'Installed Grin (%s) to %s\n' "$release_label" "$INSTALL_DIR/grin"
case ":${PATH:-}:" in
    *:"$INSTALL_DIR":*) ;;
    *) printf 'Add it to PATH with: export PATH="%s:$PATH"\n' "$INSTALL_DIR" ;;
esac
