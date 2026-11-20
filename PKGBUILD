# Maintainer: kndxhz <kndxhz@163.com>
pkgname=waybar-lyrics-bin
pkgver=1.5.13
pkgrel=1
pkgdesc="Waybar synchronized lyrics from MPRIS, SPlayer, and SPlayer-Next (prebuilt binary)"
arch=('x86_64' 'aarch64')
url="https://github.com/kndxhz/waybar-lyrics"
license=('GPL3')
depends=('glibc')
optdepends=('playerctl: For media control support')
provides=('waybar-lyrics')
conflicts=('waybar-lyrics' 'waybar-lyrics-git')
options=('!strip' '!debug')

source_x86_64=("${pkgname}-${pkgver}-amd64::${url}/releases/download/v${pkgver}/waybar-lyrics-linux-amd64")
source_aarch64=("${pkgname}-${pkgver}-arm64::${url}/releases/download/v${pkgver}/waybar-lyrics-linux-arm64")
sha256sums_x86_64=('c0d2e5536cbd1dc2dd9937229ac1219ccd7d1dc4efff6d6f008b410a3bd852f4')
sha256sums_aarch64=('1d5d30241157628c0df027cab27974559697436d4fa6248df408d86d8a3427c7')

package() {
  local _arch_suffix
  case "$CARCH" in
  x86_64) _arch_suffix="amd64" ;;
  aarch64) _arch_suffix="arm64" ;;
  *)
    error "Unsupported architecture: $CARCH"
    return 1
    ;;
  esac

  local _binary_name="${pkgname}-${pkgver}-${_arch_suffix}"
  install -Dm755 "$srcdir/$_binary_name" "$pkgdir/usr/bin/waybar-lyrics"
}
