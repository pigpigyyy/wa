cd ~/Workspace/wa

echo "build macOS arm64"
CGO_CFLAGS="-mmacosx-version-min=11.3" CGO_LDFLAGS="-mmacosx-version-min=11.3" CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -buildmode=c-archive -ldflags="-s -w"
mv wa.a wa1.a

echo "build macOS x86_64"
CGO_CFLAGS="-mmacosx-version-min=11.3" CGO_LDFLAGS="-mmacosx-version-min=11.3" CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -buildmode=c-archive -ldflags="-s -w"
mv wa.a wa2.a

lipo -create wa1.a wa2.a -output libwa.a
cp libwa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/macOS/
rm -f wa1.a wa2.a libwa.a wa.h

echo "build iOS"
GOOS=ios GOARCH=arm64 CGO_ENABLED=1 \
CGO_CFLAGS="-isysroot $(xcrun --sdk iphoneos --show-sdk-path) -miphoneos-version-min=13.0" \
CGO_LDFLAGS="-isysroot $(xcrun --sdk iphoneos --show-sdk-path) -miphoneos-version-min=13.0" \
go build -buildmode=c-archive -ldflags="-s -w"
cp wa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/iOS/libwa.a
rm -f wa.a wa.h

echo "build iOS simulator arm64"
GOOS=ios GOARCH=arm64 CGO_ENABLED=1 \
CGO_CFLAGS="-isysroot $(xcrun --sdk iphonesimulator --show-sdk-path) -mios-simulator-version-min=13.0" \
CGO_LDFLAGS="-isysroot $(xcrun --sdk iphonesimulator --show-sdk-path) -mios-simulator-version-min=13.0" \
go build -buildmode=c-archive -ldflags="-s -w"
mv wa.a wa1.a

echo "build iOS simulator x86_64"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 \
CGO_CFLAGS="-isysroot $(xcrun --sdk iphonesimulator --show-sdk-path) -mios-simulator-version-min=13.0" \
CGO_LDFLAGS="-isysroot $(xcrun --sdk iphonesimulator --show-sdk-path) -mios-simulator-version-min=13.0" \
go build -buildmode=c-archive -ldflags="-s -w"
mv wa.a wa2.a
lipo -create wa1.a wa2.a -output libwa.a
cp libwa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/iOS-Simulator/
rm -f wa1.a wa2.a libwa.a wa.h

echo "build Android arm64 arm x86 x86_64"
mv wa.gomobile wa.go
mv main.go main.bak
gomobile bind -v -o wa.aar -target=android .
mv main.bak main.go
mv wa.go wa.gomobile
rm -rf temp
mkdir temp
cd temp
unzip ../wa.aar
rm -rf jni/x86
rm -f ../wa-slim.aar
zip -r ../wa-slim.aar .
cd ..
cp wa-slim.aar ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Android/wa.aar
rm -f wa.aar wa-slim.aar wa-sources.jar
rm -rf temp

#echo "build Linux amd64"
#GOOS=linux GOARCH=amd64 CGO_ENABLED=1 \
#go build -buildmode=c-archive -ldflags="-s -w" -o libwa.a
#cp libwa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Linux/amd64/
#rm -f libwa.a libwa.h

#echo "build Linux arm64"
#GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
#go build -buildmode=c-archive -ldflags="-s -w" -o libwa.a
#cp libwa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Linux/aarch64/
#rm -f libwa.a libwa.h

#echo "build Windows x86"
#GOOS=windows GOARCH=386 CGO_ENABLED=1 \
#CC="zig cc -target x86-windows-msvc" \
#go build -buildmode=c-archive -ldflags="-s -w" -o wa.lib
#cp wa.lib ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Windows/wa.lib


