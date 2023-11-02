#!/bin/bash

if [[ ! $GITHUB_REF =~ ^"refs/tags/v" ]]
then
    echo >&2 "This is not commit tagged for release."
    exit 0
fi

VERSION=${GITHUB_REF#refs/tags/v}

echo "Version $VERSION"

function build() {
    echo "Building $1"
    goos=${1%/*}
    goarch=${1#*/}
    mkdir -p bin
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build \
    -o bin/reg-$goos-$goarch \
    --ldflags '-extldflags "-static"' \
    --ldflags="-X 'goreg/cmd.Version=$VERSION'" \
    main.go
}

rm -rf bin

# osarchs=$(go tool dist list | grep -E "^(linux|darwin|windows)\\/")
osarchs="darwin/amd64 darwin/arm64 linux/amd64 linux/arm linux/arm64 windows/amd64 windows/arm windows/arm64"
for osarch in $osarchs
do
    build $osarch
done

go install github.com/tcnksm/ghr@latest

if [ $DRY_RUN ]
then
    echo "Dry run, not publishing."
    exit 0
fi
~/go/bin/ghr -u ${GITHUB_REPOSITORY%/*} -r ${GITHUB_REPOSITORY#*/} v${VERSION} bin
