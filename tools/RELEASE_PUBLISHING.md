# FileES release publishing

> **Komendy krok po kroku:** `HOWTO-BUILD-SERVER-BUNDLE.md`. Przykłady poniżej
> pokazują klucz pod `$HOME/.signify/filees-release.pub` — to ścieżka z maszyny
> **podpisującej**. Na hoście budującym klucz publiczny bierze się z kopii
> roboczej `FILEES-BIN/FILEESrelease.pub`; klucze w `cmd/*/assets/release.pub`
> są zaślepkami i build je odrzuca.

Publikacja rozdziela maszynę budującą od maszyny podpisującej. Prywatny klucz
release nie może znajdować się na build hoście, VM testowej ani w repozytorium.

## 1. Deterministyczny manifest

Produkcyjny build klienta przyjmuje wyłącznie publiczny trust root:

```sh
FILEES_RELEASE_PUBKEY="$HOME/.signify/filees-release.pub" \
FILEES_RELEASE_KEY_ID="release-2026-a" \
./packaging/build-gui.sh linux-amd64
```

Obie zmienne muszą wystąpić razem. Build odrzuca placeholder i nie zna żadnej
ścieżki ani zmiennej private key. Publiczny klucz jest kodowany base64 i
wstrzykiwany do binarki daemona przez linker; źródłowy placeholder i WC nie są
modyfikowane. `build-gui.sh` tworzy katalog bundla oraz deterministyczny
`filees-client-linux-amd64.tar.gz` przez `filees-release-bundle`: sortowane
ścieżki, uid/gid i czas równe zero, tryby 0755/0644, bez symlinków i plików
specjalnych.

Kandydat klienta Windows powstaje na hoście budującym bez klucza prywatnego:

```sh
FILEES_BIN_WC="$HOME/FILEES-BIN" \
RELEASE_ID=r826 SEQUENCE=826 SECURITY_EPOCH=1 \
KEY_ID="release-2026-a" CHANNEL=alpha \
./tools/prepare-client-release-windows.sh
```

Skrypt buduje dokładnie tę samą parę, którą później konsumują MSI i
self-update, oraz zapisuje manifest pod
`releases/<id>/desktop/windows-amd64/manifest.json` i neutralny kandydat jako
`releases/<id>/channel.v2.json`. Nie podpisuje i nie dotyka `channels/`.
Wails powstaje z tagiem `production` i subsystemem `windowsgui`, więc binarium
z kanału nie otwiera okna konsoli przy starcie z nadzorcy.
Jeżeli istniejący kanał v2 zawiera inną platformę ze starszego release'u,
przygotowanie kończy się odmową. Tego wpisu nie można przepisać: każdy manifest
musi powtarzać tożsamość nowego envelope. Przed wspólnym kanałem Windows+Linux
obie platformy trzeba złożyć pod jednym `release_id` i dopiero wtedy podpisać.

Produkcyjny build serwera również przyjmuje wyłącznie publiczny trust root:

```sh
FILEES_RELEASE_PUBKEY="$HOME/.signify/filees-release.pub" \
./packaging/build-server.sh openbsd-amd64
```

Klucz jest wstrzykiwany wyłącznie do `filees-install`. Build odrzuca placeholder
i nie zna nazwy ani ścieżki klucza prywatnego.

Polityka binariów OpenBSD jest wersjonowana w
`packaging/server/openbsd-binary-policy.json`. Każdy target ma jawny `owner`,
`group` i pełny tryb POSIX, w tym set-id:

```json
{
  "platform": "openbsd-amd64",
  "files": [
    {
      "source": "bin/filees-admin",
      "target": "{sbin_dir}/filees-admin",
      "kind": "binary",
      "mode": "4511",
      "owner": "_filees-state",
      "group": "wheel"
    }
  ]
}
```

`sequence` i `security_epoch` są **obowiązkowe i niezerowe**. `sequence` rośnie
o jeden z każdym publikowanym wydaniem i nigdy nie jest ponownie użyty dla innego
`release_id`. `security_epoch` podnosi się wyłącznie wtedy, gdy wydanie naprawia
coś, czego starsze wydanie nie może cofnąć — po podniesieniu epoki instalator
odmówi zejścia poniżej niej nawet dla wydania o wyższym `sequence`. Bez tych
liczników poprawnie podpisane, ale stare wydanie z załataną już podatnością
byłoby przyjęte jak zwykła aktualizacja (podpis dowodzi autentyczności, nie
świeżości). Plik kanału musi powtórzyć obie wartości — instalator odrzuca
rozjazd między kanałem a manifestem. Świadome cofnięcie wymaga jawnego
`filees-install --allow-rollback` i jest głośno logowane.

Generator nie przyjmuje digestów od operatora. Czyta każdy regularny plik z
payloadu, odrzuca traversal, wyjście przez symlink, powtórzone targety oraz
nieznane pola specyfikacji. To samo źródło może celowo zasilać dwa targety o
różnych granicach uprawnień (np. recovery entry i authorize). Wpisy sortuje po
`source`, a następnie `target`; nie dodaje bieżącej daty, dlatego identyczne
wejście daje identyczny manifest.

