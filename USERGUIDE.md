# FileES GUI — podręcznik użytkownika

**Wersja:** 0.1.0  
**Dotyczy:** `filees-gui-wails` 0.1.0; daemon `filees` opisany jest osobno w README.md. Pełna instrukcja systemu jest dwujęzyczna: `manual/index.html` to przełącznik PL/EN, rozdziały są w `manual/assets/pl/` i `manual/assets/en/`. Na `manual.filees.space` htdocs to kopia `svn://cloud.atmprojekt.pl/SYNCSHARE/manual`. Strony `mandoc` serwera są w `docs/man/` (angielski).

---

## Czym jest FileES GUI

`filees-gui-wails` to pełny panel desktopowy z własnym trayem. Pokazuje żywą
projekcję serwerów, repozytoriów, kolejki, blokad, ogłoszeń, udziałów
publicznych, kwarantanny i aktualizacji oraz prowadzi wszystkie operacje bez
otwierania terminala. Demon `filees` pozostaje źródłem prawdy i odpowiada za
synchronizację. Zamknięcie okna chowa je do traya; dopiero jawne **Zakończ
FileES** zatrzymuje całą parę.

Stary `cmd/filees-gui` oparty na Fyne+zenity/yad/WinForms jest
**deprecated / abandoned**. Jego obecność w źródłach i starym packagingu jest
historią wdrożenia, nie alternatywną instrukcją użytkownika.

---

## Wymagania

### Linux
- Pulpit z obsługą SNI (GNOME wymaga rozszerzenia **AppIndicator/KStatusNotifierItem**; KDE, XFCE, MATE działają bez dodatków)
- GTK4 i WebKitGTK 6.0 dla bieżącego buildu Wails
- `xdg-open` — do otwierania katalogów
- `notify-send` — opcjonalnie, do powiadomień systemowych; brak narzędzia nie zatrzymuje GUI

### Windows
- Windows 11 z WebView2 Runtime
- PowerShell — do wyboru plików i powiadomień toast
- docelowo kompletne MSI klienta; bieżące źródła Wails są gotowe, a przepięcie
  oficjalnego packagingu Windows pozostaje osobnym zadaniem wydaniowym

---

## Instalacja

### Linux

Bieżący klient alfa jest budowany z `cmd/filees-gui-wails` i instalowany jako
para z demonem. Stary `packaging/build-gui.sh` nadal buduje porzucony renderer
Fyne i nie jest źródłem aktualnego wydania. Podpisany updater Linux jest
dostępny z popupu wersji w panelu Wails.

### Windows

Docelowy artefakt to per-user MSI zawierające demon i Wails, bez UAC. Obecne
`packaging/windows/` opisuje jeszcze stary, niekompletny artefakt GUI-only i
nie jest wydaniem Wails; plan migracji jest w
`concepts/WINDOWS_CLIENT_MSI_ALPHA_CONCEPT.md`.

---

## Uruchamianie

```bash
filees-gui-wails                           # łączy z domyślnym socketem demona
filees-gui-wails --socket /ścieżka/daemon.sock  # niestandardowy socket
filees-gui-wails --version                 # pokazuje wersję GUI i kończy działanie
```

GUI można uruchomić przed daemonem — będzie czekać na połączenie z narastającym interwałem (1 s → 2 s → 5 s → 10 s → 30 s).

GUI chroni się przed wielokrotnym uruchomieniem w obrębie sesji użytkownika. Druga instancja kończy się przed utworzeniem ikony tray — także wtedy, gdy podano jej inny socket.

---

## Autostart

Autostart jest własnością instalatora bieżącego klienta, nie osobnym trybem
Wails. Na Linuksie uruchamia parę demon+GUI w sesji użytkownika. Docelowy MSI
Windows ma instalować i usuwać wpis autostartu razem z kompletnym klientem.
Polecenia `filees-gui --autostart ...` należały do porzuconego renderera i nie
są instrukcją dla `filees-gui-wails`.

