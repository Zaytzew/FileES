# filees-cfapi — punkt zaczepienia w Eksploratorze

## Metadane odtworzonego wpisu 2026-10-06

Opcjonalne release_observation_v1 rozszerza odpowiedź pojedynczego
file-state o size i identity. To rozmiar logiczny oraz ograniczona,
poprawna tożsamość UTF-16 przekonwertowana do UTF-8; brak obsługiwanej
tożsamości zwraca pusty tekst, nie zgodę. Nadal wyłącznie metadane
własnego korzenia, bez odczytu bajtów i sieci. Stan i flaga chmurki same
nie potwierdzają tożsamości pobrania. Odpowiedź zbiorcza pozostaje bez zmian.

Prywatne recoverSeed w Go używa tego odczytu po faktycznym wykluczeniu
z SVN, porównując rejestr v2 z UUID repo oraz zabezpieczone bajty.
Odtwarza jedynie brakujący wpis przez podłączony restore-release;
zastane obiekty nie są zastępowane. Nie wywołuje prepare/exclude ani
nie usuwa kopii. Nie jest podpięte do menedżera/startup/GUI; pierwsza
część transakcji i ochrona podmiany nazwy między procesami pozostają.
Dowody: implementation notes (not distributed)

## Wewnętrzne przygotowanie i odtworzenie po wykluczeniu 2026-10-06

Podłączony pomocnik rozumie dwie opcjonalne linie prywatnego mostu:

```
demon  -> helper   prepare-release <TAB> id <TAB> ścieżka <TAB> USN
helper -> demon    release-prepared <TAB> id <TAB> HRESULT
demon  -> helper   restore-release <TAB> id <TAB> ścieżka <TAB> rozmiar
helper -> demon    release-restored <TAB> id <TAB> HRESULT
```

`prepare_release_usn_v1` oznacza kontrolę własnego pełnego lokalnego
pliku, markera, stanu bez zmian i USN pod uchwytem READ/WRITE_DAC bez
współdzielenia. Otwarci czytelnicy/pisarze, niepełny plik, nieaktualny token
i prywatna/obca ścieżka odmawiają. Revert usuwa tylko oznaczenia CFAPI,
nie bajty; atrybut tylko do odczytu pozostaje. Kontrolka bez zmian nie
zastępuje porównania z bazową treścią SVN i zabezpieczenia poza WC.

`restore_release_v1` tworzy dokładnie jeden nowy niepobrany wpis. Tożsamością
pobrania jest ścieżka w repo, a rozmiar pochodzi od przyszłego koordynatora.
Istniejący plik, placeholder lub katalog odmawiają bez zastępowania.
Faktyczny korzeń rodzica jest sprawdzany przez uchwyt, również przy junction.
Zadanie trafia do podłączonego pomocnika: odtworzenie przez osobny proces
przy aktywnym dostawcy odmawiało z CLOUD_FILE_IN_USE.

Nie ma samodzielnych czasowników, wywołania z demona ani nowej akcji GUI.
Trwała transakcja, blokada WC między etapami, kontrola zmiany nazwy/podmiany,
odzyskanie po przerwaniu i wycofanie kopii zabezpieczającej są nadal konieczne.
Uchwyty nie stanowią rezerwacji ścieżki po odpowiedzi pomocnika.
Dowody: `implementation notes (not distributed)`.

## Świadome pobranie jednego pliku 2026-10-06

`hydrate --root <punkt> --rel <plik>` sprawdza własny punkt i faktyczny
korzeń otwartego pliku przed żądaniem bajtów. Otwiera metadane z WRITE_DAC,
bez READ_DATA, potem CfHydratePlaceholder żąda całości przez działającego
dostawcę. Odmawia folderów, prywatnych ścieżek i wyjścia poza punkt.
Nie przypina na stałe, nie nadaje stanu czystego, nie konwertuje ani nie wywołuje SVN;
plik już lokalny pozostaje bez zmian, również przy nieopublikowanej edycji.
Funkcja wymaga flagi zgodności explicit_hydrate_v1.

