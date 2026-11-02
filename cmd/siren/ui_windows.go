//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"siren/internal/config"
	"siren/internal/logsetup"
)

// initConsole attaches the hidden console of a windowsgui process to the
// parent terminal, so running siren.exe from PowerShell still prints. A
// double-click from Explorer has no parent console: this is a no-op then.
func initConsole() {
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getConsoleWindow := modkernel32.NewProc("GetConsoleWindow")
	attachConsole := modkernel32.NewProc("AttachConsole")
	const attachParentProcess = ^uintptr(0) // (DWORD)-1

	if h, _, _ := getConsoleWindow.Call(); h != 0 {
		return // already attached to a console
	}
	if r, _, _ := attachConsole.Call(attachParentProcess); r == 0 {
		return // no parent console (Explorer launch): stay quiet, tray only
	}
	// Reopen the standard handles onto the attached console and switch it
	// to UTF-8: the agent prints Russian text, the default legacy codepage
	// garbles it.
	setConsoleOutputCP := modkernel32.NewProc("SetConsoleOutputCP")
	setConsoleOutputCP.Call(65001) // CP_UTF8
	if h, err := windows.CreateFile(windows.StringToUTF16Ptr("CONOUT$"),
		windows.GENERIC_WRITE, windows.FILE_SHARE_WRITE|windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, 0, 0); err == nil {
		f := os.NewFile(uintptr(h), "CONOUT$")
		os.Stdout = f
		os.Stderr = f
	}
	if h, err := windows.CreateFile(windows.StringToUTF16Ptr("CONIN$"),
		windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, 0, 0); err == nil {
		os.Stdin = os.NewFile(uintptr(h), "CONIN$")
	}
}

// Single instance: a named mutex marks the primary process; a named
// auto-reset event carries "open the window" requests to it.
const (
	uiMutexName = `Local\siren-agent-instance`
	uiOpenEvent = `Local\siren-agent-open-window`
)

// singletonMutex is kept for the process lifetime; releasing it would let a
// second instance become primary.
var singletonMutex windows.Handle

// secondInstanceSignalsPrimary reports whether this process is a duplicate.
// The duplicate signals the primary to open its control window and the
// caller must exit.
func secondInstanceSignalsPrimary() bool {
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(uiMutexName))
	if err == nil {
		singletonMutex = h
		runtime.KeepAlive(singletonMutex)
		return false
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// CreateEvent opens the primary's event by name.
		if ev, err := windows.CreateEvent(nil, 0, 0, windows.StringToUTF16Ptr(uiOpenEvent)); err == nil {
			_ = windows.SetEvent(ev)
		}
		return true
	}
	return false // mutex failure: prefer running to not running at all
}

