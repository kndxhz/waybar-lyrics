# Maintainer: kndxhz <kndxhz@163.com>
pkgname=waybar-lyrics-bin
pkgver=1.6.0
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
sha256sums_x86_64=('8454a1e1da0c2a5fcf4d9b443aa0ab5e7253c3eb4aa80cec7dfd05e861c46b63')
sha256sums_aarch64=('8f3cd05a8b11651126c0650e5215b2929f4828f4175e67efdbddd54a228bf37a')

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
