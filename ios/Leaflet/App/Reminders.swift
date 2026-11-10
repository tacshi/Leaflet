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
}

enum Reminders {
    static let limit = 60

    static func request(_ r: Reminder, calendar: Calendar = .current) -> UNNotificationRequest {
        let content = UNMutableNotificationContent()
        content.title = r.title
        content.subtitle = r.subtitle
        content.body = r.body
        content.sound = .default
        content.threadIdentifier = "todos"
        content.interruptionLevel = .timeSensitive
        content.userInfo = ["page": r.pageID, "block": r.blockID]
        var trigger: UNCalendarNotificationTrigger?
        if r.at > 0 {
            let date = Date(timeIntervalSince1970: TimeInterval(r.at) / 1000)
            let parts = calendar.dateComponents(
                [.year, .month, .day, .hour, .minute, .second], from: date)
            trigger = UNCalendarNotificationTrigger(dateMatching: parts, repeats: false)
        }
        return UNNotificationRequest(identifier: r.id, content: content, trigger: trigger)
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
        center.removeAllPendingNotificationRequests()
        guard !list.isEmpty, await allowed(center, asking: asking) else { return }
        var scheduled: [Reminder] = []
        for reminder in list {
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
