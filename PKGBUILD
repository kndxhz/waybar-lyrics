# Maintainer: kndxhz <kndxhz@163.com>
pkgname=waybar-lyrics
pkgver=1.5.11
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
sha256sums=('406d4b6fe9998954380096c4087d200f80b1a054a5780dfeccdb8e35b17584b4')

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
