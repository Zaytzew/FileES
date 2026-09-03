# Autostart pary desktopowej na Windows

Instalacja produkcyjna właściciela, założona 2026-09-03. Skrypty tu leżą, bo
przez cały poprzedni dzień istniały **wyłącznie** na jego dysku — a to znaczy,
że nikt poza sesją, która je napisała, nie wiedział ani że istnieją, ani jak
działają.

## Co jest zainstalowane

Katalog produkcyjny: `%LOCALAPPDATA%\Programs\FileES\`

| plik | rola |
|---|---|
| `config.json` | **konfiguracja produkcyjna** — demon czyta ją z katalogu roboczego |
| `filees.exe`, `filees-gui-wails.exe` | para, zbudowana przez `packaging/build-pair.sh` |
| `start-filees.vbs` | shim uruchamiający, kopia `autostart-launch.vbs` |
| `start-filees.ps1` | nadzorca, kopia `autostart-supervisor.ps1` |
| `logs/` | dziennik demona i nadzorcy |

Zadanie Harmonogramu nazywa się **`FileES`**, wyzwalacz „przy logowaniu",
akcja `wscript.exe "…\start-filees.vbs"`, bez limitu czasu wykonania.

## Trzy decyzje, które wyglądają na drobiazgi i nie są

**Dlaczego `wscript`, a nie `powershell` wprost.** Harmonogram uruchamiający
`powershell.exe -WindowStyle Hidden` **i tak zostawia konsolę na ekranie** —
host konsoli powstaje, zanim styl zdąży zadziałać. `WScript.Shell.Run` z
`intWindowStyle = 0` nie tworzy jej wcale. Właściciel zobaczył to okno w
minutę po pierwszym wdrożeniu.

**Dlaczego demon jest wskrzeszany, a interfejs nie.** Demon jest usługą i musi
chodzić. Zamknięcie okna interfejsu to decyzja użytkownika, a nadzorca, który
otwierałby je z powrotem, kłóciłby się z człowiekiem. Interfejs startuje raz,
przy starcie nadzorcy.

**Dlaczego log demona ma w nazwie godzinę, nie samą datę.**
`-RedirectStandardError` **obcina** plik, a nie dopisuje. Przy nazwie dziennej
restart po awarii nadpisałby log tej właśnie awarii — czyli jedyny plik, po
który ktokolwiek by sięgnął. Jeden plik na uruchomienie.

## Czego tu świadomie NIE ma

Nadzorca nie ma limitu prób restartu. Ta maszyna jest żywym testem, a demon,
który poddałby się po N próbach, zostawiłby pracę właściciela bez opieki
**po cichu** — czyli dokładnie ten kształt awarii, przeciwko któremu ten
produkt istnieje.

Zatrzymanie demona odbywa się dziś przez `Stop-Process`, czyli twardo. Windows
nie ma odpowiednika SIGTERM do dostarczenia, a `filees` nie ma podkomendy
wyłączającej — jedyna łagodna droga to `system.shutdown` po IPC, którą umie
interfejs. **To ma znaczenie:** manifest obserwatora zapisuje się wyłącznie
przy łagodnym zamknięciu, więc każde twarde ubicie kosztuje ponowne wykrycie
całego drzewa przy starcie. Opisane szerzej w `messages-to-brothers/claude.md`,
wpis z nocy 2026-09-03.

## Kolejność przy podmianie binarek

Nadzorca wskrzesza demona w ciągu piętnastu sekund, więc samo `filees shutdown`
nie wystarczy — demon wróci, zanim zdążysz podmienić plik.

```powershell
# 1. zatrzymaj nadzorcę (inaczej wskrzesi demona w trakcie podmiany)
Stop-ScheduledTask -TaskName FileES
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" |
  Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -like '*start-filees.ps1*' } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force }

# 2. zatrzymaj demona ŁAGODNIE — inaczej tracisz manifest obserwatora
& "$env:LOCALAPPDATA\Programs\FileES\filees.exe" shutdown

# 3. podmień binarki, 4. wystartuj zadanie
Start-ScheduledTask -TaskName FileES
```

**Dlaczego krok drugi nie jest kosmetyką.** Manifest obserwatora zapisuje się
przy zamknięciu cyklu skanowania i — od r807 — po każdej potwierdzonej
publikacji. `Stop-Process` nie daje ani jednego, ani drugiego. Zmierzone
2026-09-04: manifesty stały na 15:21 przez kilkanaście restartów; po jednym
`filees shutdown` dostały datę bieżącą, a kolejny start nie wykrył **żadnej**
zmiany — czyli fantomowa kolejka, o którą pytał właściciel, zniknęła.