---

## Ikona tray — znaczenie stanów

| Ikona | Stan | Znaczenie |
|-------|------|-----------|
| zielona | **Aktywne** | Wszystkie repozytoria zsynchronizowane i online |
| pomarańczowa | **Praca w toku** | Trwa commit, aktualizacja, inicjalizacja lub baselining |
| szara | **Offline** | Co najmniej jedno repozytorium nie ma połączenia z serwerem |
| czerwona | **Wymaga uwagi** | Konflikt, degradacja lub błąd wymagający działania użytkownika |
| przyciemniona/brak | **Brak połączenia** | GUI nie może połączyć się z daemonem |

Gdy jest wiele repozytoriów, ikona pokazuje najpoważniejszy stan spośród wszystkich. Repozytorium zdrowe nie maskuje problemu w innym.

---

## Menu tray

Kliknięcie ikony otwiera menu. Układ jest stały; elementy wyszarzone to informacje (nie są klikalne).

### Nagłówek

```
FileES — Połączono          ← stan połączenia (wyszarzony)
Ostatnia aktualizacja: 14:32:01   ← czas ostatniego odświeżenia
─────────────────────────
```

Możliwe etykiety stanu:
- **Połączono** — aktywna sesja z daemonem
- **Odświeżanie** — sesja aktywna, dane właśnie odświeżane po reconnect (stan przejściowy)
- **Brak połączenia** — demon nieosiągalny; dane z poprzedniej sesji mogą być widoczne jako nieaktualne

Gdy dane są nieaktualne, czas odświeżenia uzupełniany jest dopiskiem *(dane nieaktualne)*.

### Lista repozytoriów

Każde repozytorium jest podmenu z nazwą i bieżącym stanem:

```
▶ projektA — Aktywne
    Stan: Aktywne
    Rewizja: 1042 / 1042
    Oczekujące zmiany: 3
    ─────────────────────
    Otwórz katalog
    Zablokuj pliki…
    Odblokuj pliki…
```

Gdy brak repozytoriów, w miejscu listy pojawia się wpis *Brak repozytoriów*.

### Dziennik

Sekcja jest widoczna, gdy daemon udostępnia aktywność albo błędy. Łączy oba
rodzaje wpisów od najnowszego, zamiast dzielić je na dwie rzadko kompletne
listy:

```
▶ Dziennik · ⚠ 1
    przed chwilą · ⚠ BŁĄD · Dokumenty — [LOCK-2001] Plik jest zablokowany
    4 minuty temu · Dokumenty / projekt.dwg — opublikowano · r1042
    Otwórz log…
```

Panel pokazuje najnowsze zagregowane wpisy. Wiele ścieżek tej samej
rewizji lub etapu może zostać złączonych, a nieudana aktywność nie dubluje
powiązanego błędu. Wpisy są informacyjne; tooltip zawiera szczegóły lub
wskazówkę. **Dziennik** otwiera cały dostępny snapshot w oknie Wails.
Lista używa czasu względnego („przed chwilą”, minuty, godzina, „wczoraj”, dni),
a pełny widok dokładnego `dd:mm:yy hh:mm`. Na Windows błędy są dodatkowo
pogrubione i ciemnoczerwone. Powtarzane `NET-4007` jednego repozytorium są
jednym incydentem; awaria naprawiona automatycznie przed upływem 45 sekund
pozostaje wyłącznie w ikonie stanu i surowym logu diagnostycznym.

### Akcje globalne

```
─────────────────────────
Połącz ponownie
Wersja 0.1.0 / aktualizacja klienta
Uruchom FileES ponownie…
Zamknij FileES…
```

- **Połącz ponownie** — wymusza natychmiastową próbę połączenia z daemonem, pomijając czas oczekiwania backoffu. Działa zarówno przy braku połączenia, jak i w trakcie sesji (resetuje sesję).
- **Wersja 0.1.0 / aktualizacja klienta** — pastylka wersji otwiera informacje
  o kliencie i wydaniu dystrybucyjnym. Gdy daemon udostępnia podpisany kanał
  aktualizacji, to samo okno pokazuje plan oraz akcję aktualizacji.