// writeUIPanic appends a UI panic report to %TEMP%\siren-ui-panic.log.
func writeUIPanic(r interface{}) {
	f, ferr := os.OpenFile(filepath.Join(os.TempDir(), "siren-ui-panic.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s panic: %v\n%s\n",
		time.Now().Format("2006-01-02 15:04:05"), r, debug.Stack())
}

// watchOpenRequests opens the control window whenever a duplicate process
// asks for it.
func watchOpenRequests(a *uiApp) {
	ev, err := windows.CreateEvent(nil, 0, 0, windows.StringToUTF16Ptr(uiOpenEvent))
	if err != nil {
		return
	}
	go func() {
		for {
			s, _ := windows.WaitForSingleObject(ev, windows.INFINITE)
			if s == windows.WAIT_OBJECT_0 && !a.exiting {
				a.mw.Synchronize(a.showWindow)
			}
		}
	}()
}

// uiApp owns the control window, the tray icon and the pipeline lifecycle
// of the interactive (double-click) launch.
type uiApp struct {
	cfgPath string
	cfg     *config.Config
	cfgErr  error

	traps        []config.Trap
	profilePorts map[int]bool
	trapSlots    [trapSlotCount]*walk.CheckBox

	mw       *walk.MainWindow
	svcLabel *walk.Label
	msgLabel *walk.Label

	agentNameLE *walk.LineEdit
	tokenLE     *walk.LineEdit
	chatLE      *walk.LineEdit
	windowLE    *walk.LineEdit
	allowTE     *walk.TextEdit
	logCombo    *walk.ComboBox
	portLE      *walk.LineEdit
	delLE       *walk.LineEdit
	emuCombo    *walk.ComboBox
	tlsCB       *walk.CheckBox

	healthTE *walk.TextEdit
	checkBtn *walk.PushButton
	checking int32 // guards one live health check at a time

	ctx      context.Context
	cancel   context.CancelFunc
	pipeline bool
	exiting  bool
	ni       *walk.NotifyIcon
}

var emulatorNames = []string{
	config.EmulatorSilent, config.EmulatorHTTP, config.EmulatorFTP, config.EmulatorTelnet,
	config.EmulatorVNC, config.EmulatorMySQL, config.EmulatorPostgres, config.EmulatorSSH,
	config.EmulatorRDP,
}

// trapSlotCount is the fixed number of trap checkboxes: walk fails to lay out
// dynamically created widgets reliably, so the form uses static slots and
// rebuildTraps only updates their text/state/visibility.
const trapSlotCount = 24

// freeParentConsole detaches the process from its borrowed console (see
// initConsole): the interactive UI has no console output, and staying
// attached would tie its lifetime to the console window — closing that
// window terminates every attached process, the UI included.
func freeParentConsole() {
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	freeConsole := modkernel32.NewProc("FreeConsole")
	_, _, _ = freeConsole.Call()
}

// runInteractive is the double-click mode: the agent pipeline (unless the
// service owns the job) plus a tray icon and a control window.
func runInteractive(cfgPath string) (err error) {
	// A GUI-subsystem process prints nothing on a panic: capture it to a
	// file so a dying UI leaves a trace instead of just vanishing.
	defer func() {
		if r := recover(); r != nil {
			writeUIPanic(r)
			err = fmt.Errorf("control UI panicked: %v", r)
		}
	}()
	if secondInstanceSignalsPrimary() {
		return nil // duplicate: primary opens the window, we leave
	}
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		absCfg = cfgPath
	}

	a := &uiApp{cfgPath: absCfg}
	a.cfg, a.cfgErr = config.Load(absCfg)
	if a.cfg != nil {
		a.traps = append([]config.Trap(nil), a.cfg.Traps...)
		a.profilePorts = map[int]bool{}
		for _, t := range config.ProfileTraps(a.cfg.Profile) {
			a.profilePorts[t.Port] = true
		}
	}

	if err := a.build(); err != nil {
		return fmt.Errorf("control UI: %w", err)
	}
	defer a.teardown()

	// The SCM service owns the agent job when it runs: a second pipeline in
	// this process would fight it for ports and duplicate alerts.
	if a.cfgErr == nil && !a.serviceRunning() {
		a.startPipeline()
	}
	// The interactive UI needs no console: logs go to the file and panics
	// to the crash log. Detaching also unties the UI's lifetime from the
	// console that launched it — closing that console window would
	// otherwise terminate every attached process, the UI included. The
	// -console mode keeps its console (its logs are the point).
	freeParentConsole()
	watchOpenRequests(a)
	go a.pollServiceState()
	go a.refreshHealthLoop()

	if a.serviceNotInstalled() {
		// First run guidance: nothing installed yet, show the window.
		a.showWindow()
	}
	a.mw.Run()
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

func (a *uiApp) build() error {
	var trayIcon *walk.Icon
	if img := trayImage(); img != nil {
		if ic, err := walk.NewIconFromImage(img); err == nil {
			trayIcon = ic
		}
	}

	title := "Siren — управление агентом"
	if a.cfg != nil {
		title += " · " + a.cfg.AgentName
	}

	// Trap slots are static (4 per row): dynamic widget creation proved
	// unreliable in walk, so rebuildTraps only updates the fixed slots.
	trapRows := make([]Widget, 0, trapSlotCount/4)
	for r := 0; r < trapSlotCount/4; r++ {
		boxes := make([]Widget, 0, 5)
		for c := 0; c < 4; c++ {
			boxes = append(boxes, CheckBox{AssignTo: &a.trapSlots[r*4+c]})
		}
		// The trailing spacer keeps the row left-aligned: walk centers an
		// HBox row that has nothing to absorb the extra width.
		boxes = append(boxes, HSpacer{})
		trapRows = append(trapRows, Composite{Layout: HBox{MarginsZero: true, Spacing: 12}, Children: boxes})
	}

	err := (MainWindow{
		AssignTo: &a.mw,
		Title:    title,
		Font:     Font{PointSize: 9},
		MinSize:  Size{Width: 560, Height: 700},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 10, Right: 12, Bottom: 10}, Spacing: 8},
		Children: []Widget{
			GroupBox{Title: "Служба Windows (LocalSystem, автостарт)", Layout: VBox{
				Margins: Margins{Left: 8, Top: 4, Right: 8, Bottom: 8}, Spacing: 6,
			}, Children: []Widget{
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Состояние:"},
					Label{AssignTo: &a.svcLabel, Text: "…", Font: Font{PointSize: 9, Bold: true}},
					HSpacer{},
				}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					PushButton{Text: "Установить и запустить", MaxSize: Size{Width: 170}, OnClicked: a.installStart},
					PushButton{Text: "Стоп", MaxSize: Size{Width: 90}, OnClicked: a.stopSvc},
					PushButton{Text: "Удалить", MaxSize: Size{Width: 90}, OnClicked: a.removeSvc},
					HSpacer{},
				}},
			}},
			GroupBox{Title: "Ловушки (галочка = порт слушается)", Layout: VBox{
				Margins: Margins{Left: 8, Top: 4, Right: 8, Bottom: 8}, Spacing: 6,
			}, Children: append([]Widget{
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Добавить:"},
					LineEdit{AssignTo: &a.portLE, CueBanner: "порт, напр. 8080", MinSize: Size{Width: 110}, MaxSize: Size{Width: 140}},
					ComboBox{AssignTo: &a.emuCombo, Model: emulatorNames, Value: config.EmulatorSilent, MaxSize: Size{Width: 130}},
					CheckBox{AssignTo: &a.tlsCB, Text: "TLS"},
					PushButton{Text: "Добавить", MaxSize: Size{Width: 100}, OnClicked: a.addTrap},
					HSpacer{},
				}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Удалить порт:"},
					LineEdit{AssignTo: &a.delLE, CueBanner: "порт", MinSize: Size{Width: 110}, MaxSize: Size{Width: 140}},
					PushButton{Text: "Удалить", MaxSize: Size{Width: 100}, OnClicked: a.removeTrapClicked},
					HSpacer{},
				}},
			}, trapRows...)},
			GroupBox{Title: "Диски (сторож здоровья)", Layout: VBox{
				Margins: Margins{Left: 8, Top: 4, Right: 8, Bottom: 8}, Spacing: 6,
			}, Children: []Widget{
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					PushButton{Text: "Обновить", MaxSize: Size{Width: 110}, OnClicked: a.refreshHealthPanel},
					PushButton{AssignTo: &a.checkBtn, Text: "Проверить сейчас", MaxSize: Size{Width: 150}, OnClicked: a.liveHealthCheck},
					HSpacer{},
				}},
				TextEdit{AssignTo: &a.healthTE, ReadOnly: true, VScroll: true,
					MinSize: Size{Height: 150}, MaxSize: Size{Height: 300}},
			}},
			GroupBox{Title: "Настройки", Layout: VBox{
				Margins: Margins{Left: 8, Top: 4, Right: 8, Bottom: 8}, Spacing: 6,
			}, Children: []Widget{
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Имя машины:", MinSize: Size{Width: 170}},
					LineEdit{AssignTo: &a.agentNameLE, MinSize: Size{Width: 280}, MaxSize: Size{Width: 420}},
					HSpacer{},
				}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Token бота:", MinSize: Size{Width: 170}},
					LineEdit{AssignTo: &a.tokenLE, PasswordMode: true},
				}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Chat ID:", MinSize: Size{Width: 170}},
					LineEdit{AssignTo: &a.chatLE, MinSize: Size{Width: 280}, MaxSize: Size{Width: 280}},
					HSpacer{},
				}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Окно агрегации, секунд:", MinSize: Size{Width: 170}},
					LineEdit{AssignTo: &a.windowLE, MinSize: Size{Width: 90}, MaxSize: Size{Width: 90}},
					HSpacer{},
				}},
				Composite{Layout: HBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
					Label{Text: "Уровень журнала:", MinSize: Size{Width: 170}},
					ComboBox{AssignTo: &a.logCombo, Model: []string{"debug", "info", "warn", "error"}, Value: "info", MinSize: Size{Width: 120}},
					HSpacer{},
				}},
				Label{Text: "Allowlist — IP/CIDR или хост:порты (напр. 192.168.1.10:22), не алертить (по одному на строку):"},
				TextEdit{AssignTo: &a.allowTE, VScroll: true, MinSize: Size{Height: 90}, MaxSize: Size{Height: 140}},
			}},
			Label{AssignTo: &a.msgLabel, Text: "", Font: Font{PointSize: 9, Bold: true}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				PushButton{Text: "Сохранить и перезапустить службу", OnClicked: a.save},
				HSpacer{},
			}},
		},
	}).Create()
	if err != nil {
		return err
	}

	// Open at a size that fits the content: walk shows a fresh MainWindow
	// at its minimum size, which cramps the trap grid and settings rows.
	bounds := a.mw.Bounds()
	bounds.Width, bounds.Height = 660, 800
	_ = a.mw.SetBounds(bounds)

	if trayIcon != nil {
		a.mw.SetIcon(trayIcon)
	}
	a.fillFromConfig()
	a.rebuildTraps()
	a.refreshHealthPanel()

	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !a.exiting {
			*canceled = true
			a.mw.Hide() // the window hides; the agent keeps running in the tray
		}
	})
	a.setupTray(trayIcon)
	return nil
}

