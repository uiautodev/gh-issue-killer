#!/usr/bin/env bash
# Build the desktop shell and the engine, then assemble a portable bundle.
#
# There is no frontend build step: the Wails window loads the engine's own
# dashboard. The macOS bundle is a .app inside a zip, Linux is a tarball.
# The release workflow runs this once per runner; version comes from the tag.
#
#   desktop/build/package.sh v1.2.3
set -euo pipefail

version="${1:-dev}"
desktop_dir="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$desktop_dir/.." && pwd)"
goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
dist="$desktop_dir/build/dist"
name="gh-issue-killer-desktop_${version}_${goos}_${goarch}"
work="$dist/$name"

if [ "$goos" = "windows" ]; then
  echo "package.sh does not zip Windows bundles; use build/package.ps1" >&2
  exit 1
fi

rm -rf "$work" "$dist/$name.zip" "$dist/$name.tar.gz"
mkdir -p "$work"

echo "building engine ${goos}/${goarch}"
(cd "$root" && go build -trimpath \
  -ldflags "-s -w -X main.version=${version}" \
  -o "$work/gh-issue-killer" ./cmd/gh-issue-killer)

echo "building desktop shell ${goos}/${goarch}"
(cd "$desktop_dir" && go build -trimpath -ldflags "-s -w" \
  -o "$work/gh-issue-killer-desktop" .)

case "$goos" in
darwin)
  app="$work/gh-issue-killer.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  sed "s/__VERSION__/${version#v}/g" "$desktop_dir/build/darwin/Info.plist" >"$app/Contents/Info.plist"
  mv "$work/gh-issue-killer-desktop" "$work/gh-issue-killer" "$app/Contents/MacOS/"
  (cd "$work" && zip -qry "$dist/$name.zip" "gh-issue-killer.app")
  rm -rf "$work"
  ;;
linux)
  tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
  rm -rf "$work"
  ;;
*)
  echo "unsupported GOOS $goos" >&2
  exit 1
  ;;
esac

ls -lh "$dist/$name".*