- **Uruchom FileES ponownie…** — kontrolowanie opróżnia kolejkę i zatrzymuje
  runtime, po czym uruchamia ponownie daemon i GUI.
- **Zamknij FileES…** — kontrolowanie zatrzymuje cały stack kliencki: daemon,
  watchery i GUI.

---

## Wydanie (shouting commit)

**Opublikuj wydanie…** w podmenu folderu wysyła bieżące, jeszcze niezacommitowane zmiany z komentarzem. To nie jest zwykły autocommit: komentarz ląduje w `svn:log` i po aktualizacji inni członkowie zespołu widzą badge **Wydanie: …**. Kliknięcie oznacza „przeczytałem”. FileES nie jest komunikatorem — badge pojawia się dopiero po udanym `svn update` na danej maszynie.

Pusta lista zmian kończy się polskim oknem „Brak zmian do opublikowania”, nie pustym commitem i nie angielskim komunikatem SVN. Udany zapis pokazuje okno z numerem rewizji. Lokalne `shout_patterns` w `config.json` to coś innego: tylko podpowiedź we własnym traju.

---

## Stany repozytorium

| Etykieta w menu | Znaczenie |
|-----------------|-----------|
| Aktywne | Repozytorium zsynchronizowane, operacje normalne |
| Praca w toku | Trwa commit lub aktualizacja |
| Inicjalizacja | Pierwsze uruchomienie — tworzenie kopii roboczej |
| Budowanie stanu bazowego | Generowanie stanu bazowego do śledzenia zmian |
| Wstrzymane | Synchronizacja chwilowo wstrzymana |
| Zatrzymywanie | Repozytorium jest wyłączane |
| Offline | Brak połączenia z serwerem SVN |
| Wymaga uwagi | Konflikt, degradacja lub błąd wymagający działania |
| Stan nieznany | Nierozpoznany stan — demon ma nowszy protokół |

**Rewizja** — para `lokalna / HEAD`. Gdy są równe, kopia robocza jest aktualna. Różnica oznacza trwającą lub oczekującą aktualizację.

**Oczekujące zmiany** — liczba plików zmodyfikowanych lokalnie, czekających na commit.

---

## Aktywacja i wznowienie przerwanej aktywacji

Wybierz **Aktywuj klienta na nowym serwerze…**, wklej zaproszenie otrzymane
e-mailem, a następnie osobno dostarczony OTP. Adres, port i przypięty klucz
hosta pochodzą z podpisanego zaproszenia — użytkownik ich nie przepisuje.

Jeśli GUI zostało zamknięte po przyjęciu zaproszenia, następne wybranie tej
akcji najpierw pokaże **Niedokończona aktywacja FileES**. Wybierz **Wznów**.
Gdy OTP został już zużyty, demon odtworzy połączenie trwałym kluczem reconnect;
gdy poprzednia próba zatrzymała się wcześniej, GUI poprosi o OTP dla tej samej
zapisanej próby. Nie usuwaj ręcznie katalogu stanu i nie zamawiaj drugiego
ticketu tylko z powodu zamknięcia okna.

Wybranie **Inne zaproszenie** pozwala wkleić nowy, serwerowo podpisany ticket.
Może on zastąpić niedokończoną próbę, ale nigdy działający profil klienta.
Błąd jest pokazywany w oknie modalnym oraz w Dzienniku; brak kolejnego okna nie
jest traktowany jako sukces.

---

## Dodawanie lokalnego folderu jako nowego repozytorium

W ustawieniach serwera wybierz **Dodaj pierwszy/kolejny folder do FileES…**, wskaż
istniejący folder i potwierdź jego nazwę. Tworzenie repozytorium oraz pierwszy
commit działają w tle. Dla dużego folderu może to potrwać kilka minut.

