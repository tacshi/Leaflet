import Foundation
import Testing
import UserNotifications

@testable import Leaflet

/// RemindersTests check what the system is asked to show of a to-do, and
/// the comparison of a version with its page.
struct RemindersTests {
    private func reminder(at: Int64) -> Reminder {
        Reminder(
            id: "todo:p#b", pageID: "p", blockID: "b", title: "Due today", subtitle: "Plan",
            body: "Send the draft", at: at, due: "2026-03-10")
    }

    @Test func aReminderToComeShowsAtItsTime() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = .gmt
        let at = try #require(
            calendar.date(from: DateComponents(year: 2026, month: 3, day: 10, hour: 9, minute: 30)))
        let r = Reminders.request(
            reminder(at: Int64(at.timeIntervalSince1970 * 1000)), calendar: calendar)
        let trigger = try #require(r.trigger as? UNCalendarNotificationTrigger)
        #expect(!trigger.repeats)
        #expect(trigger.dateComponents.day == 10)
        #expect(trigger.dateComponents.hour == 9)
        #expect(trigger.dateComponents.minute == 30)
        #expect(r.identifier == "todo:p#b")
    }

    /// A to-do already due shows at once, and says what it is, through a
    /// Focus, with what a tap opens.
    @Test func aReminderDueShowsAtOnce() {
        let r = Reminders.request(reminder(at: 0))
        #expect(r.trigger == nil)
        #expect(r.content.title == "Due today")
        #expect(r.content.subtitle == "Plan")
        #expect(r.content.body == "Send the draft")
        #expect(r.content.interruptionLevel == .timeSensitive)
        #expect(r.content.userInfo["page"] as? String == "p")
        #expect(r.content.userInfo["block"] as? String == "b")
    }

    /// The system hears a tap is handled on the main thread, wherever it
    /// came from: UIKit aborts the app otherwise.
    @Test func aTapIsHandledOnTheMainThread() async {
        let onMain = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
            DispatchQueue.global().async {
                ReminderDelegate.take(nil) { done.resume(returning: Thread.isMainThread) }
            }
        }
        #expect(onMain)
    }

    @Test func nothingChangedHasNoRows() {
        #expect(VersionDiff.rows(from: "a\nb", to: "a\nb").isEmpty)
    }

    /// What went and what came show in place, with the lines around them.
    @Test func changesShowWithTheirContext() {
        let rows = VersionDiff.rows(
            from: "1\n2\n3\n4\n5\n6\n7", to: "1\n2\n3\nfour\n5\n6\n7", context: 1)
        #expect(rows.map(\.kind) == [.gap, .same, .removed, .added, .same, .gap])
        #expect(rows.map(\.text) == ["⋯", "3", "4", "four", "5", "⋯"])
    }

    @Test func linesAddedAtTheEndShow() {
        let rows = VersionDiff.rows(from: "a", to: "a\nb")
        #expect(rows.map(\.kind) == [.same, .added])
    }

    @Test func linesRemovedAtTheStartShow() {
        let rows = VersionDiff.rows(from: "a\nb", to: "b")
        #expect(rows.map(\.kind) == [.removed, .same])
    }

    @Test func anEmptyPageComparesWithAVersion() {
        #expect(VersionDiff.rows(from: "", to: "a").map(\.kind) == [.removed, .added])
    }
}
