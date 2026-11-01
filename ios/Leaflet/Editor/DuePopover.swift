import UIKit

/// DuePopover changes a to-do's due date, as the desktop's due chip's
/// popover: a calendar, a time of day to add or take away, and Remove
/// date. It changes a draft, which the to-do takes as it closes; one closed
/// untouched leaves the to-do as it was.
final class DuePopover: UIViewController, UICalendarSelectionSingleDateDelegate,
    UIPopoverPresentationControllerDelegate
{
    /// dueHour is the time of day a due date takes as it is given one.
    static let dueHour = 9

    private let had: Bool
    /// day is the day chosen, none until one is for a to-do without a date.
    private var day: DateComponents?
    private var time: Date
    private var timed: Bool
    private var edited = false
    private var done = false
    /// onClose takes the date chosen, or none to take it away.
    private let onClose: (Due?) -> Void

    private let calendar = UICalendarView()
    private let timeRow = UIStackView()

    init(_ due: Due?, onClose: @escaping (Due?) -> Void) {
        had = due != nil
        let cal = Calendar.current
        day = due.map { cal.dateComponents([.year, .month, .day], from: $0.date) }
        timed = due?.timed ?? false
        if let due, due.timed {
            time = due.date
        } else {
            time =
                cal.date(bySettingHour: Self.dueHour, minute: 0, second: 0, of: due?.date ?? .now)
                ?? .now
        }
        self.onClose = onClose
        super.init(nibName: nil, bundle: nil)
        modalPresentationStyle = .popover
        popoverPresentationController?.delegate = self
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    override func viewDidLoad() {
        super.viewDidLoad()
        calendar.calendar = .current
        calendar.locale = Locale(identifier: L.language)
        calendar.fontDesign = .rounded
        let selection = UICalendarSelectionSingleDate(delegate: self)
        selection.selectedDate = day
        calendar.selectionBehavior = selection
        if let day { calendar.visibleDateComponents = day }

        timeRow.axis = .horizontal
        timeRow.spacing = 8
        timeRow.alignment = .center
        fillTimeRow()

        var parts: [UIView] = [calendar, timeRow]
        if had {
            var config = UIButton.Configuration.gray()
            config.title = L.editorRemoveDate
            config.baseForegroundColor = .systemRed
            let remove = UIButton(
                configuration: config,
                primaryAction: UIAction { [weak self] _ in self?.remove() })
            parts.append(remove)
        }
        let stack = UIStackView(arrangedSubviews: parts)
        stack.axis = .vertical
        stack.spacing = 10
        stack.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 12),
            stack.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -12),
            stack.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor, constant: 8),
            stack.bottomAnchor.constraint(
                lessThanOrEqualTo: view.safeAreaLayoutGuide.bottomAnchor, constant: -12),
            stack.widthAnchor.constraint(equalToConstant: 320),
        ])
        preferredContentSize = view.systemLayoutSizeFitting(
            CGSize(width: 344, height: UIView.layoutFittingCompressedSize.height))
    }

    /// fillTimeRow shows Add time, or the time of day with a button that
    /// takes it away.
    private func fillTimeRow() {
        for v in timeRow.arrangedSubviews { v.removeFromSuperview() }
        guard timed else {
            var config = UIButton.Configuration.gray()
            config.title = L.editorAddTime
            config.image = UIImage(systemName: "clock")
            config.imagePadding = 6
            timeRow.addArrangedSubview(
                UIButton(
                    configuration: config,
                    primaryAction: UIAction { [weak self] _ in self?.addTime() }))
            return
        }
        let picker = UIDatePicker()
        picker.datePickerMode = .time
        picker.preferredDatePickerStyle = .compact
        picker.locale = Locale(identifier: L.language)
        picker.date = time
        picker.accessibilityLabel = L.editorDueTime
        picker.addAction(
            UIAction { [weak self, weak picker] _ in
                guard let self, let picker else { return }
                self.time = picker.date
                self.edited = true
            }, for: .valueChanged)
        var config = UIButton.Configuration.plain()
        config.image = UIImage(systemName: "xmark")
        let x = UIButton(
            configuration: config,
            primaryAction: UIAction { [weak self] _ in
                self?.timed = false
                self?.edited = true
                self?.fillTimeRow()
            })
        x.accessibilityLabel = L.editorRemoveTime
        // The time at the start, its button at the end.
        let space = UIView()
        space.setContentHuggingPriority(.defaultLow, for: .horizontal)
        picker.setContentHuggingPriority(.required, for: .horizontal)
        x.setContentHuggingPriority(.required, for: .horizontal)
        timeRow.addArrangedSubview(picker)
        timeRow.addArrangedSubview(space)
        timeRow.addArrangedSubview(x)
    }

    private func addTime() {
        if day == nil {
            day = Calendar.current.dateComponents([.year, .month, .day], from: .now)
            (calendar.selectionBehavior as? UICalendarSelectionSingleDate)?
                .setSelected(day, animated: true)
        }
        timed = true
        edited = true
        fillTimeRow()
    }

    private func remove() {
        done = true
        onClose(nil)
        dismiss(animated: true)
    }

    /// chosen is the draft's date.
    private var chosen: Due? {
        let cal = Calendar.current
        guard let day, let start = cal.date(from: day) else { return nil }
        guard timed else { return Due(date: cal.startOfDay(for: start), timed: false) }
        let t = cal.dateComponents([.hour, .minute], from: time)
        let at = cal.date(
            bySettingHour: t.hour ?? Self.dueHour, minute: t.minute ?? 0, second: 0, of: start)
        return Due(date: at ?? start, timed: true)
    }

    override func viewDidDisappear(_ animated: Bool) {
        super.viewDidDisappear(animated)
        guard !done else { return }
        done = true
        if edited, let chosen { onClose(chosen) }
    }

    func dateSelection(
        _ selection: UICalendarSelectionSingleDate, didSelectDate dateComponents: DateComponents?
    ) {
        // A tap on the day chosen takes it away; the day stays, as the
        // desktop's calendar has no day unchosen.
        guard let dateComponents else {
            selection.setSelected(day, animated: false)
            return
        }
        day = dateComponents
        edited = true
    }

    // A popover on iPhone too, as the calendar fits.
    func adaptivePresentationStyle(
        for controller: UIPresentationController, traitCollection: UITraitCollection
    ) -> UIModalPresentationStyle { .none }
}
