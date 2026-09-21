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
beta.v2.json **nie tworzy poprawnej dystrybucji beta dla nowych instalacji**:
MSI/AppImage z domyślną alfą nadal ją wybiorą.

Przed publikacją pierwszej bety potrzebny jest trwały wybór kanału instalacji,
zachowywany przez aktualizację i restart na Windows/Linux. Nie przebudowujemy
odebranych binariów wyłącznie po to, by zmienić kanał. Jawne wyłączenie
aktualizacji i tryb Store nadal mają pierwszeństwo. Istniejących użytkowników
alpha nie przełączamy automatycznie.

## Strona pobierania

Do podpisania i odbioru bety strona nadal oferuje alpha. Dopiero potem beta
może stać się domyślnym pobraniem, z oddzielnym, jednoznacznym odsyłaczem do
alpha. Publikator downloadów musi czytać oba podpisane wskaźniki oddzielnie,
nie wyznaczać bety z najnowszej rewizji. Właściciel nadal wdraża paczkę WWW
ręcznie; lokalny ZIP nie jest dowodem wdrożenia.
