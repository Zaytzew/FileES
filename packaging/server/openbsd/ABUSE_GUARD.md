# Ochrona źródeł błędnych prób dostępu

Stan źródeł: baza r1724 + zmiana z 2026-09-29. To instrukcja opcjonalnego
uruchomienia, nie potwierdzenie instalacji. Nie zmieniać produkcyjnej zapory
bez testu na osobnej VM i sprawdzenia rzeczywistej drogi HTTP/proxy.

## Ochrona w filees-links

Domyślnie 12 rzeczywiście błędnych haseł z jednego IP w oknie 10 minut
powoduje odrzucanie **wszystkich kolejnych żądań tego źródła** przez 10 minut.
Nie jest to blokada udziału. Obejmuje też poprawne hasło i posiadacza ważnego
linku wizyty z tego samego IP; właściciel zaakceptował koszt wspólnego NAT-u.
Próg jest stałą bieżącego wydania, nie nowym parametrem administratora.
Odmowy nie przedłużają okresu blokady. Licznik dotyczy wszystkich udziałów
w jednym procesie. Poprawne hasło nie zeruje wcześniejszych niepowodzeń.

Odrzucenie następuje przed backendem i Argon2, z HTTP 429, Retry-After
i no-store. Pozostałe źródła nadal mają dostęp. Limit pamięci: 4096 źródeł,
do dwóch równoległych prób na źródło, istniejące cztery globalne sloty Argon2.
Zapełnienie tablicy albo slotów daje 503, nie dopisuje błędnego hasła.
Nie naliczamy także błędnego formatu weryfikatora ani konfiguracji udziału.
Zajęte próby zwalniają rezerwację również na błędnych ścieżkach.
Zadanie usługi usuwa wygasłe wpisy co minutę także bez ruchu.

IP, liczniki i terminy są wyłącznie w RAM. Restart zeruje ochronę aplikacyjną.
Nie ma Redis, plików stanu, dziennika adresów, ciasteczka rozpoznającego
napastnika ani opóźniania zajętych wykonawców przez sleep.
To nie jest ochrona przed DDoS, botnetem, zmianą IP ani próbami poniżej progu.
OTP ma własne ograniczenia; w tej iteracji nie dopisujemy jego odmów do tego
licznika i nie traktujemy każdego 404 jako próby włamania.

## Adres odbiorcy i granica zaufania

Bez proxy używany jest REMOTE_ADDR przekazany przez zaufany serwer FastCGI.
Nagłówki X-Forwarded-For od zwykłego odbiorcy są ignorowane. Sam prywatny
adres pośrednika nie nadaje mu zaufania.

Opcjonalny fragment `public-links.json`:

```json
"abuse": {
  "trusted_proxies": ["127.0.0.1"],
  "signal_socket": "/var/run/filees-abuse/signal.sock"
}
```

Lista zawiera **dokładne adresy**, bez CIDR. Pośrednik musi usunąć nagłówek
od klienta i wstawić pojedynczy rzeczywisty IP. Łańcuch, powtórzony nagłówek,
brak IP albo adres samego zaufanego proxy oznacza 503, nie blokadę proxy.
Nie wpisywać przykładowego loopback bez sprawdzenia konfiguracji. Jeżeli
httpd widzi tylko adres proxy, a proxy nie jest zadeklarowane, wszystkie
żądania będą miały wspólny licznik. To warunek kontroli przed wdrożeniem.
Nie wystawiać FastCGI na niezaufaną sieć.

## Prywatny sygnał i oddzielny strażnik PF

`signal_socket` jest opcjonalny. Bez niego ochrona aplikacyjna działa,
a usługa nie otwiera żadnego dodatkowego gniazda. Administrator przygotowuje
katalog 0700 należący do konta filees-links. Gniazdo otrzymuje 0600.
Przykład dla instalacji używającej `_filees-public` (najpierw sprawdzić konto):

```sh
install -d -o _filees-public -g _filees-public -m 700 /var/run/filees-abuse
```

Po połączeniu gniazdo wysyła JSON z IP i czasem wygaśnięcia aktualnych blokad,
następnie zamyka połączenie. Nie przenosi URL, haseł, tokenów ani komend.
Jest dostępne lokalnemu administratorowi i kontu usługi, nie przez HTTP.
Obecność starego gniazda blokuje uruchomienie; po awarii administrator usuwa
je dopiero po upewnieniu się, że poprzedni proces nie działa.

