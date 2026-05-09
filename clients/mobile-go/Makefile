# Build the mobilehop bindings via gomobile.
#
# Phase NN+1: produces an .aar (Android) + .xcframework (iOS) from
# internal/client/ wrapped in clients/mobile-go/mobilehop. The artifacts
# are NOT shipped — they're a build-tripwire that proves the FFI surface
# is genuinely consumable by gomobile. CI runs `make all` on every push;
# local devs only need to run it before touching internal/client/api.go
# or clients/mobile-go/mobilehop/mobilehop.go.
#
# Prereqs:
#   - go install golang.org/x/mobile/cmd/gomobile@latest
#   - gomobile init  (first time, downloads the gomobile shim sources)
#   - For Android: ANDROID_HOME pointing at an Android SDK (level 21+)
#   - For iOS:     full Xcode (Command Line Tools alone is not enough)
#
# Local dev tip: if you only have one platform SDK, run the matching
# half target (`make android-aar` OR `make ios-xcframework`) instead of
# `make all`. CI exercises both in parallel matrix slots.

DIST := dist
PKG  := github.com/trustos/hopssh/clients/mobile-go/mobilehop

.PHONY: all
all: android-aar ios-xcframework

# -androidapi 21: NDK r26+ requires API 21+ (gomobile defaults to 16,
# which the NDK rejects with "unsupported API version 16 (not in 21..34)").
# Android 5.0 Lollipop (API 21) is the de-facto floor for modern Android
# work and matches our other mobile-toolchain defaults.
.PHONY: android-aar
android-aar: | $(DIST)
	gomobile bind -target=android -androidapi 21 -o $(DIST)/mobilehop.aar -javapkg=com.hopssh $(PKG)
	@echo "==> $(DIST)/mobilehop.aar"

.PHONY: ios-xcframework
ios-xcframework: | $(DIST)
	gomobile bind -target=ios -o $(DIST)/MobileHop.xcframework -prefix=Hopssh $(PKG)
	@echo "==> $(DIST)/MobileHop.xcframework"

.PHONY: clean
clean:
	rm -rf $(DIST)

# Sanity check — verifies the package compiles against vendored deps
# without needing platform SDKs. Always safe to run; CI runs this
# alongside the gomobile bind targets so a Go-level breakage in the
# wrapper surfaces fast even if the SDK matrix slot is slow.
.PHONY: verify
verify:
	cd $(CURDIR)/../.. && go build -mod=vendor ./clients/mobile-go/...
	cd $(CURDIR)/../.. && go vet -mod=vendor ./clients/mobile-go/...

$(DIST):
	mkdir -p $(DIST)
