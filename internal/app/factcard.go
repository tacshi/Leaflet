package app

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/netimage"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/menu"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// factCardBlock is a card block of the open page: the card, and while the
// pointer is on it, a button to edit it where the page can be edited.
func (a *App) factCardBlock(c *ui.Context, n *doc.Node, width float32) {
	editable := a.page != nil && !a.page.editor.ReadOnly
	wrap := ui.Box(c.Key("factcard:" + n.ID)).FillWidth()
	hovered := wrap.Hovered()
	wrap.Children(func() {
		a.factCard(c, doc.CardOf(n), width)
		if editable {
			shown := map[bool]float32{true: 1, false: 0}[hovered]
			if barButton(c, "edit", "pencil", locales.EditorEditCard(), shown).Absolute().Top(8).Right(8).Clicked() {
				a.dialogs.openCard(n.ID, a.page.editor)
			}
		}
	})
}

// factCard draws a card width wide: as figures, its values large side by
// side, or as details, a line each beside its picture.
func (a *App) factCard(c *ui.Context, card doc.Card, width float32) {
	pal := theme.Of(c)
	box := ui.Column(c).FillWidth().Padding(14, 16).Gap(10).Radius(10).Border(1, pal.Divider).Background(pal.Surface).
		Role(ui.RoleGroup).Label(card.Title)
	box.Children(func() {
		if card.Title != "" || card.Icon != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).PaddingEnd(28).Children(func() {
				if card.Icon != "" {
					ui.Icon(c, theme.Icon(card.Icon)).FontSize(17).TextColor(pal.TextSecondary)
				}
				ui.Text(c, card.Title).FontSize(15).FontWeight(600).TextColor(pal.Text).Grow(1).Shrink(1)
			})
		}
		if card.Layout == doc.CardFigures {
			ui.Row(c).FillWidth().Wrap().Justify(ui.SpaceBetween).Gap(28).Children(func() {
				for i, f := range card.Facts {
					ui.Column(c.Key(i)).Gap(2).MinWidth(96).Children(func() {
						if f.Label != "" {
							ui.Text(c, f.Label).FontSize(12.5).TextColor(pal.TextMuted)
						}
						ui.RichText(c, editor.InlineSpans(f.Value, 24, 700, pal.Text, pal)...).LineHeight(1.25)
					})
				}
			})
			a.cardNote(c, card)
			return
		}
		side := min(max(width*0.26, 96), 190)
		ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Start).Children(func() {
			if _, failed := a.images.get(c, card.Image, factSide); card.Image != "" && !failed {
				netimage.Image(c, card.Image, netimage.Options{MaxSide: factSide, Placeholder: pal.Ground}).
					Size(side, side*5/4).Radius(12).Shrink(0)
			}
			ui.Column(c).Grow(1).Shrink(1).Gap(6).Children(func() {
				for i, f := range card.Facts {
					line := f.Value
					if f.Label != "" {
						line = doc.EscapeInline(f.Label) + doc.FactSeparator(f.Label) + f.Value
					}
					ui.RichText(c.Key(i), editor.InlineSpans(line, 14, 400, pal.Text, pal)...).FillWidth().LineHeight(1.5)
				}
				a.cardNote(c, card)
			})
		})
	})
}

// cardNote is a card's note, muted, and a chip for its sources.
func (a *App) cardNote(c *ui.Context, card doc.Card) {
	if card.Note == "" && len(card.Sources) == 0 {
		return
	}
	pal := theme.Of(c)
	ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		if card.Note != "" {
			ui.RichText(c.Key("note"), editor.InlineSpans(card.Note, 12.5, 400, pal.TextMuted, pal)...).Shrink(1)
		}
		sourceChip(c, card.Sources)
	})
}

