# Kanały wydań FileES

Decyzja właściciela z 2026-09-21: 0.1.17 jest kandydatem do pierwszej bety.
Ten dokument opisuje przygotowanie procesu, nie ogłoszenie publikacji.

## Reguły

- **alpha**: bieżące poprawki i nowe funkcje, świadomi testerzy.
- **beta**: konkretne odebrane wydanie, pozostające na miejscu do jawnego
  awansu kolejnego. Nie śledzi HEAD ani kanału alpha.
- **stable**: rezerwa na później; obecna decyzja go nie uruchamia.

Wersja produktu, rewizja źródeł i kanał to różne informacje. Wydanie 0.1.17
ma jednoznaczny numer kompilacji. Promocja tej samej paczki do beta nie zmienia
jej binariów, manifestów, podpisów manifestów, release_id ani sequence.
Poprawka kodu wymaga nowego wydania, również w obrębie 0.1.17. Nowe funkcje
po zamrożeniu 0.1.17 pozostają w alpha z kolejną wersją produktu.

Nie tworzymy teraz kopii drzewa źródeł ani długowiecznej gałęzi SVN beta.
Kanał jest podpisanym wskaźnikiem do niezmiennego katalogu releases/.
Ewentualna gałąź poprawek starszej bety wymaga osobnej decyzji, gdy zaistnieje
taka potrzeba; nigdy nie nadpisujemy opublikowanego katalogu wydania.

## Desktop: dwa buildy z jednej rewizji (decyzja 2026-09-24)

Od 2026-09-24 **beta desktopu to osobny build, nie awans alfy**. Alfa niesie
wszystkie funkcje, łącznie z kotwicą w Eksploratorze; beta i stable są
budowane z tagiem `nocfapi` — bez kotwicy i bez kodu Cloud Files API w
demonie. Pakiet Microsoft Store śledzi betę i też jest `nocfapi`.

- Wariant wynika z kanału wkompilowanego w klienta:
  `packaging/build-client-bundle.sh` dla `FILEES_RELEASE_CHANNEL=beta|stable`
  dodaje `nocfapi` i przerywa budowę, jeśli demon nadal zawiera `cldapi`,
  `CfGetPlaceholderState` albo `filees-cfapi.exe`.
- `tools/prepare-client-release-{windows,linux}.sh` zapisują
  `releases/<id>/built-for-channel`; oba systemy jednego wydania muszą mieć
  ten sam kanał.
- `tools/release-sign-and-publish.sh` publikuje wydanie tylko w kanale, dla
  którego je zbudowano, i tylko wtedy wolno mu po raz pierwszy podpisać
  manifesty poza alfą. Alfy nie da się więc opublikować w becie ani odwrotnie.
  Wydania sprzed tej zmiany (bez `built-for-channel`) zachowują stary awans.

Jedna rewizja, dwa wydania, na przykład:

```sh
RELEASE_ID=r1510       CHANNEL=alpha SEQUENCE=… tools/prepare-client-release-windows.sh
RELEASE_ID=r1510-beta  CHANNEL=beta  SEQUENCE=… tools/prepare-client-release-windows.sh
```

Każde wydanie ma własne `release_id`, a w obrębie kanału rosnące `sequence`.
Serwer bez zmian: jego beta pozostaje awansem odebranego wydania alfa.

## Dwa niezależne zestawy kanałów

| Produkt | Alfa | Beta |
| --- | --- | --- |
| Desktop Windows i Linux | channels/alpha.v2.json | channels/beta.v2.json |
| Serwer | channels/alpha.json | channels/beta.json |

Każdy wskaźnik ma osobny podpis .sig. Windows i Linux desktopu są w jednym
wydaniu: kandydat przed promocją zawiera manifesty obu platform. Ocena
serwera dotyczy odebranego OpenBSD; sama kompilacja nie dopuszcza Linuxa.
Desktop i serwer mają niezależne release_id, sequence i odbiory.

## Awans

1. Z czystych źródeł przygotować paczki i neutralny kandydat kanału.
2. Właściciel podpisuje i publikuje alpha dotychczasowym skryptem.
3. Odebrać dokładnie te paczki. Dla tej rundy: link odbiorcy serwer → desktop,
   stabilny panel na obu systemach, aktualizacja/restart OpenBSD i nowy raport
   wersji. Nierozstrzygnięty test historii SVN Windows wymaga ustalenia
   przyczyny albo jawnej decyzji akceptacji ryzyka, nie przemilczenia.
4. Na maszynie podpisującej uruchomić ten sam skrypt z identycznym RELEASE_ID
   oraz `CHANNEL=beta`. Skrypt wymaga już istniejących poprawnych podpisów
   wszystkich manifestów i podpisuje jedynie nowy wskaźnik kanału.
