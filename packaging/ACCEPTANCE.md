# FileES GUI — archiwalna checklista starego renderera

**Status: deprecated / abandoned. Nie używać do odbioru bieżącego
wydania.** Dokument dotyczy wyłącznie historycznego `cmd/filees-gui`
(Fyne+zenity/yad na Linuksie, WinForms/PowerShell na Windows). Bieżącym i
jedynym rozwijanym GUI jest `cmd/filees-gui-wails`; jego odbiór oraz
zastąpienie starego `packaging/build-gui.sh` są osobnym, otwartym zadaniem
packagingowym opisanym w `concepts/WAILS_GUI_FORK.md` §5/§7.

Poniższe punkty zachowano wyłącznie jako zapis historycznych kryteriów.

## Linux

- [ ] Zweryfikować `sha256sum -c SHA256SUMS`, uruchomić `install-user.sh` z bundle i potwierdzić instalację `~/.local/bin/filees` oraz `~/.local/bin/filees-gui`.
- [ ] Potwierdzić, że instalator tworzy nowy `~/.config/filees/config.json` jako `0600`, lecz nigdy nie nadpisuje istniejącego pliku przy upgrade.
- [ ] Wykonać `filees config-check --config ~/.config/filees/config.json` przed uruchomieniem usługi.
- [ ] Wykonać `systemctl --user enable --now filees.service`, sprawdzić status, kontrolowany restart i shutdown mieszczący się w `TimeoutStopSec=15min`.
- [ ] Wykonać reinstall aktywnej wersji i potwierdzić kontrolowany restart tylko działającej usługi.
- [ ] Odinstalować klienta z aktywną usługą; potwierdzić stop/disable, usunięcie binariów i unit oraz zachowanie konfiguracji, tożsamości i synchronizowanych danych.
- [ ] Uruchomić `~/.local/bin/filees-gui` w sesji użytkownika.
- [ ] Potwierdzić ikonę oraz menu na KDE/XFCE/MATE lub innym pulpicie z SNI.
- [ ] Na GNOME potwierdzić działanie z rozszerzeniem AppIndicator/KStatusNotifierItem i udokumentowany brak ikony bez rozszerzenia.
- [ ] Sprawdzić `xdg-open`, picker Zenity oraz fallback KDialog.
- [ ] Sprawdzić grupowanie i ograniczanie `notify-send`.
- [ ] Wykonać `ENABLE_AUTOSTART=1 ./install-user.sh` lub `filees-gui --autostart enable`, ponownie zalogować użytkownika i potwierdzić pojedynczą instancję GUI.
- [ ] Wykonać `filees-gui --autostart disable` i potwierdzić brak startu po kolejnym logowaniu.

## Windows 10/11

- [ ] Zbudować MSI przez `build-msi.ps1`, zainstalować per-user bez monitu UAC i potwierdzić obecność skrótu Start Menu.
- [ ] Sprawdzić, że skrót ma `System.AppUserModel.ID = ATMProjekt.FileES` i uruchamia właściwy executable.
- [ ] Potwierdzić ikonę tray, menu wielu repozytoriów i brak okna konsoli.
- [ ] Sprawdzić Explorer, wielokrotny picker WinForms i anulowanie pickera.
- [ ] Potwierdzić toast `ToastGeneric` podpisany jako FileES oraz grupowanie kolejnych powiadomień.
- [ ] Wykonać `filees-gui --autostart enable`, ponownie zalogować użytkownika i potwierdzić pojedynczą instancję GUI.
- [ ] Przed odinstalowaniem wykonać `filees-gui --autostart disable`; następnie odinstalować MSI i potwierdzić usunięcie executable oraz skrótu. Automatyczne usunięcie pojedynczej wartości z `HKCU\...\Run` pozostaje otwartą bramką instalatora.

## Obie platformy

- [ ] Uruchomić GUI bez daemona, następnie daemon; potwierdzić automatyczne połączenie.
- [ ] Zrestartować daemon przy otwartym GUI; potwierdzić stan disconnected, reconnect i świeży snapshot.
- [ ] Sprawdzić co najmniej dwa repozytoria, eventy, lukę sekwencji i pełny resync.
- [ ] Sprawdzić lock/unlock, błąd operacji i wybór ścieżki spoza repozytorium.
- [ ] Użyć „Uruchom FileES ponownie…” i potwierdzić kontrolowany restart daemona oraz GUI.
- [ ] Użyć „Zamknij FileES…” i potwierdzić, że daemon, watchery oraz GUI pozostają wyłączone.