Po utworzeniu bytu serwerowego, ale przed końcem pierwszego commitu, Ustawienia
pokazują wybraną ścieżkę i stan **import początkowy w toku**. Nie jest to
repozytorium nieprzypięte: FileES trwale zajmuje ten lokalny root, lecz do czasu
zakończenia importu nie uruchamia synchronizacji ani operacji na plikach.

---

## Dołączenie do istniejącej strefy i wybór repozytoriów

Administrator może wystawić zaproszenie do już istniejącej strefy. Po
zaproszeniu i OTP klient dziedziczy jej tożsamość oraz prawa, lecz nie pobiera
automatycznie wszystkich repozytoriów na dysk.

1. Otwórz trybik wybranego serwera w panelu FileES.
2. Wskaż serwer, potem działanie **Połącz z lokalnym folderem**.
3. Dopiero wtedy widać listę udziałów, które da się podłączyć. Na Windows
   można zaznaczyć kilka; na Linuxie każdy udział osobno albo zaznaczenie
   listy, jeśli FileES je pokaże.
4. Dla każdego repozytorium wskaż albo utwórz lokalny folder.
5. Po akceptacji wiersz natychmiast pokazuje ścieżkę i `łączenie…`. Pierwszy
   checkout działa w tle; końcowy sukces albo błąd pojawi się w powiadomieniu.

Stary, nadal zaznaczony wiersz już podłączonego repozytorium nie blokuje
podłączania kolejnego. Nie wybieraj ponownie tego samego folderu, gdy wiersz ma
stan `łączenie…`.

---

## Uprawnienia gości i udostępnienia publiczne

W **Ustawieniach** (najpierw serwer, potem działanie) właściciel repozytorium ma dwie odrębne akcje:

- **Uprawnienia gości** pokazuje widoczne strefy oraz każdą ukrytą strefę,
  która nadal ma aktywny grant do tego repozytorium. Kolumna „Aktualne
  uprawnienie” pokazuje brak, tylko odczyt albo odczyt i zapis. Można ustawić
  `r`, `rw` lub cofnąć dostęp; każda zmiana wymaga potwierdzenia.
- **Udostępnienia publiczne** pokazuje kanały wydawania plików. Widać adres,
  stan, folder źródłowy, odbiorców, ochronę hasłem oraz `HEAD` albo zamrożoną
  rewizję. Dostępne są **Nowe**, **Edytuj**, **Cofnij** i **Usuń**.
- Kolumna **Edycja** pokazuje, czy repozytorium pracuje zwykle, czy **wymaga
  wypożyczenia**. Właściciel podłączonego repozytorium może użyć akcji
  **Zasady edycji**, aby przełączyć tę politykę dla wszystkich komputerów.
  Gość widzi stan, ale nie może zmienić polityki całego repozytorium.

Tworzenie albo edycja kanału wymaga podłączonej lokalnej kopii roboczej:

1. Wybierz root WC albo dowolny folder wewnątrz niego. FileES zbierze zwykłe
   pliki rekurencyjnie wraz z rozmiarami; `.svn` i `.filees` są pomijane,
   symlinki i ponad 4096 plików są odrzucane. Pusty folder jest poprawnym
   placeholderem; po dodaniu do niego plików trzeba jawnie edytować kanał.
2. Dla nowego kanału wpisz końcówkę adresu (`slug`): 3–64 małe litery, cyfry
   lub pojedyncze myślniki.
3. Opcjonalnie wpisz adresy e-mail oddzielone przecinkami lub średnikami.
   Taki kanał jest zamknięty i każdy odbiorca dostaje osobne zaproszenie. Link
   otwiera neutralną bramkę; po użyciu **Wyślij kod** odbiorca dostaje na tę
   skrzynkę ośmiocyfrowy OTP ważny pięć minut. Puste pole tworzy kanał otwarty.
