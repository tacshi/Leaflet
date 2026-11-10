import LeafletCore
import Testing

struct CoreTests {
    @Test func coreAnswers() {
        #expect(MobileVersion().hasPrefix("go"))
    }
}
