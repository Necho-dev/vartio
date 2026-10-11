//go:build darwin || windows

package agent

import (
	"context"
	"log"
	"time"

	"fyne.io/systray"
)

// runTray owns the main thread (required by macOS) until the agent stops.
// There is deliberately no Quit item: the probe should not be turned off from
// the desktop; use uninstall / service remove instead.
func runTray(ctx context.Context, cfg config, logFile string) {
	systray.Run(func() { trayReady(ctx, cfg, logFile) }, nil)
}

func trayReady(ctx context.Context, cfg config, logFile string) {
	beforeExit = trayBeforeExit
	setTrayIcon("off")
	systray.SetTooltip("Vartio " + agentVersion)

	mStatus := systray.AddMenuItem("连接中…", "")
	mStatus.Disable()
	hub := cfg.URL
	if u, err := hubBase(cfg.URL); err == nil {
		hub = u.Host
	}
	mHub := systray.AddMenuItem("Hub："+hub, "")
	mHub.Disable()
	mVer := systray.AddMenuItem("版本 "+agentVersion+" · 设备 "+cfg.DeviceID, "")
	mVer.Disable()
	systray.AddSeparator()
	mOpen := systray.AddMenuItem("打开监控页", "在浏览器中查看本机")
	mLog := systray.AddMenuItem("查看日志", logFile)
	if logFile == "" {
		mLog.Hide()
	}
	mRe := systray.AddMenuItem("立即重连", "断开并立即重新连接 hub")

	go func() {
		err := serve(ctx, cfg)
		if ctx.Err() == nil {
			log.Printf("serve: %v", err)
		}
		systray.Quit()
	}()

	go func() {
		icon := ""
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			s := getLink()
			text, state := trayText(s)
			mStatus.SetTitle(text)
			systray.SetTooltip("Vartio " + agentVersion + " · " + text)
			if state != icon {
				setTrayIcon(state)
				icon = state
			}
			select {
			case <-ctx.Done():
				return
			case <-link.changed:
			case <-tick.C:
			case <-mOpen.ClickedCh:
				if u, err := hubBase(cfg.URL); err == nil {
					u.Path, u.Fragment = "/", "/system/"+cfg.DeviceID
					openURL(u.String())
				}
			case <-mLog.ClickedCh:
				openPath(logFile)
			case <-mRe.ClickedCh:
				log.Printf("reconnect requested from tray")
				requestReconnect()
			}
		}
	}()
}