// sourceChip names a card's first source, and how many more there are:
// a click opens the one, or a menu of them all.
func sourceChip(c *ui.Context, sources []doc.CardSource) {
	if len(sources) == 0 {
		return
	}
	pal := theme.Of(c)
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = doc.SourceName(s)
	}
	chip := ui.ButtonBase(c.Key("sources")).Height(20).Padding(0, 8).Gap(4).Radius(999).Background(pal.Ground).
		TextColor(pal.TextMuted).AlignItems(ui.Center).Cursor(ui.CursorPointer).
		Label(locales.CardSourcesLabel(strings.Join(names, locales.CardSourcesSeparator()))).Tooltip(strings.Join(names, "\n"))
	if chip.Hovered() {
		chip.Background(pal.Hover.Over(pal.Ground)).TextColor(pal.Text)
	}
	chip.Children(func() {
		ui.Text(c, names[0]).FontSize(11.5).SingleLine().Ellipsis("…").MaxWidth(150)
		if len(sources) > 1 {
			ui.Textf(c, "+%d", len(sources)-1).FontSize(11.5)
		}
	})
	if len(sources) == 1 {
		if chip.Clicked() {
			openLink(sources[0].URL)
		}
		return
	}
	chip.PopoverMenuButton(func(m *ui.Menu) {
		for i, s := range sources {
			if m.Item(names[i]).Subtitle(doc.SiteName(s.URL)).Chosen() {
				openLink(s.URL)
			}
		}
	})
}

// chatCard draws a ```card fence of an agent's answer or a comment as the
// card it holds, once its fence closes, width(c) wide; one that holds none
// shows as the code it is. Without picture the card leaves its picture out,
// as a comment's does: anyone the page's public link reaches can write one,
// and its picture would be fetched from where they say as the thread shows.
// A card without its picture takes no width, so width may then be nil.
func (a *App) chatCard(width func(*ui.Context) float32, picture bool) func(*ui.Context, *markdown.CodeBlockView) {
	return func(c *ui.Context, b *markdown.CodeBlockView) {
		if b.Open {
			shimmer(c, locales.HostSlashCard(), 13)
			return
		}
		n, ok := doc.CardNodeFromJSON(b.Code())
		if !ok {
			markdown.DefaultCodeBlock(c, b)
			return
		}
		card := doc.CardOf(n)
		var w float32
		if !picture {
			card.Image = ""
		} else if width != nil {
			w = width(c)
		}
		a.factCard(c, card, w)
	}
}

// cardIcons are the card icons as menu items show them, each made once.
var cardIcons = map[string]ui.MenuIcon{}

func cardMenuIcon(name string) ui.MenuIcon {
	icon, ok := cardIcons[name]
	if !ok {
		if made, err := mygo.NewMenuIconSVG(theme.Icon(name), true); err == nil {
			icon = made
		}
		cardIcons[name] = icon
	}
	return icon
}

// cardIconLabel names a card icon, one of doc.CardIcons, in the app's
// language; an icon it does not know keeps its Lucide name.
func cardIconLabel(name string) string {
	switch name {
	case "bus":
		return locales.CardIconBus()
	case "train-front":
		return locales.CardIconTrainFront()
	case "tram-front":
		return locales.CardIconTramFront()
	case "car":
		return locales.CardIconCar()
	case "bike":
		return locales.CardIconBike()
	case "footprints":
		return locales.CardIconFootprints()
	case "plane":
		return locales.CardIconPlane()
	case "ship":
		return locales.CardIconShip()
	case "map-pin":
		return locales.CardIconMapPin()
	case "clock":
		return locales.CardIconClock()
	case "calendar":
		return locales.CardIconCalendar()
	case "ticket":
		return locales.CardIconTicket()
	case "banknote":
		return locales.CardIconBanknote()
	case "wallet":
		return locales.CardIconWallet()
	case "phone":
		return locales.CardIconPhone()
	case "hospital":
		return locales.CardIconHospital()
	case "utensils":
		return locales.CardIconUtensils()
	case "bed-double":
		return locales.CardIconBedDouble()
	case "building-2":
		return locales.CardIconBuilding2()
	case "graduation-cap":
		return locales.CardIconGraduationCap()
	case "shopping-bag":
		return locales.CardIconShoppingBag()
	case "info":
		return locales.CardIconInfo()
	case "star":
		return locales.CardIconStar()
	case "sun":
		return locales.CardIconSun()
	case "cloud-rain":
		return locales.CardIconCloudRain()
	case "thermometer":
		return locales.CardIconThermometer()
	case "globe":
		return locales.CardIconGlobe()
	case "user":
		return locales.CardIconUser()
	case "package":
		return locales.CardIconPackage()
	case "trending-up":
		return locales.CardIconTrendingUp()
	}
	return name
}

