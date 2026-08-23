# filees-gui-wails

Eksperymentalny renderer GUI oparty na Wails v3. Nie zastępuje obecnego
`filees-gui` ani demona i nie ma własnej logiki repozytoriów.

Granica jest taka sama jak w GUI Fyne:

```text
WebView/CSS -> GUIService -> internal/gui/app -> pkg/ipcclient -> daemon
```

Pierwszy pion renderuje pełną projekcję stanu, reaguje na zdarzenia IPC oraz
przekazuje techniczne intencje `Refresh` i `Reconnect`. Pierwszy pion akcyjny
udostępnia `Otwórz`, `Zablokuj` i `Zwolnij` tylko wtedy, gdy bieżąca projekcja
repozytorium na to pozwala. `GUIService.Trigger` tłumaczy gest WebView na
zamknięty zbiór istniejących intencji, a wspólny `internal/gui/actions`
ponownie sprawdza stan i jako jedyny woła IPC. Frontend jest celowo bez
Node/Vite, aby test UX nie uzależniał na starcie repozytorium od drugiego
toolchainu.

Na Windows okno jest bez natywnej ramki (`Frameless`) i ma własny pasek z
przeciąganiem oraz kontrolkami okna. CSS ukrywa scrollbar WebView, ale nie
wyłącza przewijania kółkiem, touchpadem ani klawiaturą.

Pełna projekcja zawiera także aktywne rezerwacje plikowe. Renderer dostaje
opaque identyfikator wiersza, opis i capability zwolnienia, ale nigdy token
fencingowy SVN. Przycisk `Zwolnij` wraca do wspólnego kontrolera, który po
potwierdzeniu ponownie rozwiązuje identyfikator i przekazuje token demonowi.

Proces tworzy własny tray Wails z ikoną bieżącego stanu, liczbą repozytoriów i
blokad. Pozycja `Pokaż panel` przywraca okno, a jego zamknięcie ukrywa je do
traya. Podmenu `FileES` zawiera kontrolowany restart oraz `Zakończ…`. Obie
akcje przechodzą przez wspólny kontroler i IPC demona, wymagają potwierdzenia
z możliwością `Anuluj` i dotyczą pary daemon + GUI, a nie samego renderera.

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
go build -tags production -ldflags '-H=windowsgui' -o tmp/filees-gui-wails.exe ./cmd/filees-gui-wails
```
