package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// keyMap is the single definition of every key freshen responds to. Handlers
// match against m.keys(), and the footer and the ? overlay render from the same
// value, so a binding that is disabled for the current context is neither
// handled nor advertised.
type keyMap struct {
	// Global keys behave identically on every screen and drill-down. Only the
	// modal layers (filter input, action menu, confirmation) suspend them.
	ForceQuit, Quit, Help, Back, Refresh, Filter, Jump, NextScreen, PrevScreen key.Binding

	// Movement within the current list, detail or help page.
	Up, Down, PageUp, PageDown, Home, End, Open key.Binding

	// Selected item.
	Menu, Browser, Copy key.Binding

	// Actions screen.
	ToggleQueue, CycleRuns key.Binding

	// Repositories and Concerns.
	PrevTab, NextTab, Sync, SyncAll, Switch, Push, Prune, Delete key.Binding

	// Modal layers.
	FilterApply, FilterClear, MenuSelect, MenuClose, Confirm, Cancel key.Binding
}

// screenOrder is the order of the top-level tabs, the 0–4 jump keys and Tab.
var screenOrder = []FocusType{FocusOverview, FocusRepos, FocusJobs, FocusRunners, FocusConcerns}

var baseKeys = keyMap{
	ForceQuit:  key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit (works everywhere)")),
	Quit:       key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	Help:       key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys for this screen")),
	Back:       key.NewBinding(key.WithKeys("esc", "left", "h"), key.WithHelp("esc/←", "back one level; clears the filter, then the message")),
	Refresh:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh this screen")),
	Filter:     key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter the nearest list")),
	Jump:       key.NewBinding(key.WithKeys("0", "1", "2", "3", "4"), key.WithHelp("0-4", "jump: Overview Repos Actions Runners Concerns")),
	NextScreen: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next screen")),
	PrevScreen: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous screen")),

	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
	PageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
	Home:     key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "first")),
	End:      key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "last")),
	Open:     key.NewBinding(key.WithKeys("enter", "right", "l"), key.WithHelp("enter/→", "open")),

	Menu:    key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "actions for the selection")),
	Browser: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open in GitHub")),
	Copy:    key.NewBinding(key.WithKeys("y", "c"), key.WithHelp("y", "copy link or runner ID")),

	ToggleQueue: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle runs / job queue")),
	CycleRuns:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "runs: active / needs attention / recent")),

	PrevTab: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "previous detail tab")),
	NextTab: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next detail tab")),
	Sync:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync or clone repository")),
	SyncAll: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "sync all (confirm)")),
	Switch:  key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "switch original/default branch")),
	Push:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "commit, push, open PR (confirm)")),
	Prune:   key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "prune branches/worktrees (confirm)")),
	Delete:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete archived clone (confirm)")),

	FilterApply: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply filter")),
	FilterClear: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter")),
	MenuSelect:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run action")),
	MenuClose:   key.NewBinding(key.WithKeys("esc", " "), key.WithHelp("esc", "close menu")),
	Confirm:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
	Cancel:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
}

// keys returns the bindings valid in the current context.
func (m Model) keys() keyMap {
	k := baseKeys
	confirming := m.PendingAction != ""
	menu := m.MenuOpen && !confirming
	modal := m.Filtering || confirming || menu
	free := !modal && !m.ShowHelp // no layer above the screen
	overview := m.ActiveFocus == FocusOverview
	repoScreen := m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns
	list := free && !overview && !m.detailVisible()

	for _, b := range []*key.Binding{&k.Quit, &k.Help, &k.Back, &k.Refresh, &k.Filter, &k.Jump, &k.NextScreen, &k.PrevScreen} {
		b.SetEnabled(!modal)
	}
	scrollable := m.ShowHelp || (free && !overview)
	k.Up.SetEnabled(scrollable || menu)
	k.Down.SetEnabled(scrollable || menu)
	for _, b := range []*key.Binding{&k.PageUp, &k.PageDown, &k.Home, &k.End} {
		b.SetEnabled(scrollable)
	}
	k.Open.SetEnabled(list)
	for _, b := range []*key.Binding{&k.Menu, &k.Browser, &k.Copy} {
		b.SetEnabled(free && !overview)
	}
	actionsList := free && m.ActiveFocus == FocusJobs && m.OpenRun == nil
	k.ToggleQueue.SetEnabled(actionsList)
	k.CycleRuns.SetEnabled(actionsList && !m.QueueView)
	repoDetail := free && repoScreen && m.Detail
	k.PrevTab.SetEnabled(repoDetail)
	k.NextTab.SetEnabled(repoDetail)
	for _, b := range []*key.Binding{&k.Sync, &k.SyncAll, &k.Switch, &k.Push, &k.Prune, &k.Delete} {
		b.SetEnabled(free && repoScreen)
	}
	k.FilterApply.SetEnabled(m.Filtering)
	k.FilterClear.SetEnabled(m.Filtering)
	k.MenuSelect.SetEnabled(menu)
	k.MenuClose.SetEnabled(menu)
	k.Confirm.SetEnabled(confirming)
	k.Cancel.SetEnabled(confirming)
	return k
}

