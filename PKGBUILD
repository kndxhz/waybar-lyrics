# Maintainer: kndxhz <kndxhz@163.com>
pkgname=waybar-lyrics
pkgver=1.5.6
pkgrel=1
pkgdesc="Waybar synchronized lyrics from MPRIS, SPlayer, and SPlayer-Next"
arch=('x86_64')
url="https://github.com/kndxhz/waybar-lyrics"
license=('GPL3')
depends=('glibc')
makedepends=('go')
optdepends=('playerctl: For media control support')
conflicts=('waybar-lyrics-git')
source=("$pkgname-$pkgver.tar.gz::$url/archive/v$pkgver.tar.gz")
sha256sums=('92e6a70f6740c29d3b64d77f0962014e29306d76c749662bd4f486a7317eca9c')

build() {
    cd "$srcdir/$pkgname-$pkgver"
    export CGO_CPPFLAGS="${CPPFLAGS}"
    export CGO_CFLAGS="${CFLAGS}"
    export CGO_CXXFLAGS="${CXXFLAGS}"
    export CGO_LDFLAGS="${LDFLAGS}"
    export GOFLAGS="-buildmode=pie -trimpath -ldflags=-linkmode=external -mod=readonly -modcacherw"

    go build -o waybar-lyrics .
}

check() {
    cd "$srcdir/$pkgname-$pkgver"
    export GOFLAGS="-mod=readonly -modcacherw"
    go test ./...
}

package() {
    cd "$srcdir/$pkgname-$pkgver"
    install -Dm755 waybar-lyrics "$pkgdir/usr/bin/waybar-lyrics"
    install -Dm644 README.md "$pkgdir/usr/share/doc/$pkgname/README.md"
    if [ -f LICENSE ]; then
        install -Dm644 LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
    fi
}