func (a *uiApp) fillFromConfig() {
	if a.cfg == nil {
		if a.cfgErr != nil {
			a.setMsg("Конфиг не читается: "+a.cfgErr.Error(), true)
		}
		return
	}
	a.agentNameLE.SetText(a.cfg.AgentName)
	a.tokenLE.SetText(a.cfg.Telegram.Token)
	a.chatLE.SetText(a.cfg.Telegram.ChatID)
	a.windowLE.SetText(strconv.Itoa(a.cfg.AggregateWindowSeconds))
	a.allowTE.SetText(strings.Join(a.cfg.Allowlist, "\r\n"))
	a.logCombo.SetText(a.cfg.LogLevel)
}

// rebuildTraps updates the fixed checkbox slots from the trap list; it never
// creates or destroys widgets.
func (a *uiApp) rebuildTraps() {
	for i := range a.trapSlots {
		cb := a.trapSlots[i]
		if i >= len(a.traps) {
			cb.SetVisible(false)
			cb.SetChecked(false)
			continue
		}
		t := a.traps[i]
		label := fmt.Sprintf("%d · %s", t.Port, t.Emulator)
		if t.TLS {
			label += "+TLS"
		}
		cb.SetText(label)
		cb.SetChecked(t.Enabled)
		cb.SetVisible(true)
	}
	if len(a.traps) > trapSlotCount {
		a.setMsg(fmt.Sprintf("Показаны первые %d ловушек из %d — остальные в конфиге", trapSlotCount, len(a.traps)), true)
	}
}