5. Zweryfikować podpis beta i niezmienność alpha. Następne wydanie alpha
   nie wykonuje żadnego zapisu do beta.

Podpis manifestów jest warunkiem technicznym, nie dowodem odbioru. Decyzja
o awansie pozostaje po stronie właściciela. Klucz prywatny zostaje na jego
maszynie podpisującej. Aktualny skrypt należy przed użyciem przenieść do
FILEES-BIN/tools — samo jego zmienienie w źródłach nie aktualizuje publikatora.

Nie zerujemy liczników antyrollback i nie przepisujemy sequence na potrzeby
kanału. Instalacja nowszej alfy nie może automatycznie cofnąć się do starszej
bety. Należy poczekać, aż beta dogoni zainstalowane wydanie; zejście wstecz
nie jest częścią tej zmiany.

## Kanał instalacji: warunek przed pierwszą betą desktopu

Serwer już ma `repo.channel` w install.conf. `filees-admin version` pokazuje
go jako update_channel, oddzielnie od rewizji binarium i recorded_release.
Zmiana kanału nie stanowi potwierdzenia aktualizacji ani restartu usług.

Desktop obecnie korzysta z jawnej sekcji `update` konfiguracji, a przy jej
braku z kanału wkompilowanego. Dlatego samo skopiowanie alpha.v2.json do
beta.v2.json **nie tworzy poprawnej dystrybucji beta dla nowych instalacji**
ze starym instalatorem: MSI/AppImage bez dialogu wyboru nadal wybiorą alfę.

Trwały wybór kanału jest dostępny przez CLI (w nowym buildzie):

```sh
filees update-channel --config /path/config.json
filees update-channel beta --config /path/config.json
```

Pierwsze polecenie tylko odczytuje, drugie zapisuje wybór w istniejącym
config.json i wymaga restartu pary. Bez pobierania, instalowania ani
automatycznego cofania wersji. Polecenie wymaga istniejącej konfiguracji;
nie zakłada profilu w przypadkowym katalogu. Na Windows użyć filees.exe
z katalogu instalacji i wskazać jego config.json; na Linux zwykle
`$XDG_CONFIG_HOME/filees/config.json` lub `~/.config/filees/config.json`.
Stan antyrollback oraz jego ścieżka pozostają niezmienione. Zbyt nowa alfa
nadal nie może pobrać starszej bety. Polecenie odmawia przełączenia przy
jawnym enabled:false i w trybie Store. Dostępne nazwy: alpha, beta, stable;
akceptacja nazwy nie oznacza, że taki kanał został już opublikowany.

Nowe instalatory przy tworzeniu konfiguracji wywołują wspólny dialog Wails
alpha/beta. Dopiero zapis wyboru kończy inicjalizację; anulowanie nie
uruchamia demona i przy następnym starcie pytanie wraca. Domyślnie zaznaczona
beta wymaga zatwierdzenia. Wybór dotyczy przyszłych aktualizacji, nie
pochodzenia właśnie instalowanego pliku. Istniejąca konfiguracja (także
bez jawnego kanału albo z wyłączonym update) pozostaje nietknięta.

Linux bez sesji graficznej wymaga jawnego wyboru przy nowej instalacji:

```sh
FILEES_UPDATE_CHANNEL=beta sh install-user.sh
```

Ten parametr nie przełącza istniejących instalacji. AppImage korzysta
z tego samego instalatora; Windows pyta przy pierwszym uruchomieniu
supervisora. Store nie używa tych ścieżek i zachowuje własne aktualizacje.
Przed publiczną pierwszą betą pozostaje powiązanie strony pobierania
z oboma kanałami oraz odbiór prawdziwego MSI/AppImage.
Nie przebudowujemy
odebranych binariów wyłącznie po to, by zmienić kanał. Jawne wyłączenie
aktualizacji i tryb Store nadal mają pierwszeństwo. Istniejących użytkowników
alpha nie przełączamy automatycznie.

## Strona pobierania

Po podpisaniu beta: /download/ oferuje beta, /download-alpha/ rozwojową alpha.
Build i cron odczytują oba podpisane zestawy wskaźników oddzielnie, nie
wyznaczają bety z najnowszej rewizji. State-beta.json jest nowym stanem beta;
dotychczasowy state.json pozostaje przy alpha. Konfiguracja, szablon, binary,
publish.sh i cron muszą być wdrożone razem przez packaging/site/install.sh.
Właściciel nadal wdraża paczkę WWW ręcznie; lokalny ZIP nie dowodzi wdrożenia.
