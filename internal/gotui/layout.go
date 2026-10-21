package gotui

import (
	"image"
)

type layout struct {
	width, height int
	header        image.Rectangle
	tabs          image.Rectangle
	sidebar       image.Rectangle
	workflow      image.Rectangle
	austinCard    image.Rectangle
	tonyCard      image.Rectangle
	main          image.Rectangle
	austin        image.Rectangle
	tony          image.Rectangle
	duo           image.Rectangle
	composer      image.Rectangle
	footer        image.Rectangle

	compact        bool
	sidebarVisible bool
	split          bool
}

func (a *App) computeLayout(w, h int) layout {
	l := layout{width: w, height: h}
	if w < 1 || h < 1 {
		return l
	}
	headerH := 3
	if h < 28 {
		headerH = 2
	}
	if h < 14 {
		headerH = 1
	}
	tabsH := 1
	if h < 14 {
		tabsH = 0
	}
	footerH := 1
	composerH := 4
	switch {
	case h < 12:
		composerH = 1
	case h < 18:
		composerH = 2
	case h < 26:
		composerH = 3
	case h >= 40:
		composerH = 5
	}
	if headerH+tabsH+composerH+footerH+4 > h {
		composerH = max(1, h-headerH-tabsH-footerH-4)
	}
	l.compact = h < 28
	l.header = image.Rect(0, 0, w, headerH)
	l.tabs = image.Rect(1, headerH, max(1, w-1), headerH+tabsH)
	contentTop := headerH + tabsH
	contentBottom := h - composerH - footerH
	if contentBottom-contentTop < 3 {
		contentTop = headerH
		contentBottom = max(headerH+1, h-footerH)
	}
	l.composer = image.Rect(1, max(contentTop+1, h-composerH-footerH), max(1, w-1), h-footerH)
	l.footer = image.Rect(0, h-footerH, w, h)
	l.main = image.Rect(1, contentTop, max(1, w-1), contentBottom)

	l.sidebarVisible = a.sidebar && w >= 104 && contentBottom-contentTop >= 12
	if l.sidebarVisible {
		sidebarW := 30
		if w >= 120 {
			sidebarW = 36
		}
		if w >= 150 {
			sidebarW = 40
		}
		l.sidebar = image.Rect(1, contentTop, 1+sidebarW, contentBottom)
		l.main = image.Rect(l.sidebar.Max.X+1, contentTop, max(l.sidebar.Max.X+2, w-1), contentBottom)
		if a.sidebarFocus != panelNone && l.sidebar.Dy() >= 18 {
			// The expanded panel takes everything left by two title-height
			// panels and two one-cell gaps. Long values wrap inside it.
			const collapsedH = 3
			workH, austinH, tonyH := collapsedH, collapsedH, collapsedH
			switch a.sidebarFocus {
			case panelWorkflow:
				workH = l.sidebar.Dy() - 2*collapsedH - 2
			case panelAustin:
				austinH = l.sidebar.Dy() - 2*collapsedH - 2
			case panelTony:
				tonyH = l.sidebar.Dy() - 2*collapsedH - 2
			}
			l.workflow = image.Rect(l.sidebar.Min.X, l.sidebar.Min.Y, l.sidebar.Max.X, l.sidebar.Min.Y+workH)
			l.austinCard = image.Rect(l.sidebar.Min.X, l.workflow.Max.Y+1, l.sidebar.Max.X, l.workflow.Max.Y+1+austinH)
			l.tonyCard = image.Rect(l.sidebar.Min.X, l.austinCard.Max.Y+1, l.sidebar.Max.X, l.austinCard.Max.Y+1+tonyH)
		} else {
			workH := 10
			if contentBottom-contentTop >= 28 {
				workH = 12
			}
			if contentBottom-contentTop >= 36 {
				workH = 14
			}
			l.workflow = image.Rect(l.sidebar.Min.X, l.sidebar.Min.Y, l.sidebar.Max.X, min(l.sidebar.Max.Y, l.sidebar.Min.Y+workH))
			remaining := l.sidebar.Max.Y - l.workflow.Max.Y - 1
			cardH := remaining / 2
			if cardH < 5 {
				l.sidebarVisible = false
				l.sidebar = image.Rectangle{}
				l.workflow = image.Rectangle{}
				l.main = image.Rect(1, contentTop, max(1, w-1), contentBottom)
			} else {
				l.austinCard = image.Rect(l.sidebar.Min.X, l.workflow.Max.Y+1, l.sidebar.Max.X, l.workflow.Max.Y+1+cardH)
				l.tonyCard = image.Rect(l.sidebar.Min.X, l.austinCard.Max.Y+1, l.sidebar.Max.X, l.austinCard.Max.Y+1+cardH)
			}
		}
	}
	l.split = a.viewMode == viewSplit && l.main.Dx() >= 72 && l.main.Dy() >= 16
	if l.split {
		duoH := 5
		if l.main.Dy() < 26 {
			duoH = 4
		}
		if l.main.Dy() >= 30 {
			duoH = 9
		}
		topH := l.main.Dy() - duoH - 1
		mid := l.main.Min.X + (l.main.Dx()-1)/2
		l.austin = image.Rect(l.main.Min.X, l.main.Min.Y, mid, l.main.Min.Y+topH)
		l.tony = image.Rect(mid+1, l.main.Min.Y, l.main.Max.X, l.main.Min.Y+topH)
		l.duo = image.Rect(l.main.Min.X, l.austin.Max.Y+1, l.main.Max.X, l.main.Max.Y)
	}
	return l
}