// removeTrapClicked deletes the trap whose port is typed in the delete
// field; profile default ports are protected from removal.
func (a *uiApp) removeTrapClicked() {
	port, err := strconv.Atoi(strings.TrimSpace(a.delLE.Text()))
	if err != nil || port < 1 || port > 65535 {
		a.setMsg("Порт должен быть числом 1..65535", true)
		return
	}
	if a.profilePorts[port] {
		a.setMsg(fmt.Sprintf("Порт %d входит в профиль — его можно только отключить галочкой", port), true)
		return
	}
	for i, t := range a.traps {
		if t.Port == port {
			a.traps = append(a.traps[:i], a.traps[i+1:]...)
			a.delLE.SetText("")
			a.setMsg("", false)
			a.rebuildTraps()
			return
		}
	}
	a.setMsg(fmt.Sprintf("Порта %d нет в списке", port), true)
}

func (a *uiApp) addTrap() {
	port, err := strconv.Atoi(strings.TrimSpace(a.portLE.Text()))
	if err != nil || port < 1 || port > 65535 {
		a.setMsg("Порт должен быть числом 1..65535", true)
		return
	}
	for _, t := range a.traps {
		if t.Port == port {
			a.setMsg(fmt.Sprintf("Порт %d уже в списке", port), true)
			return
		}
	}
	emu := a.emuCombo.Text()
	if emu == "" {
		emu = config.EmulatorSilent
	}
	a.traps = append(a.traps, config.Trap{Port: port, Enabled: true, Emulator: emu, TLS: a.tlsCB.Checked()})
	a.portLE.SetText("")
	a.tlsCB.SetChecked(false)
	a.setMsg("", false)
	a.rebuildTraps()
}

