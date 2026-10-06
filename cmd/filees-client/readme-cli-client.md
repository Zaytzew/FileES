# filees-client

Jednorazowy klient kopii roboczej. Nie jest demonem. Nie zna półek, szuflad,
udziałów ani wehikułu czasu. Wehikuł czasu jest osobnym, późniejszym krokiem.

Służy temu, żeby katalog opublikowany przez `ksefpuck` (`target.root`, układ
`YYYY/MM/…pdf`) był kopią roboczą FileES w realmie właściciela. `ksefpuck`
zostaje przy plikach i nie uruchamia FileES. Cron robi to sam, na końcu
istniejącego łańcucha:

```
0 9,15,20 * * * /usr/local/bin/ksefuck --notify && /usr/local/bin/syschat --drain-queue && /usr/local/bin/pdfuck && /usr/local/bin/ksefpuck && /usr/local/bin/filees-client sync --config /etc/filees/filees-client.conf --wc wc-01 --message ksefpuck
```

`&&` zostaje. Sync rusza tylko wtedy, gdy `ksefpuck` skończy z kodem 0.

## Polecenia

Aktywacja nowej instalacji albo join do istniejącego realmu (bilet z
`--join-realm-alias` to ten sam przebieg, nie osobny program):

```
filees-client activate-begin --server-id ID --server HOST --known-hosts /abs/known_hosts --state-root /abs/state --email a@b.c
filees-client activate-finish --server-id ID --server HOST --known-hosts /abs/known_hosts --state-root /abs/state
filees-client activate-resume --server-id ID --server HOST --known-hosts /abs/known_hosts --state-root /abs/state
```

`activate-finish` czyta jedno hasło jednorazowe ze standardowego wejścia.

Checkout i sync nie przyjmują ścieżki kopii ani URL z linii poleceń. `--wc`
to identyfikator z configu:

```
filees-client checkout --config /etc/filees/filees-client.conf --wc wc-01
filees-client sync --config /etc/filees/filees-client.conf --wc wc-01
```

`sync` najpierw rozpoznaje i planuje lokalne usunięcia, potem robi `svn update`,
dodaje nowe pliki (także całe nowe `YYYY/MM/`) i commituje zmiany. Domyślny
komunikat to `FileES sync`, a KSEFUCK podaje własny przez `--message`.
Konflikt lub symlink w nowej zawartości kończy przebieg błędem bez commitu.
Po konflikcie `svn update` może pozostawić lokalne artefakty do ręcznego
rozstrzygnięcia. Czysta kopia wypisuje `clean`.
Blokada `.svn/filees-client.lock` odmawia równoległego przebiegu tej samej WC.

## Config

```
[tools]
svn=/usr/local/bin/svn
ssh=/usr/bin/ssh
log_dir=/var/log/filees-client

[realm]
identity=/var/filees/id_ed25519
known_hosts=/var/filees/known_hosts
port=2223
host=
only=!wc-01 !wc-02

[wc "wc-01"]
path=/home/ksefUCK/published
url=svn+ssh://_filees-client@host/books

[wc "wc-02"]
path=/home/ksefUCK/other
url=svn+ssh://_filees-client@host/other
```

`svn` i `ssh` mają te domyślne ścieżki, gdy kluczy nie ma. Nie są brane z
odziedziczonego `PATH`: przed wywołaniem SVN klient ogranicza `PATH` do
katalogu skonfigurowanego pliku `ssh`. Sekcje `[wc "…"]` są kopiami realmu. Bez `only` dozwolone są wszystkie
wpisane kopie i żadna inna. `only=!wc-01 !wc-02` zostawia tylko te dwie.
Identyfikator spoza listy jest odrzucany, zanim wystartuje `svn`.

`log_dir` musi istnieć przed uruchomieniem i być zapisywalny dla konta
wywołującego cron. Dziennik `wc-01.jsonl` ma uprawnienia 0600 i rotuje po
5 MiB do jednej poprzedniej generacji. Zawiera czas, etap i wynik, bez
sekretów, ścieżek plików i tekstów błędów. Przy braku zapisu sync odmawia
przed dotknięciem SVN. Na OpenBSD ten katalog jest również prywatnym `HOME`
procesu SVN, więc może zawierać jego `.subversion`.

Osobne artefakty Linux/OpenBSD buduje `packaging/build-filees-client.sh`.
Skrypt nie publikuje ich do kanału i nie dodaje klienta do paczki serwera.
Dump/load i Whale są nadal w opracowaniu; brak odpowiadających poleceń CLI.

## Sandbox

Na Windowsie nie ma `pledge` ani `unveil`. Lista z configu i tak obowiązuje.

Na OpenBSD, po sprawdzeniu `--wc`, proces i wywołane przez niego `svn` oraz
`ssh` dostają `pledge` (`ApplyForExec`, obietnice potomka obejmują `prot_exec`).
`unveil` obejmuje skonfigurowane binarki, klucz, `known_hosts`, dozwolone
kopie oraz odczyt ich katalogów nadrzędnych. Systemowy SVN wymaga odczytu
tych rodziców przy kanonizacji WC. Dlatego inne pliki pod takim rodzicem
mogą być czytelne dla procesu działającego jako to samo konto; konfiguracja
CLI nadal nie pozwala wybrać ich jako celu sync. Dla ścisłej lokalnej
izolacji należy uruchamiać klienta na dedykowanym koncie i trzymać jego WC
w dedykowanym drzewie, bez cudzych danych w katalogach nadrzędnych.

To nie jest sandbox odziedziczony po procesie `ksefpuck`. Tamten zamyka się
zanim cron dojdzie do następnej komendy i nie odsłania klucza ani `ssh`.
`filees-client` zakłada własny kaganiec tego samego rodzaju, na kopie z configu.