Przeglądarka demona używa istniejącego repo.head_materialize. Powodzenie
hydratacji nie wystarcza: demon czeka na status potwierdzający przyjęcie
ścieżki do WC. Status używa strażnika pisarza, ale samo oczekiwanie na
hydratację nie trzyma go, bo callback adopt też go potrzebuje. Brak zgodnego
helpera nie pozwala użyć zwykłego SVN na placeholderach. Folderowe pobranie,
zwolnienie miejsca i wybór przy masowych pobraniach są osobnymi porcjami.
Raport: `implementation notes (not distributed)`.

## Bieżąca próba stanów — 2026-10-05, baza r1921 + WC

Oznaczenia systemowe nadal wymagają `FILEES_CFAPI_SHELL_PREVIEW=1`;
nie są domyślnie włączone. W tym trybie po przyjęciu przez SVN plik
zachowuje CFAPI i wszystkie lokalne bajty (`ALWAYS_FULL`, bez zwalniania
miejsca). Tożsamość `filees.local/v1` odróżnia tę lokalną pracę od
niewczytanego wpisu drzewa. Zmiana nazwy i usunięcie lokalnego pliku
przechodzą do zwykłego obserwatora/publikacji; niewczytane wpisy nadal
odmawiają. Bez zmiennej działa dotychczasowy tor `adopt` → `revert`.

`file-state --root <punkt> --rel <plik>` odczytuje wyłącznie metadane:
placeholder, dostępność bajtów, stan lokalny/bez zmian i token USN.
Nie pobiera danych. Prywatny most podłączonego dostawcy otrzymuje:

```
demon  -> helper   state-set <TAB> id <TAB> ścieżka względna <TAB> USN <TAB> 0|1
helper -> demon    state-set <TAB> id <TAB> HRESULT
```

Nie ma samodzielnego polecenia do ustawiania stanu. Helper sprawdza własny
korzeń, faktyczny identyfikator punktu otwartego pliku, lokalne bajty i USN.
Krótki wyłączny uchwyt chroni porównanie i zapis metadanych; nie zdejmuje
atrybutu tylko do odczytu. Niepełny plik i nieaktualny token odmawiają.
Flaga zgodności `local_state_usn_v1` oznacza, że nowy demon wymaga nowego
helpera w tej samej paczce, również gdy próba jest wyłączona.

Demon sprawdza status SVN przed potwierdzeniem braku zmian, w tym stan
właściwości. Brak wiersza lub błąd nie oznacza pliku czystego. Odczyt
jeden raz obejmuje najwyżej 64 lokalne ścieżki, również przy błędach;
kursor i odpowiedzi nie tworzą trwałej pamięci wszystkich ścieżek.
Nadal potrzebne przed domyślnym użyciem: zbiorczy odczyt zamiast procesów
per plik, serializacja statusu/właściwości z pisarzem WC i odbiór wyglądu
powłoki. USN zabezpiecza bajty, nie zmiany właściwości w bazie SVN.
Na woluminie bez obsługi wymaganego USN potwierdzenie nie jest nadawane.

Testy na syntetycznych punktach: pełne pobranie/adopt, edycja/publikacja,
właściwości, ponowne podłączenie, zastąpienie przy zapisie, zmiana nazwy
przez usługę publikacji, tylko do odczytu i odłączenie PASS. Nie jest to
odbiór żywego BricsCAD, podglądów ani zainstalowanej pary właściciela.
Raport: `implementation notes (not distributed)`.
Datowane opisy pierwszego cięcia poniżej zachowują dawny kontrakt.

Helper powłoki Windows dla pionu „Kotwica w Eksploratorze”
(`implementation notes (not distributed)`, porcje w
`implementation notes (not distributed)`).

