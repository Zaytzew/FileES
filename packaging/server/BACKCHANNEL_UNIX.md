# Migracja backchannelu Public Shares do gniazd Unix

Decyzja z 2026-09-30: usługi `filees-public-authority` i `filees-links`
nie obsługują już surowego TCP, także loopback. Na jednym hoście używają
chronionego gniazda Unix; na osobnych hostach — gniazd Unix na obu końcach
odwrotnego tunelu OpenSSH. Nie dodajemy sekretu aplikacyjnego ani certyfikatu.
Nie zmienia to FastCGI, HTTPS obsługiwanego przez serwer WWW ani IPC desktopu.

## Jeden host — domyślna instalacja

Instalator już stosuje `unix`, `/var/run/filees/public-authority.sock`,
katalog `_filees-state:_filees-public` z prawami 0750 i gniazdo 0660.
Przy tych ustawieniach zmiana transportu nie jest potrzebna. Przed aktualizacją
sprawdź właścicieli i prawa: usługi nie poprawiają ich automatycznie.

W `server.json`, wewnątrz `public_shares`:

```json
"backchannel_network": "unix",
"backchannel_address": "/var/run/filees/public-authority.sock",
"backchannel_socket_group": "_filees-public"
```

W `public-links.json`:

```json
"backchannel": {
  "network": "unix",
  "address": "/var/run/filees/public-authority.sock"
}
```

To fragmenty istniejących konfiguracji, nie całe pliki do podmiany.

## Osobne hosty — migracja z TCP

1. Zatrzymaj dotychczasowy tunel w uzgodnionym oknie utrzymaniowym.
2. Po stronie autorytatywnej ustaw powyższe gniazdo Unix. Konto uruchamiające
   klienta SSH musi mieć dostęp do gniazda, np. przez grupę `_filees-public`.
   Klucz prywatny tunelu pozostaje na tym hoście; użyj istniejącego klucza
   i zweryfikowanego wpisu `known_hosts`, nie klucza bootstrap FileES.
3. Na hoście publicznym przygotuj osobny katalog, np.
   `/var/run/filees-backchannel`, należący do dedykowanego konta tunelu.
   Dla innego konta usługi links użyj grupy `_filees-public` i praw 2750
   (setgid zapewnia dziedziczenie grupy także na Linuksie). Usługa links
   należy do tej grupy. Tylko właściciel katalogu może tworzyć wpisy;
   grupa nie może mieć prawa zapisu.
4. W `public-links.json` ustaw `network: unix` i adres
   `/var/run/filees-backchannel/authority.sock`. Katalog musi istnieć
   przed uruchomieniem links; samo gniazdo może powstać później.
5. Skonfiguruj ograniczone konto tunelu po stronie publicznego `sshd`.
   Przykładowy blok wymaga dostosowania nazwy konta oraz kontroli efektywnej
   konfiguracji przez administratora:

```text
Match User _filees-tunnel
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AllowTcpForwarding remote
    PermitListen none
    AllowStreamLocalForwarding remote
    StreamLocalBindMask 0117
    StreamLocalBindUnlink no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
    MaxSessions 0
```

`AllowTcpForwarding remote` nie jest tu zgodą na słuchanie na porcie TCP:
`PermitListen none` odrzuca wszystkie takie żądania. W odebranych wersjach
OpenSSH ustawienie `AllowTcpForwarding no` blokowało również przekierowanie
Unix przez wspólną tablicę uprawnień kanałów. Test obejmuje działający tunel
Unix oraz odmowę żądania portu TCP. Nie używaj `DisableForwarding yes` ani
`no-port-forwarding` przy tym kluczu — wyłączą potrzebny tunel.
Opcję klucza `restrict` trzeba uzupełnić o `port-forwarding`.

`PermitListen` nie ogranicza ścieżek Unix. Granicą są prawa systemu plików
i dedykowane konto bez sesji powłoki. Nie współdziel go z innymi zadaniami.
Maska 0117 tworzy gniazdo 0660. Dla jednego konta po stronie publicznej
można zamiast tego użyć katalogu 0700 i maski 0177 (gniazdo 0600).

6. Klient SSH na serwerze autorytatywnym uruchamia przekierowanie:

```sh
ssh -nNT -o BatchMode=yes -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
  -i /path/to/existing/tunnel-key \
  -R /var/run/filees-backchannel/authority.sock:/var/run/filees/public-authority.sock \
  _filees-tunnel@public-host
```

   Podstaw istniejące dane tunelu, zweryfikuj `sshd -t` przed przeładowaniem
   konfiguracji oraz efektywne ustawienia Match. Nie zastępuj całego
   `sshd_config` przykładem. Zarządca usług musi odtwarzać katalog pod
   `/var/run` po restarcie systemu. Nie używaj katalogów czyszczonych przez
   mechanizm tymczasowych plików podczas pracy usług.
7. Uruchom authority, tunel i links. Sprawdź pobranie syntetycznego pliku,
   prawa obu gniazd, odmowę po zatrzymaniu tunelu i ponowny start. Dopiero
   potem przywróć ruch publiczny. Nie ma automatycznego powrotu do TCP.

## Restart i granice ochrony

Authority nie usuwa aktywnego gniazda, symlinka ani zwykłego pliku.
Pozostałość po awarii usuwa tylko przy zgodnym właścicielu i jednoznacznym
`ECONNREFUSED`. Niepewność oznacza odmowę startu, nie wymuszone przejęcie.

Osobno traktuj gniazdo tworzone przez **sshd**. Przy `StreamLocalBindUnlink no`
pozostałość po zerwanym tunelu może wymagać interwencji administratora.
Najpierw zatrzymaj stary tunel, potwierdź właściciela, ścieżkę i brak
nasłuchu; dopiero wtedy usuń dokładnie tę pozostałość. Nie stosuj ogólnego
usuwania katalogu ani automatycznego `StreamLocalBindUnlink yes`, które
mogłoby odłączyć działający tunel. Nadzorca ponowień tunelu musi uwzględniać
ten stan — FileES nie zarządza cyklem procesu SSH administratora.

Authority sprawdza katalog i jego przodków przed bind. Links robi to przed
`unveil`, a przy każdym nowym połączeniu sprawdza typ, właściciela i prawa
gniazda. Administrator/root i właściciel katalogu są zaufani; nie mogą
rozluźniać praw w trakcie pracy. Zmiana katalogu lub trasy wymaga restartu.
Już ustanowione połączenia nie są cofane samą zmianą praw pliku.

Odbiór izolowanych konfiguracji Linux/OpenBSD i jego granice opisuje
`implementation notes (not distributed)`. Nie oznacza on migracji
jakiegokolwiek serwera produkcyjnego.
