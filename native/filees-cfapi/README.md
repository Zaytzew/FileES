# filees-cfapi — kotwica w Eksploratorze

Helper powłoki Windows dla pionu „Kotwica w Eksploratorze”
(`implementation notes (not distributed)`, porcje w
`implementation notes (not distributed)`).

Podział jak przy `native/filees-svn`: **demon zna Subversion, helper zna
powłokę**. Ten program nie łączy się z żadnym serwerem i nie dotyka kopii
roboczej. Wszystko, co wie, dostaje w argumentach i na wejściu.

Wymaga Windows 10 1709+ (Cloud Files API, `cldflt.sys`). Na innych systemach
CMake odmawia konfiguracji — to nie jest przenośny kod.

## Czasowniki

Każdy wypisuje **jeden obiekt JSON** na standardowe wyjście
(`"schema":"filees.cfapi/v1"`), a kod wyjścia tylko go potwierdza.

| Czasownik | Co robi |
|---|---|
| `version` | schemat, lista czasowników i flagi funkcji |
| `register --root <folder> --identity <tekst>` | rejestruje folder jako kotwicę; tożsamość wraca w callbackach |
| `unregister --root <folder>` | zdejmuje rejestrację |
| `info --root <folder>` | czy to kotwica, czyja i czy dostawca działa |
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