Podział jak przy `native/filees-svn`: **demon zna Subversion, helper zna
powłokę**. Ten program nie łączy się z żadnym serwerem i nie dotyka kopii
roboczej. Wszystko, co wie, dostaje w argumentach i na wejściu.

Wymaga Windows 10 1709+ (Cloud Files API, `cldflt.sys`). Na innych systemach
CMake odmawia konfiguracji — to nie jest przenośny kod.
Opcjonalna rejestracja powłoki używa także `ProviderId` z Windows 10 1809+.
Budowa wymaga C++17 oraz nagłówków C++/WinRT z Windows SDK. Pozostały helper
pozostaje w C; adapter nie dodaje zewnętrznych bibliotek wykonawczych.

## Czasowniki

Każdy wypisuje **jeden obiekt JSON** na standardowe wyjście
(`"schema":"filees.cfapi/v1"`), a kod wyjścia tylko go potwierdza.

| Czasownik | Co robi |
|---|---|
| `version` | wersja produktu z pliku VERSION, schemat, lista czasowników i flagi funkcji |
| `register --root <folder> --identity <tekst>` | rejestruje folder jako kotwicę; tożsamość wraca w callbackach |
| `shell-register --root <folder> --identity <tekst> --icon <plik.ico>` | rejestruje istniejący punkt FileES w powłoce użytkownika; tryb testowy |
| `unregister --root <folder>` | zdejmuje rejestrację CFAPI i powłoki, również przy ponowieniu |
| `info --root <folder>` | czy to kotwica, czyja i czy dostawca działa |
| `file-state --root <folder> --rel <plik>` | odczyt metadanych i USN, bez pobrania |
| `hydrate --root <folder> --rel <plik>` | jawne pobranie jednego pliku przez działającego dostawcę, bez zastępowania lokalnych bajtów |
| `placeholders --root <folder> [--rel <podkatalog>]` | tworzy placeholdery z listingu na wejściu |
| `connect --root <folder>` | podłącza dostawcę i **trzyma połączenie do zamknięcia wejścia** |
| `revert --root <plik>` | zamienia pobrany placeholder w zwykły plik; tylko przy niepodłączonym dostawcy |

Listing dla `placeholders` to jedna pozycja w wierszu, pola rozdzielone
tabulatorem:

```
kind <TAB> rozmiar <TAB> tożsamość <TAB> nazwa
```

`kind` to `f` albo `d`, rozmiar jest dziesiętny, nazwa idzie na końcu, bo
jako jedyna może zawierać spacje. Nazwa ze znakiem ścieżki, `..` albo
tabulatorem jest odrzucana, nie naprawiana.

## Zgodność przy uruchomieniu — 2026-09-29

Demon odpytuje `version` przed podłączeniem menedżera punktów zaczepienia.
Wymaga zgodnego schematu, wszystkich używanych funkcji oraz tej samej bazowej
wersji produktu (np. `0.1.18`). Rewizja SVN `r` nie jest porównywana; to nie
kontrola podpisu ani dowód zgodności dowolnych zmian implementacji. Zmiana
protokołu musi mieć odzwierciedlenie w schemacie lub flagach funkcji.

`FILEES_CFAPI` zmienia ścieżkę, ale nie omija kontroli. Demon z wersją `dev`
pomija tylko równość wersji produktu. Brak wersji w starym pomocniku jest
odmową także dla `dev`. Limit odpowiedzi to 64 KiB, zapytania 10 sekund
(z dodatkowym ograniczeniem oczekiwania na zamknięcie potoków).

Po odmowie nie powstaje menedżer ani możliwość tworzenia nowych punktów.
Istniejące rejestracje i foldery pozostają nietknięte; odłączenie wymagające
pomocnika jest blokowane przed sprzątaniem metadanych. Pobieranie na żądanie
pozostaje niedostępne do dostarczenia zgodnej pary. Dlatego nowy demon alfy
musi być wydany razem z nowym pomocnikiem, nie samodzielnie. Nie zmienia to
trybu częściowego podłączenia: Windows CFAPI nie przechodzi awaryjnie na sparse.