func (a *uiApp) save() {
	if a.cfg == nil {
		a.setMsg("Конфиг не загружен: "+a.cfgErr.Error(), true)
		return
	}
	for i := range a.traps {
		if i < trapSlotCount && a.trapSlots[i] != nil {
			a.traps[i].Enabled = a.trapSlots[i].Checked()
		}
	}
	windowSec, err := strconv.Atoi(strings.TrimSpace(a.windowLE.Text()))
	if err != nil || windowSec <= 0 {
		a.setMsg("Окно агрегации должно быть положительным числом", true)
		return
	}
	var allow []string
	for _, line := range strings.Split(a.allowTE.Text(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			allow = append(allow, line)
		}
	}

	cfg := &config.Config{
		AgentName:              strings.TrimSpace(a.agentNameLE.Text()),
		Profile:                a.cfg.Profile,
		Traps:                  a.traps,
		Telegram:               a.cfg.Telegram,
		AggregateWindowSeconds: windowSec,
		Allowlist:              allow,
		NameMap:                a.cfg.NameMap,
		SshdUnit:               a.cfg.SshdUnit,
		DataDir:                a.cfg.DataDir,
		Health:                 a.cfg.Health, // keep the watch section: the UI form has no fields for it
		LogLevel:               a.logCombo.Text(),
		LogFile:                a.cfg.LogFile,
	}
	if cfg.AgentName == "" {
		a.setMsg("Имя машины не может быть пустым", true)
		return
	}
	cfg.Telegram.Token = strings.TrimSpace(a.tokenLE.Text())
	cfg.Telegram.ChatID = strings.TrimSpace(a.chatLE.Text())

	if err := config.Save(a.cfgPath, cfg); err != nil {
		a.setMsg("Не сохранено: "+err.Error(), true)
		return
	}
	a.cfg = cfg
	a.applyChanges()
	a.setMsg("Сохранено и применено ("+time.Now().Format("15:04:05")+")", false)
}

