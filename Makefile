
export CGO_ENABLED := 0

BUILD_DIR ?= build
BINARIES := leaflet leaflet.exe

.PHONY: check clean ios-check ios-core ios-project ios-packages ios-test

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

ios-check: ios-project
	@for tool in swiftlint osv-scanner mobsfscan semgrep; do \
		command -v $$tool >/dev/null || { echo "ios-check needs $$tool on PATH" >&2; exit 1; }; \
	done
	cd ios && find Leaflet Shared Widgets LeafletTests ../speech -name '*.swift' -not -path '*/Generated/*' \
		-not -path '../speech/.build/*' -print0 | \
		xargs -0 xcrun swift-format lint --strict --configuration .swift-format
	cd ios && swiftlint lint --strict --quiet
	xcodebuild -resolvePackageDependencies -quiet -onlyUsePackageVersionsFromResolvedFile \
		-project ios/Leaflet.xcodeproj -scheme Leaflet
	osv-scanner scan source -L ios/Package.resolved
	cd ios && mobsfscan -w -c .mobsf Leaflet Shared Widgets LeafletTests ../speech

clean:
	@case "$(strip $(BUILD_DIR))" in \
		"" | / | . | ..) echo 'BUILD_DIR must name a build directory.' >&2; exit 1 ;; \
	esac
	rm -rf -- "$(BUILD_DIR)"
	rm -f -- $(BINARIES)

ios-core:
	ios/scripts/build-core.sh

IOS_RESOLVED := ios/Leaflet.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved

ios-project:
	@command -v xcodegen >/dev/null || { echo "ios-project needs xcodegen on PATH" >&2; exit 1; }
	go run ./ios/gen
	xcodegen generate --spec ios/project.yml
	mkdir -p $(dir $(IOS_RESOLVED))
	cp ios/Package.resolved $(IOS_RESOLVED)

ios-packages: ios-project
	rm -f $(IOS_RESOLVED)
	xcodebuild -resolvePackageDependencies -quiet -project ios/Leaflet.xcodeproj -scheme Leaflet
	cp $(IOS_RESOLVED) ios/Package.resolved

ios-test: ios-core ios-project
	xcodebuild test -quiet -onlyUsePackageVersionsFromResolvedFile \
		-project ios/Leaflet.xcodeproj -scheme Leaflet \
		-destination 'platform=iOS Simulator,name=$(IOS_IPHONE),OS=latest' \
		-destination 'platform=iOS Simulator,name=$(IOS_IPAD),OS=latest'
