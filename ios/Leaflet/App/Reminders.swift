import Foundation
import Observation
import UserNotifications
import os

/// Reminder is a to-do's notification, as the core says it
/// (mobile.Reminders).
struct Reminder: Codable, Hashable, Sendable {
    var id: String
    var pageID: String
    var blockID: String
    var title: String
    var subtitle: String
    var body: String
    /// at is when it shows, in milliseconds, or 0 for now.
    var at: Int64
    /// due is the date it reminds of, kept once it is scheduled.
    var due: String

    enum CodingKeys: String, CodingKey {
        case id, title, subtitle, body, at, due
        case pageID = "page_id"
        case blockID = "block_id"
    }
}

/// Reminders schedules a to-do's notification with the system, which shows
/// it whether or not the app is running. The desktop looks each minute for
/// what is due; iOS doesn't keep an app running to, so what is to come is
/// scheduled ahead, again each time the to-dos may have changed.
enum Reminders {
    /// limit is how many stay scheduled: iOS keeps the 64 soonest of an
    /// app's, and the rest are scheduled as those go by.
    static let limit = 60

    /// request is the system's request for r. It is Time Sensitive, for a
    /// to-do due to get through a Focus, which needs the app's
    /// com.apple.developer.usernotifications.time-sensitive entitlement.
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

    /// schedule puts what the core says in place of what is scheduled.
    /// asking says the app is on the screen, to ask for permission once
    /// there is something to remind of: never before, and never from the
    /// background.
    @MainActor
    static func schedule(_ client: CoreClient, asking: Bool) async {
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

    /// allowed says the system shows the app's notifications, asking first
    /// where it may and hasn't.
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

/// ReminderTarget is the to-do a reminder opens.
struct ReminderTarget: Equatable, Sendable {
    let pageID: String
    let blockID: String
}

/// ReminderInbox holds the to-do of a reminder tapped until a window opens
/// it: the tap that launches the app comes before any window.
@MainActor @Observable
final class ReminderInbox {
    static let shared = ReminderInbox()
    var target: ReminderTarget?
}

/// ReminderDelegate hears what the system does with the app's
/// notifications. It is the system center's delegate from before the app
/// finishes launching, as a tap that launches the app needs.
final class ReminderDelegate: NSObject, UNUserNotificationCenterDelegate, Sendable {
    static let shared = ReminderDelegate()

    /// A reminder shows while the app is on the screen too, where the
    /// system would hold it back.
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

    /// take keeps the to-do tapped for a window to open, and then says the
    /// tap is handled, on the main thread: what UIKit does as it hears that
    /// aborts the app anywhere else, which is why these are the delegate's
    /// completion handlers and not its async methods, whose end Swift says
    /// from a thread of its own.
    nonisolated static func take(_ target: ReminderTarget?, then done: @escaping () -> Void) {
        nonisolated(unsafe) let done = done
        Task { @MainActor in
            if let target { ReminderInbox.shared.target = target }
            done()
        }
    }
}
