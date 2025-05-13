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

echo "build Android arm64"
GOOS=android GOARCH=arm64 CGO_ENABLED=1 \
CC="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$(uname -s | tr '[:upper:]' '[:lower:]')-x86_64/bin/aarch64-linux-android28-clang" \
go build -buildmode=c-shared -ldflags="-s -w" -o wa.so
cp wa.so ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Android/arm64-v8a/libwa.so
rm -f wa.so wa.h

echo "build Android arm"
GOOS=android GOARCH=arm CGO_ENABLED=1 \
CC="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$(uname -s | tr '[:upper:]' '[:lower:]')-x86_64/bin/armv7a-linux-androideabi28-clang" \
go build -buildmode=c-shared -ldflags="-s -w" -o wa.so
cp wa.so ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Android/armeabi-v7a/libwa.so
rm -f wa.so wa.h

echo "build Android x86_64"
GOOS=android GOARCH=amd64 CGO_ENABLED=1 \
CC="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$(uname -s | tr '[:upper:]' '[:lower:]')-x86_64/bin/x86_64-linux-android28-clang" \
go build -buildmode=c-shared -ldflags="-s -w" -o wa.so
cp wa.so ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Android/x86_64/libwa.so
rm -f wa.so wa.h

echo "build Linux amd64"
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 \
CC="zig cc -target x86_64-linux-gnu" \
go build -buildmode=c-archive -ldflags="-s -w" -o wa.a
cp wa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Linux/amd64/libwa.a
rm -f wa.a wa.h

echo "build Linux arm64"
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
CC="zig cc -target aarch64-linux-gnu" \
go build -buildmode=c-archive -ldflags="-s -w" -o wa.a
cp wa.a ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Linux/aarch64/libwa.a
rm -f wa.a wa.h

echo "build Windows x86_64"
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
CC="zig cc -target x86_64-windows-gnu" \
go build -buildmode=c-archive -ldflags="-s -w" -o wa.lib
cp wa.lib ~/Workspace/Dora-SSR/Source/3rdParty/Wa/Lib/Windows/wa.lib

