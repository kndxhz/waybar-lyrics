# Maintainer: kndxhz <kndxhz@163.com>
pkgname=waybar-lyrics-git
pkgver=r45.1ba403c
pkgrel=1
pkgdesc="Waybar synchronized lyrics from MPRIS, SPlayer, and SPlayer-Next"
arch=('x86_64')
url="https://github.com/kndxhz/waybar-lyrics"
license=('GPL3')
depends=('glibc')
makedepends=('go' 'git')
optdepends=('playerctl: For media control support')
provides=('waybar-lyrics')
conflicts=('waybar-lyrics')
source=("${pkgname%-git}::git+${url}.git")
sha256sums=('SKIP')

pkgver() {
    cd "$srcdir/${pkgname%-git}"
    printf "r%s.%s" "$(git rev-list --count HEAD)" "$(git rev-parse --short=7 HEAD)"
}

build() {
    cd "$srcdir/${pkgname%-git}"
    export CGO_CPPFLAGS="${CPPFLAGS}"
    export CGO_CFLAGS="${CFLAGS}"
    export CGO_CXXFLAGS="${CXXFLAGS}"
    export CGO_LDFLAGS="${LDFLAGS}"
    export GOFLAGS="-buildmode=pie -trimpath -ldflags=-linkmode=external -mod=readonly -modcacherw"

    go build -o waybar-lyrics .
}

check() {
    cd "$srcdir/${pkgname%-git}"
    export GOFLAGS="-mod=readonly -modcacherw"
    go test ./...
}

package() {
    cd "$srcdir/${pkgname%-git}"
    install -Dm755 waybar-lyrics "$pkgdir/usr/bin/waybar-lyrics"
    install -Dm644 README.md "$pkgdir/usr/share/doc/$pkgname/README.md"
    if [ -f LICENSE ]; then
        install -Dm644 LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
    fi
}