4. Kanał otwarty może dostać wspólne hasło (co najmniej 8 znaków). Hasło jest
   hashowane lokalnie; plaintext nie trafia do serwera ani dziennika. Przy
   edycji można zachować dotychczasowe hasło bez ponownego wpisywania.
5. Puste pole rewizji śledzi `HEAD`; dodatni numer zamraża kanał na tej rewizji.

Odbiorca widzi pod publicznym adresem domyślnie zwinięte drzewo folderów podobne
do widoku „Szczegóły” w Eksploratorze: nazwę, ikonę typu pliku, opis typu,
rozmiar i przycisk pobrania. Może zaznaczyć kilka plików, pobrać pojedynczy
folder albo cały udział jako ZIP. Publiczna strona używa identyfikacji
`filees:space` (granat/pomarańcz) i nie wykonuje JavaScriptu; nie generuje na
razie miniaturek zdjęć. Kanały utworzone przez starszego klienta pozostają
zgodne, ale mogą pokazywać kreskę zamiast nieznanego rozmiaru do czasu ich
edycji.

FileES nie zapisuje cookies ani danych przeglądarki. Po poprawnym OTP adres
otwartego listingu jest przenośnym kluczem tylko do końca tego samego
pięciominutowego okna. Po wygaśnięciu skopiowany adres ponownie pokazuje bramkę
OTP; ponowne kliknięcie wysyłki w aktywnym oknie nie wydłuża TTL ani nie zmienia
kodu.

Nieprawidłowy adres oraz cofnięty albo usunięty udział pokazują tę samą
obrandowaną stronę „Przestrzeń niedostępna” z kodem HTTP 404. FileES nie
ujawnia na niej nazwy realmu, udziału ani przyczyny niedostępności. Wygaśnięty
adres wizyty, który nadal zawiera aktywne zaproszenie, wraca zamiast tego do
neutralnej bramki OTP opisanej wyżej.

**Cofnij** natychmiast przestaje wydawać pliki, ale zachowuje rekord i adres.
**Usuń** usuwa politykę kanału; adres pozostaje zarezerwowany jako tombstone i
nie może zostać przejęty przez późniejszy kanał.

---

## Półki przyjęcia

**Półki przyjęcia** są akcją wyłącznie właściciela repozytorium projektu.
Półka jest zawsze zamknięta: lista wnoszących nie może być pusta, każdy dostaje
osobne zaproszenie i kładzie plik przeglądarką. Nie ma trybu anonimowego.

1. Wpisz końcówkę publicznego adresu (`slug`): 3–64 małe litery, cyfry lub
   pojedyncze myślniki.
2. Podaj adresy e-mail wnoszących, oddzielone przecinkiem lub średnikiem.
3. Potwierdź **Kod z poczty**, jeśli wnoszący ma przed wysłaniem wpisać
   skrzynkę z zaproszenia i jednorazowy kod (osiem cyfr, pięć minut). Bez
   tej opcji sam link z tokenem wystarcza do wniesienia.

Po utworzeniu FileES pyta o lokalny folder na czyste przyjęcia. Zaakceptowane
pliki lądują w zwykłym repozytorium półki; odrzut AV trafia do **Kwarantanny**
realmu, nie do repozytorium projektu. Kwarantanna jest listą w oknie FileES
(projekcja demona, bez przeglądarki WWW): **Pobierz** zapisuje kopię na dysk
i mówi, ile godzin zostanie reszta, **Odrzuć** ukrywa pozycję w manifeście,
a zamknięcie okna nic nie kasuje. Po 48 godzinach serwer sam usuwa plik.

Publiczny formularz nie pokazuje aliasu, slugu ani adresów. Przy włączonym
kodzie z poczty najpierw jest bramka (e-mail i kod), potem pole pliku.
Nieprawidłowe zaproszenie i brak zaproszenia na kanale bez kodu dają to samo
404. Na kanale z kodem dowolny niepusty token pokazuje tę samą bramkę — strona
nie potwierdza, czy skrzynka jest na liście.

