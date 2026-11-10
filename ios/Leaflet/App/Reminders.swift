import Foundation
import Observation
import UserNotifications
import os

struct Reminder: Codable, Hashable, Sendable {
    var id: String
    var pageID: String
    var blockID: String
    var title: String
    var subtitle: String
    var body: String
    var at: Int64
    var due: String

    enum CodingKeys: String, CodingKey {
        case id, title, subtitle, body, at, due
        case pageID = "page_id"
        case blockID = "block_id"
    }

    /// When it shows, or nil for one that is due.
    var date: Date? { at > 0 ? Date(timeIntervalSince1970: TimeInterval(at) / 1000) : nil }
}

enum Reminders {
    static let limit = 60

    /// A reminder whose time has come by now shows at once: a trigger in the past never fires.
    static func request(_ r: Reminder, now: Date = .now, calendar: Calendar = .current)
        -> UNNotificationRequest
    {
        let content = UNMutableNotificationContent()
        content.title = r.title
        content.subtitle = r.subtitle
        content.body = r.body
        content.sound = .default
        content.threadIdentifier = "todos"
        content.interruptionLevel = .timeSensitive
        content.userInfo = ["page": r.pageID, "block": r.blockID]
        var trigger: UNCalendarNotificationTrigger?
        if let date = r.date, date > now {
            trigger = UNCalendarNotificationTrigger(
                dateMatching: parts(of: date, calendar: calendar), repeats: false)
        }
        return UNNotificationRequest(identifier: r.id, content: content, trigger: trigger)
    }

    private static func parts(of date: Date, calendar: Calendar) -> DateComponents {
        calendar.dateComponents([.year, .month, .day, .hour, .minute, .second], from: date)
    }

    /// The pending requests, of these identifiers, that the list no longer holds.
    static func stale(_ pending: [String], keeping list: [Reminder]) -> [String] {
        let kept = Set(list.map(\.id))
        return pending.filter { !kept.contains($0) }
    }

    /// Whether a reminder read as to come has shown by itself since: its time has passed, and a
    /// request of its own was set for that very time.
    static func shown(
        _ r: Reminder, at times: [String: DateComponents], now: Date = .now,
        calendar: Calendar = .current
    ) -> Bool {
        guard let date = r.date, date <= now, let was = times[r.id] else { return false }
        let at = parts(of: date, calendar: calendar)
        return [Calendar.Component.year, .month, .day, .hour, .minute, .second].allSatisfy {
            was.value(for: $0) == at.value(for: $0)
        }
    }

    /// Whether the reminders delivered to this device belong to an account it has left.
    static func left(_ before: String?, for user: String) -> Bool {
        guard let before, !before.isEmpty else { return false }
        return before != user
    }

    private static let userKey = "reminders.user"

    /// Takes the reminders of an account off the device once another, or none, is signed in.
    @MainActor
    static func keep(to user: String, defaults: UserDefaults = .standard) {
        let before = defaults.string(forKey: userKey)
        if before != user { defaults.set(user, forKey: userKey) }
        guard left(before, for: user) else { return }
        let center = UNUserNotificationCenter.current()
        center.removeAllPendingNotificationRequests()
        center.removeAllDeliveredNotifications()
    }

    @MainActor
    static func schedule(_ client: CoreClient, asking: Bool) async {
        if let running {
            again = Again(client: client, asking: asking || again?.asking == true)
            await running.value
            return
        }
        let run = Task { @MainActor in
            var next: Again? = Again(client: client, asking: asking)
            while let this = next {
                await scheduleOnce(this.client, asking: this.asking)
                next = again
                again = nil
            }
            running = nil
        }
        running = run
        await run.value
    }

    private struct Again {
        let client: CoreClient
        let asking: Bool
    }

    @MainActor private static var running: Task<Void, Never>?
    @MainActor private static var again: Again?

    @MainActor
    private static func scheduleOnce(_ client: CoreClient, asking: Bool) async {
        let center = UNUserNotificationCenter.current()
        let list: [Reminder]
        do {
            list = try await client.reminders(now: .now, limit: limit)
        } catch {
            Logger.app.error("Could not read the reminders: \(error)")
            return
        }
        let pending = await center.pendingNotificationRequests().map(\.identifier)
        center.removePendingNotificationRequests(withIdentifiers: stale(pending, keeping: list))
        guard !list.isEmpty, await allowed(center, asking: asking) else { return }
        var times: [String: DateComponents] = [:]
        let delivered = await center.deliveredNotifications().map(\.request)
        for request in await center.pendingNotificationRequests() + delivered {
            guard let trigger = request.trigger as? UNCalendarNotificationTrigger else { continue }
            times[request.identifier] = trigger.dateComponents
        }
        var scheduled: [Reminder] = []
        for reminder in list {
            if shown(reminder, at: times) {
                scheduled.append(reminder)
                continue
            }
            do {
                try await center.add(request(reminder))
                scheduled.append(reminder)
            } catch {
                Logger.app.error("Could not schedule a reminder: \(error)")
            }
        }
        do {
            try await client.remindedOf(scheduled)
        } catch {
            Logger.app.error("Could not keep the reminders scheduled: \(error)")
        }
    }

    @MainActor
    private static func allowed(_ center: UNUserNotificationCenter, asking: Bool) async -> Bool {
        switch await center.notificationSettings().authorizationStatus {
        case .authorized, .provisional, .ephemeral:
            return true
        case .notDetermined:
            guard asking else { return false }
            return (try? await center.requestAuthorization(options: [.alert, .sound])) ?? false
        case .denied:
            return false
        @unknown default:
            return false
        }
    }
}

struct ReminderTarget: Equatable, Sendable {
    let pageID: String
    let blockID: String
}

@MainActor @Observable
final class ReminderInbox {
    static let shared = ReminderInbox()
    var target: ReminderTarget?
}

final class ReminderDelegate: NSObject, UNUserNotificationCenterDelegate, Sendable {
    static let shared = ReminderDelegate()

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification,
        withCompletionHandler done: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        done([.banner, .list, .sound])
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        var target: ReminderTarget?
        let info = response.notification.request.content.userInfo
        if response.actionIdentifier == UNNotificationDefaultActionIdentifier,
            let page = info["page"] as? String, let block = info["block"] as? String
        {
            target = ReminderTarget(pageID: page, blockID: block)
        }
        Self.take(target, then: completionHandler)
    }

    nonisolated static func take(_ target: ReminderTarget?, then done: @escaping () -> Void) {
        nonisolated(unsafe) let done = done
        Task { @MainActor in
            if let target { ReminderInbox.shared.target = target }
            done()
        }
    }
}
