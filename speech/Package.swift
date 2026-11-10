// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "LeafletSpeech",
    platforms: [.macOS(.v13), .iOS(.v26)],
    products: [
        .library(name: "LeafletSpeech", targets: ["LeafletSpeech"]),
        .executable(name: "leaflet-speech", targets: ["leaflet-speech"]),
    ],
    targets: [
        .target(name: "LeafletSpeech"),
        .executableTarget(name: "leaflet-speech", dependencies: ["LeafletSpeech"]),
    ]
)