---

## Blokowanie i odblokowanie plików

`Zablokuj pliki…` i `Odblokuj pliki…` są dostępne w podmenu repozytorium,
gdy daemon zgłasza odpowiednie capabilities. W zwykłym repozytorium wykonują
blokadę SVN; w repozytorium oznaczonym w kolumnie **Edycja** jako **wymaga
wypożyczenia** nabywają lub zwalniają edit-passport. Politykę ustawia raz
właściciel repozytorium i otrzymują ją wszystkie podłączone instalacje — nie
konfiguruje się jej osobno na każdym komputerze. Udane wypożyczenie przywraca
plikowi lokalną zapisywalność. Blokada trzymana przez inną osobę pokazuje jej
alias oraz czas wygaśnięcia zamiast kończyć się bez komunikatu.

Operacja dostępna wyłącznie gdy:
- GUI jest połączone z daemonem (**Połączono**, nie **Odświeżanie**)
- Daemon zadeklarował capability `repo.lock` / `repo.unlock`

W stanie **Odświeżanie** akcje blokowania i odblokowania są ukryte. Pojawiają się dopiero po odebraniu świeżego snapshotu i zmianie stanu na **Połączono**.

### Przebieg operacji

1. Kliknąć **Zablokuj pliki…** lub **Odblokuj pliki…** w podmenu repozytorium.
2. Otworzy się systemowy selektor plików. Można wybrać wiele plików.
3. Selektor ogranicza wybór do katalogu repozytorium; pliki spoza niego są odrzucane.
4. Po zatwierdzeniu demon wykonuje operację. Wynik pojawi się jako powiadomienie systemowe:
   - sukces: *Zablokowano N plik(ów)* / *Odblokowano N plik(ów)*
   - błąd: np. *Błąd operacji (lock) — LOCK-2001*, z lokalizowanym opisem i bezpieczną wskazówką dalszego postępowania

Anulowanie selektora (ESC lub przycisk Anuluj) nie wykonuje żadnej operacji i nie pokazuje powiadomienia.

Dla tego samego repozytorium można mieć aktywną tylko jedną operację naraz. Kliknięcie ponownie podczas trwającej operacji jest ignorowane.

## Lista rezerwacji

W nagłówku menu wybierz **Lista rezerwacji plikowych…**. Pozycja jest aktywna,
gdy lokalne kopie robocze mają co najmniej jedną rezerwację, i otwiera jedną
listę blokad SVN ze wszystkich aktywnych serwerów. Wiersz podaje serwer, kopię
roboczą, ścieżkę względną, właściciela i czas `GG:MM DD-MM-RRRR`. Listę można
odświeżyć bez zamykania okna. To nie jest globalny panel administracyjny:
blokady w repozytorium bez lokalnej working copy nie pojawią się na liście.

**Zwolnij** zawsze wymaga potwierdzenia. Gdy folder zawiera lokalne zmiany lub
rezerwacja ma aktywny paszport edycji, pojawia się dodatkowe ostrzeżenie o
możliwych niezapisanych danych. FileES nie sprawdza, czy edytor ma „otwarty
plik”, ponieważ nowoczesne edytory często zapisują przez atomową podmianę i
taki wskaźnik byłby niewiarygodny. Rezerwacja aktywna na innym urządzeniu lub
należąca do innego użytkownika ma działanie **Poproś o zwolnienie**. Requester
widzi stan prośby, a holder odpowiada **OK** albo **Zwolnij**; druga akcja
zwalnia dokładnie tę instancję blokady. **Zwolnij wszystko** wymaga jednego
potwierdzenia i zwalnia wyłącznie moje rezerwacje, każdą z aktualnym tokenem.
Po zatwierdzeniu daemon ponownie sprawdza blokadę i jej token, więc odświeżony
lub podmieniony wiersz nie zwolni przypadkowo innej rezerwacji.

