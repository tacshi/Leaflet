// swift-tools-version: 6.2

// LeafletSpeech transcribes recordings with the device's own speech
// recognition: the iOS app's on-device engine (ios/project.yml), and the
// Mac app's, through leaflet-speech, the helper it ships, which
// scripts/build-speech.sh builds. Both answer the core alike
// (recording.OnDeviceAnswer), which says why one could not transcribe in
// the user's language.

import PackageDescription

let package = Package(
    name: "LeafletSpeech",
    // The helper runs on the Mac app's oldest macOS, and says there that
    // it can't transcribe: SpeechTranscriber is macOS 26's.
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