// pair merges two real bindings into one footer hint ("↑/↓ move") without
// inventing keys: the key text is taken from the bindings themselves.
func pair(a, b key.Binding, desc string) key.Binding {
	first := strings.SplitN(a.Help().Key, "/", 2)[0]
	second := strings.SplitN(b.Help().Key, "/", 2)[0]
	out := key.NewBinding(key.WithKeys(a.Keys()...), key.WithHelp(first+"/"+second, desc))
	out.SetEnabled(a.Enabled() && b.Enabled())
	return out
}

func relabel(b key.Binding, desc string) key.Binding {
	out := key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
	out.SetEnabled(b.Enabled())
	return out
}

// footerBindings are the contextual hints on the last row, most important
// first. The final hint (keys help or the way out of a modal layer) is always
// rendered; footerView drops hints from the end of the rest to fit.
func (m Model) footerBindings() []key.Binding {
	k := m.keys()
	var out []key.Binding
	add := func(bs ...key.Binding) {
		for _, b := range bs {
			if b.Enabled() {
				out = append(out, b)
			}
		}
	}
	queue := "job queue"
	if m.QueueView {
		queue = "runs"
	}
	switch {
	case m.PendingAction != "":
		add(k.Confirm, relabel(k.ForceQuit, "quit"), k.Cancel)
	case m.MenuOpen:
		add(pair(k.Up, k.Down, "choose"), relabel(k.MenuSelect, "run"), relabel(k.MenuClose, "close"))
	case m.Filtering:
		add(relabel(k.FilterApply, "apply"), relabel(k.ForceQuit, "quit"), relabel(k.FilterClear, "clear"))
	case m.ShowHelp:
		add(pair(k.Up, k.Down, "scroll"), relabel(k.Jump, "screens"), k.Quit, relabel(k.Help, "close"))
	case m.ActiveFocus == FocusOverview:
		add(relabel(k.Jump, "screens"), relabel(k.NextScreen, "next"), relabel(k.Filter, "filter repos"), relabel(k.Refresh, "refresh"), k.Quit, relabel(k.Help, "keys"))
	case m.detailVisible():
		add(pair(k.Up, k.Down, "scroll"), relabel(k.Back, "back"), pair(k.PrevTab, k.NextTab, "tab"), relabel(k.Menu, "actions"), relabel(k.Refresh, "refresh"), k.Quit, relabel(k.Help, "keys"))
	default:
		add(pair(k.Up, k.Down, "move"), relabel(k.Open, "open"), relabel(k.Filter, "filter"), relabel(k.ToggleQueue, queue), relabel(k.CycleRuns, "filter runs"),
			relabel(k.Menu, "actions"), relabel(k.Refresh, "refresh"), relabel(k.Jump, "screens"), k.Quit, relabel(k.Help, "keys"))
	}
	return out
}

type helpGroup struct {
	title    string
	bindings []key.Binding
}

// helpGroups lists every binding valid on the current screen, grouped. It is
// evaluated for the screen under the overlay, not for the overlay itself.
func (m Model) helpGroups() []helpGroup {
	under := m
	under.ShowHelp = false
	k := under.keys()
	groups := []helpGroup{
		{"Anywhere", []key.Binding{k.Jump, k.NextScreen, k.PrevScreen, k.Back, k.Filter, k.Refresh, k.Help, k.Quit, k.ForceQuit}},
		{"Move", []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End, k.Open}},
		{"Selection", []key.Binding{k.Menu, k.Browser, k.Copy}},
		{"Actions", []key.Binding{k.ToggleQueue, k.CycleRuns}},
		{"Repository", []key.Binding{k.PrevTab, k.NextTab, k.Sync, k.SyncAll, k.Switch, k.Push, k.Prune, k.Delete}},
	}
	out := groups[:0]
	for _, g := range groups {
		enabled := g.bindings[:0]
		for _, b := range g.bindings {
			if b.Enabled() {
				enabled = append(enabled, b)
			}
		}
		if len(enabled) > 0 {
			out = append(out, helpGroup{g.title, enabled})
		}
	}
	return out
}