### Stały alias

Alias jest publiczną nazwą realmu widoczną przy rezerwacjach; nie jest adresem
e-mail ani UID. **Ustaw stały alias…** pojawia się tylko dla świeżej, pustej
strefy. Klient dołączony do istniejącej strefy dziedziczy jej alias i nie
powinien być pytany o nową nazwę. Jeżeli repozytoria są już widoczne, a aliasu
brakuje, jest to niepełna projekcja serwera — nie próbuj tworzyć innego aliasu.
Do czasu odświeżenia FileES ukrywa **Widoczność…** i operacje lock wymagające
tożsamości strefy.

---

## Otwieranie katalogu

**Otwórz katalog** otwiera lokalny katalog repozytorium w domyślnym menedżerze plików (Linux: `xdg-open`, Windows: Explorer). Opcja jest wyszarzona, jeśli demon nie podał lokalnej ścieżki dla tego repozytorium.

---

## Przeniesiona albo brakująca kopia robocza

Nie zmieniaj nazwy ani nie usuwaj rootu aktywnej kopii poza FileES. Na Windows
działający daemon blokuje taki rename/delete uchwytem systemowym. Po zamknięciu
FileES system nie może już tego wymusić, dlatego następny start sprawdza `.svn`,
URL repozytorium i marker tożsamości.

Jeżeli poprawna kopia została przeniesiona:

1. Repozytorium przejdzie do **Wymaga uwagi** z operacją
   `working_copy_missing`; FileES nie utworzy pustego zamiennika.
2. Otwórz ustawienia folderu przy właściwym serwerze i wybierz **Wskaż przeniesioną kopię roboczą**.
3. Wskaż istniejący root z `.svn`. FileES sprawdzi URL i tożsamość, a następnie
   trwale przepnie lokalną ścieżkę bez checkoutu.

Lokalne, niecommitowane zmiany są dozwolone i pozostają nietknięte. Nie używaj
**Połącz** do odzyskania przeniesionej WC — ta akcja oznacza pierwszy checkout
repozytorium, które nie ma jeszcze lokalnej kopii na tym kliencie.

---

## Odłączanie folderu

W podmenu opcjonalnie podłączonego repozytorium są dwie różne operacje:

- **Odłącz folder…** — po jednym potwierdzeniu zatrzymuje synchronizację tej
  working copy. FileES usuwa z rootu wyłącznie katalogi `.svn` i `.filees`.
  Wszystkie dokumenty i pozostałe katalogi zostają na dysku; wynik jest
  zwykłym folderem. Operacja działa również wtedy, gdy samo repo jest offline;
  trwały tombstone zapobiega ponownemu podłączeniu starego wpisu z
  `config.json` po restarcie.
- **Odłącz trwale…** — jest dostępne tylko dla repozytorium własnego realmu i
  wymaga dwóch oddzielnych potwierdzeń. Serwer wycofuje dostęp i usuwa FSFS.
  Przy retencji większej od zera najpierw tworzy oraz weryfikuje pełny dump,
  usuwa FSFS natychmiast i zachowuje wyłącznie dump przez skonfigurowaną liczbę
  dni. Retencja `0` to tryb panic: natychmiastowe usunięcie bez dumpa i bez
  serwerowej kopii odzyskowej.

Repozytoriów oznaczonych jako **Wymagane przez serwer** nie można odłączyć ani
usunąć tą ścieżką. Odłączenie trwa do końcowego potwierdzenia daemona; nie
zamykaj FileES w trakcie operacji.

Po odłączeniu zachowany katalog jest zwykłym folderem, a nie kopią roboczą.
Ponowne **Połącz** wykonuje pierwszy checkout do nowego lub pustego folderu;
nie scala automatycznie zachowanych plików z repozytorium. Odrzucenie wybranego
celu albo konflikt lokalnego lifecycle jest pokazywany w modalnym oknie błędu,
nie tylko w powiadomieniu Windows.

