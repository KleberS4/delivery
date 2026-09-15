#!/bin/sh
#
# Install delivery.
#
#   curl -fsSL https://raw.githubusercontent.com/KleberS4/delivery/main/install.sh | sh
#
# The binary is verified against the SHA256SUMS published with the release
# before it is installed. A checksum that does not match aborts the install and
# leaves nothing behind: this tool's whole purpose is refusing content that is
# not what was approved, and it would be absurd to install it any other way.
#
# Environment:
#   DELIVERY_VERSION      tag to install (default: the latest release)
#   DELIVERY_INSTALL_DIR  where to put the binary (default: ~/.local/bin)

set -eu

REPO="KleberS4/delivery"
BIN="delivery"

say()  { printf '%s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- preconditions ----------------------------------------------------------

command -v curl >/dev/null 2>&1 || die "curl is required"

# macOS ships shasum rather than sha256sum. One of the two must exist, because
# installing without verifying is not a fallback this script offers.
if command -v sha256sum >/dev/null 2>&1; then
	sha_cmd="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	sha_cmd="shasum -a 256"
else
	die "neither sha256sum nor shasum found; cannot verify the download"
fi

# --- platform ---------------------------------------------------------------

os=$(uname -s)
case "$os" in
	Linux)  os="linux" ;;
	Darwin) os="darwin" ;;
	*)      die "unsupported operating system: $os (linux and macOS only)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64)  arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*)               die "unsupported architecture: $arch (amd64 and arm64 only)" ;;
esac

asset="$BIN-$os-$arch"

# --- version ----------------------------------------------------------------

version="${DELIVERY_VERSION:-}"
if [ -z "$version" ]; then
	# The /releases/latest URL redirects to the newest tag, which avoids
	# needing a JSON parser just to learn a version number.
	resolved=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
		"https://github.com/$REPO/releases/latest") \
		|| die "could not reach GitHub to resolve the latest release"
	# A repository with no releases redirects /releases/latest to /releases,
	# so the shape of the resolved URL — not the shape of its last segment —
	# is what says whether a release exists at all.
	case "$resolved" in
		*/releases/tag/*)
			version="${resolved##*/}"
			;;
		*/releases | */releases/)
			die "$REPO has no published release yet; set DELIVERY_VERSION to install a specific tag, or build from source"
			;;
		*)
			die "could not resolve the latest release of $REPO (GitHub sent us to $resolved)"
			;;
	esac
fi

base="https://github.com/$REPO/releases/download/$version"

# --- download and verify ----------------------------------------------------

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "delivery $version ($os/$arch)"

curl -fsSL -o "$tmp/$asset" "$base/$asset" \
	|| die "no binary published for $os/$arch at $version"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" \
	|| die "release $version publishes no SHA256SUMS; refusing to install unverified"

# Verify only the line for this asset. Checking the whole manifest would fail
# on the files that were not downloaded.
grep " $asset\$" "$tmp/SHA256SUMS" > "$tmp/expected" \
	|| die "$asset is absent from SHA256SUMS; refusing to install unverified"

( cd "$tmp" && $sha_cmd -c expected >/dev/null 2>&1 ) \
	|| die "checksum mismatch for $asset — the download does not match what was published; nothing installed"

say "checksum verified"

# --- install ----------------------------------------------------------------

dir="${DELIVERY_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir" || die "cannot create $dir"

chmod 0755 "$tmp/$asset"
# Move into place as one step, so an interrupted install never leaves a
# half-written binary where a working one used to be.
mv -f "$tmp/$asset" "$dir/$BIN" || die "cannot write to $dir"

say "installed to $dir/$BIN"

case ":$PATH:" in
	*":$dir:"*) say "run: delivery" ;;
	*)          say ""
	            say "$dir is not on your PATH. Add it:"
	            say "    export PATH=\"\$PATH:$dir\"" ;;
esac
