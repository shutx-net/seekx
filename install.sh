#!/bin/sh
# seekx のインストーラ。GitHub Release のバイナリを落として配置する。
#
#   curl -fsSL https://raw.githubusercontent.com/shutx-net/seekx/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/shutx-net/seekx/main/install.sh | sh -s -- --dir /usr/local/bin
#   curl -fsSL https://raw.githubusercontent.com/shutx-net/seekx/main/install.sh | sh -s -- --version v0.1.0
#
# 【全体を関数に入れ、最終行でだけ main を呼ぶ構成にしてある。】
# curl | sh はダウンロードしながら実行するため、通信が途中で切れると
# 尻切れのスクリプトがそのまま動く。この形なら切れた場合は関数定義が
# 読まれるだけで何も実行されない。構成を崩さないこと。
#
# POSIX sh で書いてある（bash 依存の構文を入れないこと）。

set -eu

REPO=shutx-net/seekx
BIN=seekx

die() {
	echo "エラー: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 が必要です。"
}

usage() {
	cat <<'USAGE'
seekx インストーラ

  --version <tag>   入れるバージョン（既定: 最新リリース）
  --dir <path>      配置先（既定: $HOME/.local/bin）
  --help

環境変数 SEEKX_VERSION / SEEKX_INSTALL_DIR でも同じ指定ができます。
Windows は README の PowerShell 手順を使ってください。
USAGE
}

detect_os() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	case "$os" in
	linux | darwin) echo "$os" ;;
	*) die "このスクリプトは linux と darwin だけに対応しています（uname -s = $(uname -s)）。Windows は README の PowerShell 手順を使ってください。" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) die "未対応のアーキテクチャです（uname -m = $arch）。配布しているのは amd64 と arm64 だけです。" ;;
	esac
}

# sha256sum は Linux、shasum は macOS。どちらも無ければ検証できないので中断する
# （検証を飛ばして続けるほうが危険なので、既定で通す抜け道は作らない）。
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "sha256sum も shasum も見つかりません。checksum を検証できないので中断します。"
	fi
}

# /releases/latest は最新タグのページへリダイレクトする。この形なら GitHub API を
# 使わないので、未認証のレート制限（60 回/時・IP 単位）に当たらない。
# タグを先に確定させてから明示的な URL で落とすので、取得中に新しいリリースが
# 出てもアーカイブと checksums.txt が食い違わない。
resolve_tag() {
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest") ||
		die "最新バージョンを解決できませんでした（リポジトリが private の場合は落とせません）。"
	tag=${url##*/}
	case "$tag" in
	v*) echo "$tag" ;;
	*) die "最新バージョンの解決に失敗しました（得られた URL: $url）。--version でタグを直接指定してください。" ;;
	esac
}

main() {
	version=${SEEKX_VERSION:-}
	dir=${SEEKX_INSTALL_DIR:-$HOME/.local/bin}

	while [ $# -gt 0 ]; do
		case "$1" in
		-v | --version)
			[ $# -ge 2 ] || die "--version にタグを指定してください。"
			version=$2
			shift 2
			;;
		-d | --dir)
			[ $# -ge 2 ] || die "--dir にディレクトリを指定してください。"
			dir=$2
			shift 2
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "不明な引数: $1（--help を見てください）" ;;
		esac
	done

	need curl
	need tar

	os=$(detect_os)
	arch=$(detect_arch)
	[ -n "$version" ] || version=$(resolve_tag)

	asset="${BIN}_${version}_${os}_${arch}.tar.gz"
	base="https://github.com/${REPO}/releases/download/${version}"

	tmp=$(mktemp -d) || die "作業ディレクトリを作れませんでした。"
	trap 'rm -rf "$tmp"' EXIT INT TERM

	echo "seekx ${version} (${os}/${arch}) を取得します"
	curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}" ||
		die "ダウンロードに失敗しました: ${base}/${asset}"
	curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt" ||
		die "checksums.txt を取得できませんでした: ${base}/checksums.txt"

	# ファイル名にドットが入るので grep の正規表現ではなく awk の完全一致で引く。
	want=$(awk -v a="$asset" '$2 == a { print $1 }' "${tmp}/checksums.txt")
	[ -n "$want" ] || die "checksums.txt に ${asset} の行がありません。"
	got=$(sha256_of "${tmp}/${asset}")
	[ "$want" = "$got" ] || die "checksum が一致しません（期待 ${want} / 実際 ${got}）。"
	echo "checksum を検証しました"

	tar -xzf "${tmp}/${asset}" -C "$tmp" || die "展開に失敗しました。"
	[ -f "${tmp}/${BIN}" ] || die "アーカイブに ${BIN} が入っていません。"

	mkdir -p "$dir" || die "${dir} を作れませんでした。"
	chmod 0755 "${tmp}/${BIN}"
	# 実行中のバイナリを cp で上書きすると Text file busy になる。mv なら inode が
	# 差し替わるだけなので、起動中の seekx があっても失敗しない。
	mv -f "${tmp}/${BIN}" "${dir}/${BIN}" ||
		die "${dir} に書き込めませんでした。別の場所へ入れるなら --dir を使ってください。"

	echo "インストールしました: ${dir}/${BIN}"
	case ":${PATH}:" in
	*":${dir}:"*) : ;;
	*)
		echo "注意: ${dir} は PATH に入っていません。シェルの設定に次を足してください:"
		echo "  export PATH=\"${dir}:\$PATH\""
		;;
	esac
	"${dir}/${BIN}" --version
}

main "$@"