---

## Restart i zamykanie

**Uruchom FileES ponownie…** i **Zamknij FileES…** obejmują cały stack
kliencki, nie tylko ikonę. Przed zakończeniem daemon zatrzymuje aktywne
repozytoria i wykonuje końcowy scan/flush.

Pliki zmienione, gdy FileES jest wyłączony, nie przepadają: po następnym
uruchomieniu watcher ładuje trwały manifest, porównuje go z bieżącym
filesystemem i traktuje różnice jako zwykłe dodania, modyfikacje lub usunięcia.

---

## Rozwiązywanie problemów

### Ikona nie pojawia się (GNOME)

GNOME nie obsługuje SNI natywnie. Zainstaluj rozszerzenie **AppIndicator and KStatusNotifierItem Support** (dostępne na extensions.gnome.org). Po instalacji wyloguj się i zaloguj ponownie.

### Brak połączenia — demon działa

Sprawdź, czy `filees-gui-wails` używa właściwego socketu:

```bash
filees status                     # weryfikuje, czy demon odpowiada
filees-gui-wails --socket /ścieżka/do/daemon.sock
```

Domyślna ścieżka socketu: `$XDG_RUNTIME_DIR/filees.sock` lub `~/.filees/daemon.sock`.

### Panel albo okno ustawień nie otwiera się (Linux)

Sprawdź obecność GTK4 i WebKitGTK 6.0 oraz uruchom GUI z terminala, aby zobaczyć
błąd inicjalizacji WebView. `yad` i `zenity` należą do porzuconego renderera i
ich instalowanie nie naprawia klienta Wails.

### Powiadomienia toast nie pojawiają się (Windows)

Powiadomienia wymagają zarejestrowanego AUMID. Upewnij się, że klient Wails
został zainstalowany przez właściwy pakiet, a nie uruchomiony jako luźny EXE.

### Wiele ikon w trayu

Aktualna wersja blokuje drugą instancję przed utworzeniem ikony. Jeśli mimo to
widoczne są dwie ikony, jedna może pochodzić z porzuconego `filees-gui` albo z
innej sesji użytkownika. Zakończ stary proces, sprawdź wersję poleceniem
`filees-gui-wails --version` i uruchom ponownie bieżącą instalację.

### „Widoczność…” nie jest dostępna albo folder pokazuje „brak aliasu”

Tożsamość istniejącej strefy nie dotarła jeszcze w projekcji. Nie ustawiaj
nowego aliasu. Odczekaj odświeżenie, użyj **Połącz ponownie**, a jeżeli stan
wraca po restarcie — administrator musi zrekoncyliować widok klienta po stronie
serwera. GUI celowo nie otwiera dialogu widoczności bez kanonicznego aliasu.

### Repozytorium pokazuje `working_copy_missing`

Sprawdź, czy kopia nie została przeniesiona lub czy dysk jest podłączony. Jeśli
znasz jej nową lokalizację, użyj **Wskaż kopię**. Nie twórz ręcznie pustego
katalogu pod starą nazwą; nie jest on prawidłową WC i nie zostanie uruchomiony.

---

## Znane ograniczenia wersji 0.1.0

- **Aktywacja powiadomień**: powiadomienia są informacyjne; kliknięcie nie otwiera jeszcze katalogu ani szczegółów błędu. Nie wykonuje też żadnej operacji mutującej.
- **MSI Windows**: kompletny pakiet daemon+Wails i updater Windows pozostają
  zadaniem wydaniowym; stare MSI GUI-only nie jest bieżącym klientem.
- **„Poproś o zwolnienie”**: przepływ prośby i odpowiedzi działa; automatyczne
  `svn up` + przejęcie locka u requestera oraz GC wymagają jeszcze domknięcia.
- **Wstrzymanie / Sync now**: funkcje nieobecne w bieżącym kontrakcie daemona;
  pojawią się po dodaniu odpowiednich capabilities.
