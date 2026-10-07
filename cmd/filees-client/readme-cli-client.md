# filees-client

Jednorazowy klient. Nie jest demonem. Nie zna półek, szuflad,
udziałów ani wehikułu czasu. Wehikuł czasu jest osobnym, późniejszym krokiem.

Katalog opublikowany przez `ksefpuck` (`target.root`, np.
`Faktury/YYYY/MM/…pdf`) może być zwykłym katalogiem bez `.svn`.
`ksefpuck` zostaje przy plikach i nie uruchamia FileES. Cron robi to sam,
na końcu istniejącego łańcucha:

```
0 9,15,20 * * * /usr/local/bin/ksefuck --notify && /usr/local/bin/syschat --drain-queue && /usr/local/bin/pdfuck && /usr/local/bin/ksefpuck && /usr/local/bin/filees-client sync --config /etc/filees/filees-client.conf --wc wc-01 -nwc --message ksefpuck
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

Checkout i sync nie przyjmują ścieżki źródłowej ani URL z linii poleceń.
`--wc` to identyfikator z configu. Bez `-nwc` jego `path` musi być WC:

```
filees-client checkout --config /etc/filees/filees-client.conf --wc wc-01
filees-client sync --config /etc/filees/filees-client.conf --wc wc-01
```

Zwykła aktywacja desktopowa wystarcza dla trybu bez WC:

```
filees-client sync --config /etc/filees/filees-client.conf --wc wc-01 -nwc
filees-client sync --config /etc/filees/filees-client.conf --wc wc-01 -nwc -restore-set
filees-client sync --config /etc/filees/filees-client.conf --wc wc-01 -nwc -reverse-reconcile-set
```

`-nwc` (alias `--no-working-copy`) odczytuje zwykły katalog z `path`.
Przez `svnmucc` dodaje pliki nowe, aktualizuje istniejące o innej treści
i tworzy brakujące katalogi. Identyczne bajty pomija. Nie pobiera zmian i
nie propaguje lokalnych usunięć. Jeśli ścieżka pliku lub jej katalog
nadrzędny była usunięta z repo, domyślnie jej nie przywraca.
`-restore-set` pozwala ponownie dodać takie pliki, o ile nadal są w
źródle. `-reverse-reconcile-set` robi przeciwny kierunek: pobiera jedną
rewizję repo i doprowadza lokalne `path` do jej stanu, włącznie z
nadpisaniem różnych plików i usunięciem lokalnych ścieżek nieobecnych w
repo. Obie dodatkowe flagi wymagają `-nwc` i nie mogą być łączone.
Żaden z tych przebiegów nie tworzy `.svn`.

To zachowanie programu, nie osobne prawo serwera. Klucz normalnej
aktywacji desktopowej zachowuje wszystkie swoje uprawnienia. Dla `-nwc`
URL ma wskazywać korzeń repozytorium danych, a `log_dir` musi leżeć poza
źródłem. Pliki symboliczne i nieregularne kończą przebieg błędem.

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
svnmucc=/usr/local/bin/svnmucc
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

`svn`, `svnmucc` i `ssh` mają te domyślne ścieżki, gdy kluczy nie ma. Nie są brane z
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

Osobne artefakty Linux/OpenBSD buduje `cmd/filees-client/build.sh`.
Skrypt nie publikuje ich do kanału i nie dodaje klienta do paczki serwera.

## Import dumpa do nowego repo

```
filees-client dump-load --profile /var/lib/filees/servers/ID/client-profile.json \
  --dump /abs/archive.dump --name NoweRepo --target /home/user/nowe-wc \
  --state /var/lib/filees-client/import-001
```

`--state` to prywatny katalog jednej operacji. Jego rodzic musi istnieć, a
sam katalog powinien być nowy. W razie przerwania ponawia się **dokładnie te
same flagi**; zapisany identyfikator żądania odtwarza wynik serwera, zamiast
tworzyć drugie repo. Klient kopiuje źródłowy dump do nośnika, tworzy nowe
repo przez istniejący kanał sterowania, publikuje jeden commit z dumpem,
wywołuje `LOAD_REPOSITORY_DUMP` i dopiero po potwierdzeniu nowej generacji
zakłada nową WC w `--target`. Ścieżka docelowa musi być wolna. Istniejące
repo i WC nie są nadpisywane. Konto musi mieć serwerowe prawo tworzenia repo.

Ten przebieg przeszedł syntetyczny test natywny OpenBSD z utratą odpowiedzi
po podmianie generacji, ale nie jest jeszcze odebrany z rzeczywistą aktywacją
FileES na serwerze. Szczegóły i granice są w raporcie.

## Whale

```
filees-client whale-put --profile /abs/client-profile.json --state /abs/whales \
  --repo UUID --path katalog/duzy.bin --source /abs/duzy.bin
filees-client whale-get-quote --profile /abs/client-profile.json --state /abs/whales \
  --repo UUID --path katalog/duzy.bin --revision 12 --dest /abs/nowy.bin
filees-client whale-get-confirm --profile /abs/client-profile.json --state /abs/whales --id OPERATION_ID
```

Quote podaje rozmiar i ID, nie uruchamia pobrania bez `whale-get-confirm`.
`whale-status --id`, `whale-retry --id` i `whale-resume --id` korzystają z
tego samego `--profile` oraz `--state`; resume uruchamia wszystkie niedokończone
operacje w tym katalogu i czeka na wskazaną. Polecenia nie uruchamiają demona.
Trwały stan transferu pozostaje w prywatnym katalogu `--state`. Transport
Whale korzysta z przypiętego klucza i `known_hosts` profilu aktywacji.
Odbiór z rzeczywistym serwerem FileES pozostaje do wykonania.

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
