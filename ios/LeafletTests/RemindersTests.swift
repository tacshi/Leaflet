import Foundation
import Testing
import UserNotifications

@testable import Leaflet

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
            reminder(at: Int64(at.timeIntervalSince1970 * 1000)),
            now: at.addingTimeInterval(-60), calendar: calendar)
        let trigger = try #require(r.trigger as? UNCalendarNotificationTrigger)
        #expect(!trigger.repeats)
        #expect(trigger.dateComponents.day == 10)
        #expect(trigger.dateComponents.hour == 9)
        #expect(trigger.dateComponents.minute == 30)
        #expect(r.identifier == "todo:p#b")
    }

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

    @Test func aReminderWhoseTimeHasComeShowsAtOnce() {
        let at = Date(timeIntervalSince1970: 1_773_135_000)
        let r = reminder(at: 1_773_135_000_000)
        #expect(Reminders.request(r, now: at).trigger == nil)
        #expect(Reminders.request(r, now: at.addingTimeInterval(5)).trigger == nil)
        #expect(Reminders.request(r, now: at.addingTimeInterval(-1)).trigger != nil)
    }

    @Test func aDayDueIsRemindedAtNineInTheZoneTheDeviceIsIn() async throws {
        let core = try ScratchCore("RemindersTests")
        let client = core.client
        let page = try await client.createPage(NewPage(title: "Trip"))
        var opened = try await client.openPage(page.id)
        opened.content.document.children = [
            DocNode(
                id: "pack", type: "todo_list",
                data: [
                    "checked": .bool(false), "due": .string("2026-12-02"),
                    "delta": .array([.object(["insert": .string("Pack")])]),
                ], children: nil)
        ]
        try await client.editPage(
            page.id, editor: opened.editor, content: opened.content, rev: opened.rev)
        await client.closePage(page.id, editor: opened.editor)
        let berlin = try #require(TimeZone(identifier: "Europe/Berlin"))
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = berlin
        let now = try #require(
            calendar.date(from: DateComponents(year: 2026, month: 12, day: 1, hour: 12)))
        let list = try await client.reminders(now: now, limit: 10, zone: berlin)
        let date = try #require(list.first { $0.blockID == "pack" }?.date)
        let at = calendar.dateComponents([.month, .day, .hour, .minute], from: date)
        #expect(at.month == 12 && at.day == 2 && at.hour == 9 && at.minute == 0)
    }

    @Test func onlyRemindersNoLongerListedAreTakenBack() {
        let stale = Reminders.stale(["todo:p#b", "todo:p#gone"], keeping: [reminder(at: 0)])
        #expect(stale == ["todo:p#gone"])
        #expect(Reminders.stale(["todo:p#b"], keeping: []) == ["todo:p#b"])
    }

    @Test func aReminderThatShowedByItselfIsNotShownAgain() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = .gmt
        let at = Date(timeIntervalSince1970: 1_773_135_000)
        let r = reminder(at: 1_773_135_000_000)
        let own = calendar.dateComponents(
            [.year, .month, .day, .hour, .minute, .second], from: at)
        let other = calendar.dateComponents(
            [.year, .month, .day, .hour, .minute, .second], from: at.addingTimeInterval(-3600))
        let after = at.addingTimeInterval(1)
        #expect(Reminders.shown(r, at: [r.id: own], now: after, calendar: calendar))
        let before = at.addingTimeInterval(-1)
        #expect(!Reminders.shown(r, at: [r.id: own], now: before, calendar: calendar))
        #expect(!Reminders.shown(r, at: [r.id: other], now: after, calendar: calendar))
        #expect(!Reminders.shown(r, at: [:], now: after, calendar: calendar))
        #expect(!Reminders.shown(reminder(at: 0), at: [r.id: own], now: after, calendar: calendar))
    }

    @Test func remindersGoWithTheAccountThatLeft() {
        #expect(Reminders.left("ann", for: ""))
        #expect(Reminders.left("ann", for: "bob"))
        #expect(!Reminders.left("ann", for: "ann"))
        #expect(!Reminders.left("", for: "ann"))
        #expect(!Reminders.left(nil, for: "ann"))
    }

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