// applyChanges restarts whatever runs the agent: this process's pipeline
// and/or the SCM service.
func (a *uiApp) applyChanges() {
	if a.pipeline {
		a.cancel()
		time.Sleep(700 * time.Millisecond) // let listeners release the ports
		a.startPipeline()
	}
	if st, err := serviceState(); err == nil && st == "работает" {
		_ = serviceCommand("stop", a.cfgPath, "", "")
		_ = serviceCommand("start", a.cfgPath, "", "")
	}
}

func (a *uiApp) startPipeline() {
	log, cleanup, err := logsetup.New(a.cfg.LogLevel, a.cfg.LogFile)
	if err != nil {
		log, cleanup = slog.Default(), func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx, a.cancel, a.pipeline = ctx, cancel, true
	go func() {
		if err := runAgent(ctx, a.cfg, log); err != nil {
			log.Error("agent pipeline failed", "err", err)
		}
		a.pipeline = false
		cleanup()
	}()
}

func (a *uiApp) setupTray(icon *walk.Icon) {
	ni, err := walk.NewNotifyIcon(a.mw)
	if err != nil {
		return
	}
	if icon != nil {
		_ = ni.SetIcon(icon)
	}
	_ = ni.SetToolTip("Siren — агент безопасности (ловушки + датчики входов)")
	_ = ni.SetVisible(true)

	// The NotifyIcon creates its context menu internally; actions go into it.
	menu := ni.ContextMenu()
	addMenuAction(menu, "Открыть управление", func() { a.showWindow() })
	addMenuAction(menu, "Установить и запустить службу", a.installStart)
	addMenuAction(menu, "Остановить службу", a.stopSvc)
	addMenuAction(menu, "Удалить службу", a.removeSvc)
	addMenuAction(menu, "Выход (остановить агента)", a.exit)
	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			a.showWindow()
		}
	})
	a.ni = ni
}

func addMenuAction(menu *walk.Menu, text string, fn func()) {
	act := walk.NewAction()
	act.SetText(text)
	act.Triggered().Attach(fn)
	_ = menu.Actions().Add(act)
}

func (a *uiApp) showWindow() {
	a.mw.Show()
	win.SetForegroundWindow(a.mw.Handle())
	a.refreshHealthPanel() // every open re-reads the persisted snapshot
}

func (a *uiApp) teardown() {
	if a.ni != nil {
		_ = a.ni.Dispose()
	}
}

func (a *uiApp) exit() {
	a.exiting = true
	if a.cancel != nil {
		a.cancel()
	}
	a.mw.Close()
}

// pollServiceState refreshes the service label while the window lives.
func (a *uiApp) pollServiceState() {
	for !a.exiting {
		st, err := serviceState()
		if a.mw != nil {
			a.mw.Synchronize(func() {
				if a.exiting {
					return
				}
				if err != nil {
					a.svcLabel.SetText("нет доступа к службе: " + err.Error())
					return
				}
				a.svcLabel.SetText(st)
			})
		}
		time.Sleep(3 * time.Second)
	}
}

// refreshHealthPanel re-renders the disk section from the persisted
// snapshot; runs on the UI thread (event handlers, Synchronize callbacks).
func (a *uiApp) refreshHealthPanel() {
	if a.healthTE == nil {
		return
	}
	a.healthTE.SetText(renderHealthPanel(a.cfg, time.Now()))
}

// refreshHealthLoop re-reads the persisted snapshot once a minute while the
// window is visible, so snapshots written by the running service appear in
// the panel without any user action.
func (a *uiApp) refreshHealthLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for !a.exiting {
		<-t.C
		if a.exiting || a.mw == nil {
			return
		}
		a.mw.Synchronize(func() {
			if a.exiting || !a.mw.Visible() {
				return
			}
			a.refreshHealthPanel()
		})
	}
}

