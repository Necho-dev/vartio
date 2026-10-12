//go:build darwin && cgo

package agent

import (
	_ "embed"

	"fyne.io/systray"
)

var (
	//go:embed trayicons/mac-ok.png
	macOK []byte
	//go:embed trayicons/mac-off.png
	macOff []byte
	//go:embed trayicons/mac-up.png
	macUp []byte
)

// trayFallbackPosition puts a first-run icon just left of the system items
// (clock, Control Center) so the notch cannot hide it; ⌘-drag moves it and the
// new place survives restarts and upgrades.
const trayFallbackPosition = 250

func init() { systray.SetAutosaveName(launchLabel, trayFallbackPosition) }

func trayLinked() bool { return true }

// trayBeforeExit drops the status item ahead of an in-place exec; otherwise the
// new image inherits the old item and ignores the saved position.
func trayBeforeExit() { systray.RemoveStatusItem() }

func setTrayIcon(state string) {
	icon := map[string][]byte{"ok": macOK, "off": macOff, "up": macUp}[state]
	systray.SetTemplateIcon(icon, icon)
}