## Most do demona

`connect` rozmawia z demonem **własnym wejściem i wyjściem**, jedną linią w
każdą stronę. Helper nie zna IPC, JSON-a ani Subversion:

```
helper -> demon    fetch <TAB> id <TAB> offset <TAB> length <TAB> tożsamość
demon  -> helper   ok    <TAB> id <TAB> bezwzględna ścieżka
                   err   <TAB> id <TAB> powód
```

Tożsamość to ta sama, z którą powstał placeholder — ścieżka w repozytorium.
Demon zamienia ją na ścieżkę na dysku (materializacja niepełnej kopii), a
helper czyta stamtąd bajty i oddaje je Windowsowi kawałkami po 1 MiB.

Demon może też poprosić podłączony helper o zamianę pobranego placeholdera w
zwykły plik, gdy SVN przyjął go już do kopii roboczej:

```
demon  -> helper   revert   <TAB> id <TAB> bezwzględna ścieżka
helper -> demon    reverted <TAB> id <TAB> HRESULT
```

Musi to robić **podłączony** proces: osobne `filees-cfapi revert` przy
podłączonym dostawcy dostaje `ERROR_CLOUD_FILE_IN_USE`. `0x80070178`
(`ERROR_NOT_A_CLOUD_FILE`) w odpowiedzi znaczy, że plik już jest zwykły.

**Kto uruchamia `connect`, musi czytać jego wyjście.** Callbacki chodzą na
wielu wątkach; jeśli nikt nie odbiera linii, zapis się blokuje, callback nie
odpowiada, a każda operacja w Eksploratorze czeka **dwie minuty** na timeout
filtra. Zmierzone: z odbieranym wyjściem odmowa kasowania trwa 1,5 ms, bez
niego 4 minuty.

## Co pierwsze cięcie robi, a czego nie

### Oznaczenia powłoki — próba z 2026-09-28

`shell_root.cpp` rejestruje każdy rzeczywisty korzeń osobno przez
`StorageProviderSyncRootManager`. ID to `FileES!SID!SHA256(ścieżka)`, nie nazwa
serwera ani repozytorium. Rejestracja jest trwała, jej ponowienie nie tworzy
duplikatu. Polecenie odrzuca zwykły folder i podfolder innego punktu.
Odłączenie usuwa również wpis powłoki po wcześniejszym wyrejestrowaniu CFAPI.
Nie porównuje wtedy dawnego GUID dostawcy: Windows może już go nie zwracać.

Demon wywołuje ten krok **wyłącznie z `FILEES_CFAPI_SHELL_PREVIEW=1`**.
Bez tej zmiennej zachowuje dotychczasowy wygląd. Odbiór w Eksploratorze
potwierdził chmurki i kolumnę „Stan”, ale zwykłe lokalne pliki dostają stan
„oczekiwanie na synchronizację”. Dotyczy to też docelowo plików przyjętych
przez SVN i zamienionych w zwykłe pliki przez `revert`; nie jest to dowód
oczekującego commitu. Dlatego nie włączamy oznaczeń automatycznie w wydaniu.
Wyłączenie zmiennej nie usuwa już utworzonego wpisu — test kończy się
wyrejestrowaniem własnego punktu. Nie używać tej próby na roboczych punktach.

Menu przypinania i zwalniania miejsca jest wyłączone (`AllowPinning=false`).
Nie ma własnego rozszerzenia nakładek. Aktualny próbny kontrakt po adopt
opisano na początku dokumentu; `revert` pozostaje domyślną ścieżką.
Raport: `implementation notes (not distributed)`.

### Dostarczanie plików

