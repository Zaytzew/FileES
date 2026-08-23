# filees-gui-wails

Eksperymentalny renderer GUI oparty na Wails v3. Nie zastępuje obecnego
`filees-gui` ani demona i nie ma własnej logiki repozytoriów.

Granica jest taka sama jak w GUI Fyne:

```text
WebView/CSS -> GUIService -> internal/gui/app -> pkg/ipcclient -> daemon
```

Pierwszy pion renderuje pełną projekcję stanu, reaguje na zdarzenia IPC oraz
przekazuje dwie techniczne intencje: `Refresh` i `Reconnect`. Następne akcje
powinny być dokładane wyłącznie jako tłumaczenie gestu WebView na istniejące
komendy IPC. Frontend jest celowo bez Node/Vite, aby test UX nie uzależniał
na starcie repozytorium od drugiego toolchainu.

Wymagania:

- Go 1.25 lub nowszy;
- Wails v3.0.0-beta.6;
- WebView2 Runtime na Windows;
- GTK4 i WebKitGTK 6.0 na Linux (wariant GTK3 jest osobnym build tagiem Wails).

Bindings po zmianie publicznej metody lub modelu usługi:

```powershell
wails3 generate bindings -b -d cmd/filees-gui-wails/frontend/bindings ./cmd/filees-gui-wails
```

Build testowy na Windows:

```powershell
go build -tags production -o tmp/filees-gui-wails.exe ./cmd/filees-gui-wails
```