func (d *dialogs) openCard(id string, ed *editor.Editor) {
	n := ed.Content.Find(id)
	if n == nil {
		return
	}
	card := doc.CardOf(n)
	d.cardOpen, d.cardID, d.cardEditor, d.cardError = true, id, ed, ""
	d.cardLayout = 1
	if card.Layout == doc.CardFigures {
		d.cardLayout = 0
	}
	d.cardIcon, d.cardTitle, d.cardImage, d.cardNote = card.Icon, card.Title, card.Image, card.Note
	d.cardFacts, d.cardSources = menu.CardFactsText(card), menu.CardSourcesText(card)
}

func (a *App) cardDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.cardOpen, 560, func() {
		dialogHeader(c, locales.DialogCardTitle(), locales.DialogCardSubtitle(), &d.cardOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Segmented(c.Key("layout"), &d.cardLayout, locales.DialogCardFigures(), locales.DialogCardDetails())
				ui.Spacer(c)
				icon, label := "", locales.DialogCardNoIcon()
				if d.cardIcon != "" {
					icon, label = d.cardIcon, locales.DialogCardIcon()
				}
				withIcon(c, ui.Button(c.Key("icon"), "").Label(locales.DialogCardIcon()), icon, label).
					PopoverMenuButton(func(m *ui.Menu) {
						if m.Item(locales.DialogCardNoIcon()).Radio(d.cardIcon == "").Chosen() {
							d.cardIcon = ""
						}
						m.Separator()
						for _, name := range doc.CardIcons {
							if m.Item(cardIconLabel(name)).Icon(cardMenuIcon(name)).Radio(d.cardIcon == name).Chosen() {
								d.cardIcon = name
							}
						}
					}, endMenu)
			})
			field(c, "title", &d.cardTitle, locales.DialogFieldTitle())
			if d.cardLayout == 1 {
				field(c, "image", &d.cardImage, locales.DialogCardPicture())
			}
			ui.TextArea(c.Key("facts"), &d.cardFacts).Height(140).FontSize(13).Placeholder(locales.DialogCardFacts())
			field(c, "note", &d.cardNote, locales.DialogCardNote())
			ui.TextArea(c.Key("sources"), &d.cardSources).Height(70).FontSize(12.5).Placeholder(locales.DialogCardSources())
			if d.cardError != "" {
				ui.Text(c, d.cardError).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.cardOpen = false
				}
				if primary(c, "save", "", locales.DialogCardSave()).Clicked() && a.canSaveIn(d.cardEditor) {
					layout := doc.CardDetails
					if d.cardLayout == 0 {
						layout = doc.CardFigures
					}
					image := d.cardImage
					if layout == doc.CardFigures {
						image = ""
					}
					data, err := menu.CardFromText(layout, d.cardIcon, d.cardTitle, image, d.cardFacts, d.cardNote, d.cardSources)
					if err != nil {
						d.cardError = err.Error()
						return
					}
					id := d.cardID
					d.cardEditor.Edit(func(content *doc.Content) {
						if n := content.Find(id); n != nil {
							if author, ok := n.Data["author"]; ok {
								data["author"] = author
							}
							n.Data = data
						}
					})
					d.cardOpen = false
				}
			})
		})
	})
}