// liveHealthCheck runs one collector pass in this process and shows the
// fresh result. It never touches the persisted state file (the service's
// diff baseline must stay intact) and no notifier exists here, so nothing
// is sent anywhere. Concurrent clicks are collapsed by the checking flag.
func (a *uiApp) liveHealthCheck() {
	if a.cfg == nil {
		a.setMsg("Конфиг не читается — проверка недоступна: "+a.cfgErr.Error(), true)
		return
	}
	if a.cfg.Health == nil || !a.cfg.Health.Enabled {
		a.setMsg("Сторож дисков выключен — проверка недоступна", true)
		return
	}
	if !atomic.CompareAndSwapInt32(&a.checking, 0, 1) {
		return // a check is already running
	}
	a.checkBtn.SetEnabled(false)
	a.checkBtn.SetText("проверяю…")
	go func() {
		text := renderLiveHealth(a.cfg)
		a.mw.Synchronize(func() {
			if a.exiting {
				return
			}
			a.healthTE.SetText(text)
			a.checkBtn.SetEnabled(true)
			a.checkBtn.SetText("Проверить сейчас")
			atomic.StoreInt32(&a.checking, 0)
		})
	}()
}

func (a *uiApp) serviceRunning() bool {
	st, err := serviceState()
	return err == nil && st == "работает"
}

func (a *uiApp) serviceNotInstalled() bool {
	st, err := serviceState()
	return err == nil && st == "не установлена"
}

func (a *uiApp) installStart() {
	st, err := serviceState()
	if err != nil {
		a.setMsg("Нет доступа к службе: "+err.Error(), true)
		return
	}
	if st == "не установлена" {
		if err = serviceCommand("install", a.cfgPath, "", ""); err != nil {
			a.setMsg("Установка не удалась: "+err.Error(), true)
			return
		}
	}
	if st != "работает" {
		if err = serviceCommand("start", a.cfgPath, "", ""); err != nil {
			a.setMsg("Запуск не удался: "+err.Error(), true)
			return
		}
	}
	a.setMsg("Служба установлена и запущена", false)
}

func (a *uiApp) stopSvc() {
	if err := serviceCommand("stop", a.cfgPath, "", ""); err != nil {
		a.setMsg("Стоп не удался: "+err.Error(), true)
		return
	}
	a.setMsg("Служба остановлена", false)
}

func (a *uiApp) removeSvc() {
	if err := serviceCommand("remove", a.cfgPath, "", ""); err != nil {
		a.setMsg("Удаление не удалось: "+err.Error(), true)
		return
	}
	a.setMsg("Служба удалена", false)
}

func (a *uiApp) setMsg(text string, isErr bool) {
	if a.msgLabel != nil {
		a.msgLabel.SetText(text)
		if isErr {
			a.msgLabel.SetTextColor(walk.RGB(190, 30, 30))
		} else {
			a.msgLabel.SetTextColor(walk.RGB(30, 110, 30))
		}
	}
	// Inline labels proved easy to miss: validation errors also pop a modal
	// dialog so the rejection cannot go unnoticed.
	if isErr && a.mw != nil {
		walk.MsgBox(a.mw, "Siren — ошибка", text, walk.MsgBoxIconError)
	}
}

// serviceState returns a human-readable SCM state for the UI.
func serviceState() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return "не установлена", nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	switch st.State {
	case svc.Running:
		return "работает", nil
	case svc.Stopped:
		return "остановлена", nil
	case svc.StartPending:
		return "запускается…", nil
	case svc.StopPending:
		return "останавливается…", nil
	default:
		return "переходное состояние", nil
	}
}

// trayImage draws a 16x16 red beacon on a dark background — the "siren".
func trayImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	bg := color.RGBA{R: 30, G: 34, B: 40, A: 255}
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, bg)
		}
	}
	red := color.RGBA{R: 224, G: 60, B: 49, A: 255}
	for y := 3; y <= 11; y++ {
		for x := 3; x <= 11; x++ {
			dx, dy := x-7, y-7
			if dx*dx+dy*dy <= 16 {
				img.Set(x, y, red)
			}
		}
	}
	return img
}
