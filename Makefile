# Check, cleanup and the iOS app's builds; scripts/ builds and publishes the
# desktop app.
#
#   make check		# formatting, lint, known vulnerabilities, security
#   make ios-check	# the same for the iOS app's Swift
#   make clean		# remove what builds leave: build/ and the binary
#   make ios-core		# the Go core for iOS: ios/build/LeafletCore.xcframework
#   make ios-project	# ios/Leaflet.xcodeproj, from ios/project.yml
#   make ios-packages	# ios/Package.resolved, after a package's version changes
#   make ios-test		# the iOS app's tests, on an iPhone and an iPad simulator

# Everything builds without cgo, as MyGo requires; only gomobile, for the
# iOS core, turns it on (ios/scripts/build-core.sh).
export CGO_ENABLED := 0

# Where scripts/ and go tool mygo build put the apps, installers and
# updates; and the binary go build . leaves here.
BUILD_DIR ?= build
BINARIES := leaflet leaflet.exe

.PHONY: check clean ios-check ios-core ios-project ios-packages ios-test

# The simulators make ios-test runs on, by name, as xcrun simctl list
# devices shows them.
IOS_IPHONE ?= iPhone 17
IOS_IPAD ?= iPad Pro 13-inch (M5)

check:
	@for tool in golangci-lint govulncheck gosec; do \
		command -v $$tool >/dev/null || { echo "check needs $$tool on PATH" >&2; exit 1; }; \
	done
	@unformatted=$$(gofmt -l .); \
		if [ -n "$$unformatted" ]; then echo "gofmt would change:" >&2; echo "$$unformatted" >&2; exit 1; fi
	golangci-lint run --no-config ./...
	govulncheck ./...
	gosec -quiet -tests -exclude-generated ./...

# The Swift counterparts of check's tools: swift-format for gofmt, SwiftLint
# for golangci-lint, osv-scanner on the Swift packages ios/Package.resolved
# pins for govulncheck, and mobsfscan, which runs its rules with semgrep,
# for gosec. ios/ holds their settings, for speech/ too, the package the
# app shares with the Mac's speech helper; what ios/gen writes is left out.
# The packages resolve only to the versions pinned, failing otherwise.
ios-check: ios-project
	@for tool in swiftlint osv-scanner mobsfscan semgrep; do \
		command -v $$tool >/dev/null || { echo "ios-check needs $$tool on PATH" >&2; exit 1; }; \
	done
	cd ios && find Leaflet LeafletTests ../speech -name '*.swift' -not -path '*/Generated/*' \
		-not -path '../speech/.build/*' -print0 | \
		xargs -0 xcrun swift-format lint --strict --configuration .swift-format
	cd ios && swiftlint lint --strict --quiet
	xcodebuild -resolvePackageDependencies -quiet -onlyUsePackageVersionsFromResolvedFile \
		-project ios/Leaflet.xcodeproj -scheme Leaflet
	osv-scanner scan source -L ios/Package.resolved
	cd ios && mobsfscan -w -c .mobsf Leaflet LeafletTests ../speech

clean:
	@case "$(strip $(BUILD_DIR))" in \
		"" | / | . | ..) echo 'BUILD_DIR must name a build directory.' >&2; exit 1 ;; \
	esac
	rm -rf -- "$(BUILD_DIR)"
	rm -f -- $(BINARIES)

ios-core:
	ios/scripts/build-core.sh

# Where Xcode reads the packages' resolution, in the project git ignores.
IOS_RESOLVED := ios/Leaflet.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved

# The project, with the packages' resolution git keeps put in it.
ios-project:
	@command -v xcodegen >/dev/null || { echo "ios-project needs xcodegen on PATH" >&2; exit 1; }
	go run ./ios/gen
	xcodegen generate --spec ios/project.yml
	mkdir -p $(dir $(IOS_RESOLVED))
	cp ios/Package.resolved $(IOS_RESOLVED)

# Resolves the packages again, to the versions ios/project.yml pins, and
# keeps the resolution in ios/Package.resolved, for git.
ios-packages: ios-project
	rm -f $(IOS_RESOLVED)
	xcodebuild -resolvePackageDependencies -quiet -project ios/Leaflet.xcodeproj -scheme Leaflet
	cp $(IOS_RESOLVED) ios/Package.resolved

ios-test: ios-core ios-project
	xcodebuild test -quiet -onlyUsePackageVersionsFromResolvedFile \
		-project ios/Leaflet.xcodeproj -scheme Leaflet \
		-destination 'platform=iOS Simulator,name=$(IOS_IPHONE),OS=latest' \
		-destination 'platform=iOS Simulator,name=$(IOS_IPAD),OS=latest'