Stan 2026-10-09, baza r2050 + WC: normalny helper ma `file_delete_bound_v1`.
Pojedynczy niepobrany plik może zostać usunięty po typowanym przygotowaniu
demona i exact OS completion, z trwałym dowodem, jednym remote commitem,
metadata-only uzgodnieniem SVN, manifestem/skanerem i retirement. Zwykła
odmowa pozostaje przy braku dowodów/praw, katalogu, excluded SVN node
i remote rename (w tym przeniesieniu do Kosza). Switch zgody próby nigdy
nie działa w normalnym helperze. To nie odblokowuje wszystkich kasowań
ani dowolnego przeciągania w Explorerze; starszy opis poniżej dotyczy
pierwszego cięcia. E2E/granice: `implementation notes (not distributed)`.

- Placeholder wygląda w Eksploratorze jak zwykły plik swojego typu, ma
  prawdziwą nazwę i rozmiar z HEAD, a **na dysku zajmuje zero bajtów**
  (atrybut `FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS`).
- **Kasowanie i zmiana nazwy są odmawiane.** Obie znaczyłyby zmianę
  w repozytorium, a taka decyzja należy do FileES, nie do przeciągnięcia
  myszą w Eksploratorze.
- **Otwarcie pliku działa**: `FETCH_DATA` pyta demona o ścieżkę, pokazuje
  postęp w trakcie czekania i oddaje bajty z kopii roboczej. Odmowa demona
  kończy otwarcie błędem, nigdy pustym plikiem — dla CAD-a pusty plik jest
  gorszy niż brak pliku.

## Dwie pułapki, obie kosztowały czas

1. **Katalog jako placeholder** wymaga flagi
   `CF_PLACEHOLDER_CREATE_FLAG_DISABLE_ON_DEMAND_POPULATION`. Bez niej filtr
   odmawia (`ERROR_CLOUD_FILE_NOT_SUPPORTED`), bo spodziewa się, że dostawca
   dośle zawartość na żądanie — czego to cięcie świadomie nie robi.
2. **Samo `CfConnectSyncRoot` nie wystarczy.** Dopóki dostawca nie ogłosi
   stanu przez `CfUpdateSyncProviderStatus`, system odmawia operacji
   (`ERROR_CLOUD_FILE_PROVIDER_NOT_RUNNING`) i **nie woła żadnego
   callbacku** — objaw wygląda dokładnie jak zepsuta tablica callbacków,
   czyli prowadzi w złe miejsce.

`FILEES_CFAPI_TRACE=1` wypisuje na stderr, który callback przyszedł i co
odpowiedział. Bez tej zmiennej stderr milczy, bo czyta go demon.

## Budowanie i testy

Od 2026-10-06, baza r1953: odpowiedź `file-states` zawiera również
rozmiar i tożsamość każdego pliku. Cecha `batch_release_observation_v1`
jest wymagana przez aktualny demon; starszy pomocnik odmawia przed użyciem.
Podgląd folderu pozostaje odczytem metadanych. Jawna zgoda na pobranie
wiąże pojedynczą ścieżkę z przypiętą rewizją i faktycznym obrazem pomocnika
widzianym przez Windows; nie wyłącza strażnika innych programów.
Pobranie i zwolnienie folderu są szeregiem chronionych operacji plikowych,
nie atomową operacją ani automatyczną dehydratacją. Klasyczne menu i trwały
opt-in pozostają odrębne od tego kroku.

```powershell
cmake -S native/filees-cfapi -B <build> -G "Visual Studio 17 2022" -A x64
cmake --build <build> --config RelWithDebInfo
```

```sh
FILEES_CFAPI=<build>/RelWithDebInfo/filees-cfapi.exe \
  go test -tags native_cfapi_probe ./native/filees-cfapi/
```

Zestaw jest opt-in, bo **rejestruje prawdziwą kotwicę** w katalogu
tymczasowym. Każdy test ją po sobie wyrejestrowuje: rejestracja przeżywa
proces, testy i restart maszyny.
