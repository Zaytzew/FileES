# FileES GUI — checklista odbioru natywnego

Automatyczne testy potwierdzają kontrakt, lifecycle, reconnect, bundle i metadane. Poniższe punkty wymagają uruchomienia artefaktu w rzeczywistej sesji graficznej przed wydaniem.

## Linux

- [ ] Uruchomić `install-user.sh` z bundle i następnie `~/.local/bin/filees-gui` w sesji użytkownika.
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
- [ ] Odinstalować MSI i potwierdzić usunięcie executable oraz skrótu; ustawienie autostartu wyłączyć przed odinstalowaniem.

## Obie platformy

- [ ] Uruchomić GUI bez daemona, następnie daemon; potwierdzić automatyczne połączenie.
- [ ] Zrestartować daemon przy otwartym GUI; potwierdzić stan disconnected, reconnect i świeży snapshot.
- [ ] Sprawdzić co najmniej dwa repozytoria, eventy, lukę sekwencji i pełny resync.
- [ ] Sprawdzić lock/unlock, błąd operacji i wybór ścieżki spoza repozytorium.
- [ ] Zamknąć GUI z menu i potwierdzić, że daemon oraz synchronizacja nadal działają.