```sh
FILEES_BIN_WC="$HOME/FILEES-BIN" \
FILEES_RELEASE_PUBKEY="$HOME/.signify/filees-release.pub" \
RELEASE_ID=r178 SEQUENCE=178 SECURITY_EPOCH=1 \
./tools/prepare-server-release.sh
```

Skrypt wymaga czystej WC źródeł i czystej WC `FILEES-BIN`, aktualizuje obie do
HEAD, buduje binaria z kluczem publicznym, generuje manifest schema v2 oraz
`releases/<id>/channel.json`. Kandydat jest neutralny względem kanału. Następnie
operator recenzuje i commituje **wyłącznie** niezmienny katalog
`releases/<id>`. Katalog `channels/` pozostaje w tym commicie nietknięty.

Manifest serwera schema v2 jest konsumowany przez `filees-install`. Klientowe
instalatory Linux/MSI/Android pozostają własnymi wykonawcami lifecycle'u; ich
artefakty są związane wspólnym wielokomponentowym release envelope v2, zamiast
otrzymywać serwerowe pola `target`.

Envelope klienta ma ścisły schemat i wskazuje manifest każdego
`component/platform`. Przykład kanału:

```json
{
  "schema_version": 2,
  "release_id": "r188",
  "sequence": 188,
  "security_epoch": 1,
  "key_id": "release-2026-a",
  "expires_at": "2026-08-22T00:00:00Z",
  "components": [
    {
      "name": "desktop",
      "platform": "linux-amd64",
      "manifest": "releases/r188/desktop/linux-amd64/manifest.json"
    }
  ]
}
```

Manifest v2 musi powtórzyć `release_id`, `sequence`, `security_epoch`, `key_id`,
`component` i `platform`, podać wersję oraz dokładny rozmiar i SHA-256 bundla.
Kanał i manifest są podpisywane oddzielnie tym samym zaufanym kluczem. Nie wolno
ponownie użyć sequence dla innego release ani go obniżyć; zainstalowany klient
utrzymuje trwały high-water mark.

## 2. Podpisanie offline

Na dedykowanej maszynie podpisującej, w czystej i aktualnej WC FILEES-BIN:

```sh
FILEES_BIN_WC="$HOME/FILEES-BIN" \
SIGNIFY_SEC_KEY="$HOME/.signify/filees-release.sec" \
SIGNIFY_PUB_KEY="$HOME/.signify/filees-release.pub" \
RELEASE_ID=r178 CHANNEL=alpha ./tools/release-sign-and-publish.sh
```

Skrypt wybiera z release’u wcześniej zrecenzowany kandydat kanału, podpisuje
każdy manifest, natychmiast weryfikuje podpisy kluczem publicznym, a następnie
kopiuje kandydat do `channels/` i podpisuje go. Nowe podpisy manifestów, kanał
i jego podpis trafiają do **jednego commita SVN**. HEAD nie ma więc okna, w
którym wybrany kanał wskazuje na niepodpisany release. `CHANNEL` nie ma wartości
domyślnej i musi być jawnie ustawiony na `alpha`, `beta` albo `stable`. Ten sam
release można później promować do kolejnego kanału bez przebudowy payloadu.
Jeśli release jest już podpisany i promowany, wykonanie jest no-op; istniejący
niepoprawny podpis manifestu powoduje fail closed zamiast nadpisania historii.

## 3. Przejęcie istniejącego serwera

Po jednorazowym wdrożeniu bundla z produkcyjnym kluczem i opublikowaniu release’u
bazowego uruchom na OpenBSD:

```sh
filees-install -c /etc/filees/install.conf --adopt r178
filees-install -c /etc/filees/install.conf --check
```

`--adopt` nie pobiera ani nie podmienia payloadu. Wymaga idealnej zgodności
SHA-256, właściciela, grupy i pełnego trybu każdego pliku z podpisanym
manifestem, po czym zapisuje release i high-water mark. Dzięki temu istniejący
serwer przechodzi pod zarządzanie bez uruchamiania zadań pierwszej instalacji.
Każdy późniejszy `--apply` najpierw zapisuje trwały journal kompletnych
pre-image; po przerwaniu zasilania kolejna komenda automatycznie odtwarza całą
poprzednią wersję albo usuwa journal już zatwierdzonego upgrade’u.

## 4. Odbiór klienta i test regresyjny

Po publikacji produkcyjny klient z odpowiednim embedded keyringiem powinien:

1. pokazać badge nowej wersji dopiero po weryfikacji envelope i manifestu;
2. zwrócić dry run bez modyfikacji filesystemu;
3. przed apply ponownie rozwiązać wydanie i pokazać natywne potwierdzenie;
4. sprawdzić rozmiar i SHA-256 bundla przed ekstrakcją;
5. uruchomić istniejący instalator platformowy i dopiero po sukcesie zapisać
   high-water mark;
6. zwolnić blokadę GUI przed uruchomieniem nowej binarki.

Reprodukowalne E2E jest częścią `go test ./internal/clientupdate`. Tworzy lokalne
repo SVN, podpisuje release testowym kluczem w formacie OpenBSD signify, wykonuje
rzeczywisty `packaging/linux/install-user.sh` w izolowanym HOME i potwierdza
odmowę rollbacku, złego podpisu oraz artefaktu o niezgodnym SHA-256. Testowy
klucz powstaje wyłącznie w katalogu tymczasowym i nie jest trust rootem wydania.