`cmd/filees-links-pf` jest **osobnym, opcjonalnym narzędziem**. Bieżący
skrypt bundla jeszcze go nie pakuje i instalator nie uruchamia go automatycznie.
Na OpenBSD budowa ze sprawdzonych źródeł:

```sh
go build -o filees-links-pf ./cmd/filees-links-pf
```

Administrator instaluje binarium poza zapisywalnymi katalogami usługi.
Uruchamia je jako root raz na minutę. Usługa WWW nie otrzymuje dostępu do
`pfctl`, doas, sudo ani `/dev/pf`.

Strażnik zmienia wyłącznie dedykowaną tablicę `filees_bad_sources` przez
`pfctl -t ... -T replace -f -`. IP przekazuje przez stdin, bez pliku.
Potem usuwa istniejące stany: `pfctl -k IP_źródła -k IP_serwera`.
`-destination` przyjmuje do ośmiu adresów oddzielonych przecinkami;
dla hosta dual-stack podać zarówno IPv4, jak i IPv6. Usuwanie stanów łączy
tylko adresy tej samej rodziny. Pominięty adres docelowy nie będzie objęty
usuwaniem istniejących stanów.
**Usuwanie stanów obejmuje wszystkie porty między tym źródłem a wskazanym
serwerem, nie tylko HTTPS.** Dlatego wymagane są jawne wyłączenia adresów
administracji i proxy. Prywatne, loopback i link-local są wyłączone w tym
narzędziu zawsze; ochrona aplikacyjna nadal je obejmuje. Sam adres docelowy
także trafia na listę chronioną. Nie jest to nieomylne rozpoznawanie napastnika.

Przykładowe reguły do **ręcznego połączenia** z istniejącą konfiguracją PF:

```pf
table <filees_bad_sources> persist
block in quick on egress proto tcp from <filees_bad_sources> to (egress) port { 80, 443 }
```

Reguła musi poprzedzać odpowiednie `pass quick`. Bez tej reguły sama tablica
niczego nie blokuje. Nie używać `log` w tej regule, jeśli IP mają pozostać
wyłącznie w stanie ulotnym. Nie zapisujemy tablic do pliku ani do pfsync
na niesprawdzone hosty. Logowanie infrastruktury pozostaje polityką admina.

Wpis crona po podstawieniu **rzeczywistych** adresów i ścieżki binarium:

```cron
* * * * * /usr/local/sbin/filees-links-pf -socket /var/run/filees-abuse/signal.sock -destination 203.0.113.10 -protect 198.51.100.20,203.0.113.11
* * * * * /sbin/pfctl -t filees_bad_sources -T expire 600 >/dev/null 2>&1
```

Drugi wpis jest bezpiecznikiem po awarii pierwszego programu; nie zerować
statystyk tej tablicy, bo `expire` korzysta z czasu utworzenia/wyzerowania.
Brak/błędny sygnał usuwa zawartość tylko tej tablicy, nie innych blokad PF.
Błędy mają komunikat ogólny; wyjście pfctl nie trafia do dziennika.
Wyłączenie: usunąć te dwa wpisy i wyczyścić **wyłącznie** tablicę poleceniem
`pfctl -t filees_bad_sources -T flush`.

Granice: opóźnienie PF do następnego przebiegu crona (zwykle do minuty),
usługa odrzuca nowe żądania od razu. Zatrzymanie całego crona może pozostawić
wpisy PF — tablica nie ma własnego automatycznego TTL. Alarmować brak crona
i zapewnić dostęp administracyjny spoza objętego blokadą adresu. Przy bardzo
wielu źródłach limit 20 s pracy może przerwać usuwanie stanów; tablica jest
aktualizowana najpierw, ale nie gwarantujemy przerwania wszystkich starych sesji.
Za zewnętrznym reverse proxy lokalny PF nie widzi IP odbiorcy; obecny strażnik
nie obsługuje zdalnej zapory. Nie blokować połączenia proxy→httpd.

Referencje: [pfctl(8)](https://man.openbsd.org/pfctl.8),
[pf.conf(5)](https://man.openbsd.org/pf.conf.5).
