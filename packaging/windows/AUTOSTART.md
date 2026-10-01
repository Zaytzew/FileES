# Autostart pary desktopowej na Windows

## Bieżący kontrakt MSI — 2026-10-01

Skróty uruchamiają `filees-launch.exe`: bez argumentów przy logowaniu,
`--show` przy kliknięciu. Launcher i długowieczny supervisor są napisane
w Go; normalny start nie uruchamia PowerShell ani VBScript. Zachowuje mutex
sesji, wybór kanału/config, odmowę startu obok Store i adopcję własnego demona
po aktualizacji. Błąd startu/awaria są ponawiane co 15 sekund; wyjście demona
z kodem 0 bez zastępcy kończy supervisor. Zamknięcie GUI nie kończy demona.
Jawne usuwanie poprzedniej aplikacji Store może nadal używać PowerShell;
nie jest to część zwykłego startu MSI.

`start-filees.ps1` i `.vbs` są wyłącznie adapterami starych skrótów do
Go launchera. Nie mają pętli restartu. Konfiguracja istniejąca nie jest
przepisywana. Anulowany pierwszy wybór kanału nie zapisuje configu.
Logi: `logs/supervisor.log` i osobny `daemon-<czas UTC>.log` na każdy start.

Pierwsze przejście wymaga nowego MSI, nie starego self-update. Przed
instalacją zamknąć GUI i łagodnie zakończyć demona przez `filees shutdown`,
zaczekać na zakończenie demona oraz starego supervisora. Nie usuwać configu,
manifestów, intentów ani lokalnych kopii. Po instalacji otworzyć nowy skrót.
Odbiór na działającej instalacji wymaga oddzielnej zgody; izolowane testy
opisuje `implementation notes (not distributed)`.

## Historia — poniższe instrukcje nie opisują nowego MSI

Instalacja produkcyjna właściciela, założona 2026-09-03. Skrypty tu leżą, bo
przez cały poprzedni dzień istniały **wyłącznie** na jego dysku — a to znaczy,
że nikt poza sesją, która je napisała, nie wiedział ani że istnieją, ani jak
działają.

## Korekta startu i zatrzymania — źródła 2026-09-20

Start Menu i pulpit uruchamiają `wscript start-filees.vbs --show`; autostart
używa tego samego launchera bez `--show`. Własność nadzorcy chroni mutex sesji.
Kolejne kliknięcie podnosi istniejące Wails przez jego single-instance handler.
Jeśli demon już zakończył pracę, kliknięcie czeka na zwolnienie mutexu przez
starego nadzorcę i uruchamia pełną parę. Nadzorca zachowuje uchwyt procesu,
adoptuje zastępcę po restarcie i ponawia awarię; zakończenie z kodem 0 bez
zastępcy kończy też nadzorcę. Zamknięcie samego okna GUI nie wyłącza demona.
Store również kończy nadzorcę po poprawnym zakończeniu procesu potomnego.

To zmiana źródeł, bez MSI/bundla ani odbioru Windows (host niedostępny).
Dotychczas zainstalowane skróty zmieni dopiero upgrade/repair MSI, a nowy
skrypt zacznie obowiązywać po ponownym uruchomieniu nadzorcy. Instrukcje
poniżej opisują starszą instalację; dla niej ręczne zatrzymanie nadzorcy
przed podmianą nadal obowiązuje. Cross-build nie dowodzi zachowania COM,
PowerShell, mutexu ani skrótów na Windows.

## Bez VBScriptu — `filees-launch.exe`, 2026-09-24

Czysty Windows (Windows Sandbox) odpowiedział na skrót „Brak aparatu skryptów
dla rozszerzenia .vbs” i FileES się nie uruchomił: Microsoft wycofuje
VBScript i nowe instalacje coraz częściej go nie mają. Od tej zmiany skróty
Start, pulpitu i autostartu MSI wskazują `filees-launch.exe` (`--show` dla
interaktywnych). To program okienkowy (`-H=windowsgui`), który uruchamia
`start-filees.ps1` przez `powershell.exe` z `CREATE_NO_WINDOW` i nie czeka —
ta sama własność, dla której istniał shim: brak jakiegokolwiek okna konsoli.

`start-filees.vbs` nadal jest w paczce i w MSI: instalacje ze skrótami
utworzonymi przez starsze MSI wskazują wscript do najbliższego upgrade MSI,
a samoaktualizacja utrzymuje ten plik. Opis niżej, o `wscript`, dotyczy tych
starszych skrótów.
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

Nadzorca zatrzymuje demona tylko pośrednio — sam go nie ubija. Do zatrzymania
z zewnątrz służy `filees shutdown` (r812), które przechodzi po IPC i daje
demonowi domknąć cykl skanowania. Windows nie ma SIGTERM do dostarczenia, więc
**to jest jedyna łagodna droga z wiersza poleceń**; `Stop-Process` nią nie jest
i kosztuje manifest obserwatora. Kolejność przy podmianie binarek niżej.

## Historyczna kolejność przy podmianie binarek (sprzed korekty wyjścia)

Nadzorca wskrzesza demona w ciągu piętnastu sekund, więc samo `filees shutdown`
nie wystarczy — demon wróci, zanim zdążysz podmienić plik.

```powershell
# 1. zatrzymaj nadzorcę (inaczej wskrzesi demona w trakcie podmiany)
#    UWAGA na filtr: własna linia poleceń też zawiera "start-filees.ps1",
#    więc bez -ne $PID zabijasz własną powłokę. Sprawdzone na sobie, kod 255.
#    Dlatego wzorzec jest na "-File", a nie na samą nazwę skryptu.
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" |
  Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -like '*-File*start-filees.ps1*' } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force }

# 2. zatrzymaj demona ŁAGODNIE — inaczej tracisz manifest obserwatora.
#    UWAGA: wołaj ŚWIEŻO ZBUDOWANĄ binarkę, nie zainstalowaną. To wywołanie
#    po IPC, więc wyda je dowolny klient — a zainstalowana może być starsza
#    niż sama podkomenda i wtedy cicho nic nie zrobi. Sprawdzone na sobie:
#    stara binarka odpowiedziała "unknown command", demon został, a podmiana
#    pliku padła na "plik w użyciu" i wdrożenie wyglądało na udane.
& "E:\!!!_COOPERATE\FILEES\dist\filees.exe" shutdown

# 2b. interfejs trzyma własny plik; jego zatrzymanie nie ma stanu do stracenia
Get-Process filees-gui-wails -ErrorAction SilentlyContinue | Stop-Process -Force

# 3. podmień binarki, 4. wystartuj nadzorcę
#    Przez wscript, nie przez powershell wprost - patrz wyżej, konsola.
#    Zadania harmonogramu tu już nie ma: autostart robi skrót w Startup,
#    zakładany przez MSI.
Start-Process "C:\Windows\System32\wscript.exe" `
  -ArgumentList "`"$env:LOCALAPPDATA\Programs\FileES\start-filees.vbs`""
```

**Dlaczego krok drugi nie jest kosmetyką.** Manifest obserwatora zapisuje się
przy zamknięciu cyklu skanowania i — od r807 — po każdej potwierdzonej
publikacji. `Stop-Process` nie daje ani jednego, ani drugiego. Zmierzone
2026-09-04: manifesty stały na 15:21 przez kilkanaście restartów; po jednym
`filees shutdown` dostały datę bieżącą, a kolejny start nie wykrył **żadnej**
zmiany — czyli fantomowa kolejka, o którą pytał właściciel, zniknęła.
