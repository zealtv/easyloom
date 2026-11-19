#!/bin/sh
# macOS and Linux: build EasyLoom and link it into Go's executable directory
# (or the supplied one). Keep this script compatible with /bin/sh on both.
set -eu

if [ "$#" -gt 1 ]; then
    echo "Usage: $0 [bin-directory]" >&2
    exit 2
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
go_cmd=${GO:-go}
install_bin=${1:-$("$go_cmd" env GOBIN)}
if [ -z "$install_bin" ]; then
    go_path=$("$go_cmd" env GOPATH)
    install_bin=${go_path%%:*}/bin
fi

mkdir -p "$install_bin"
install_bin=$(CDPATH= cd -- "$install_bin" && pwd)
binary=$repo_root/bin/easyloom
link=$install_bin/easyloom
if [ "$link" = "$binary" ]; then
    echo "Choose an install directory other than the repo's bin directory." >&2
    exit 1
fi
if [ -d "$link" ]; then
    echo "Cannot replace directory: $link" >&2
    exit 1
fi

cd "$repo_root"
version=$(cat VERSION)
CGO_ENABLED=0 "$go_cmd" build -ldflags "-X main.version=v$version" -o "$binary" ./cmd/easyloom
ln -sfn "$binary" "$link"
echo "Installed: $link -> $binary"
case ":$PATH:" in
    *":$install_bin:"*) ;;
    *) echo "Add $install_bin to PATH to run easyloom from any directory." ;;
esac
