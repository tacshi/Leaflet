import LeafletCore
import Testing

/// CoreTests checks that the Go core, built by make ios-core, links into
/// the app and answers.
struct CoreTests {
    @Test func coreAnswers() {
        #expect(MobileVersion().hasPrefix("go"))
    }
}
