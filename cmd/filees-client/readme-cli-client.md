# filees-client

Jednorazowy klient kopii roboczej. Nie jest demonem. Nie zna półek, szuflad,
udziałów ani wehikułu czasu. Wehikuł czasu jest osobnym, późniejszym krokiem.

Służy temu, żeby katalog opublikowany przez `ksefpuck` (`target.root`, układ
`YYYY/MM/…pdf`) był kopią roboczą FileES w realmie właściciela. `ksefpuck`
zostaje przy plikach i nie uruchamia FileES. Cron robi to sam, na końcu
istniejącego łańcucha:

```
0 9,15,20 * * * /usr/local/bin/ksefuck --notify && /usr/local/bin/syschat --drain-queue && /usr/local/bin/pdfuck && /usr/local/bin/ksefpuck && /usr/local/bin/filees-client sync --config /etc/filees/filees-client.conf --wc wc-01
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

`sync` robi `svn update`, dodaje nowe pliki (także całe nowe `YYYY/MM/`) i
commituje z komunikatem `ksefpuck`. Konflikt, brak pliku albo symlink kończy
przebieg błędem i nic nie wysyła. Czysta kopia wypisuje `clean`.

## Config

```
[tools]
svn=/usr/local/bin/svn
ssh=/usr/bin/ssh

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
`PATH`. Sekcje `[wc "…"]` są kopiami realmu. Bez `only` dozwolone są wszystkie
wpisane kopie i żadna inna. `only=!wc-01 !wc-02` zostawia tylko te dwie.
Identyfikator spoza listy jest odrzucany, zanim wystartuje `svn`.

## Sandbox

Na Windowsie nie ma `pledge` ani `unveil`. Lista z configu i tak obowiązuje.

Na OpenBSD, po sprawdzeniu `--wc`, proces i wywołane przez niego `svn` oraz
`ssh` dostają `pledge` (`ApplyForExec`, obietnice potomka obejmują `prot_exec`).
`unveil` obejmuje skonfigurowane binarki, klucz, `known_hosts` i wyłącznie
dozwolone kopie. Obca kopia na tej maszynie nie jest widoczna.

To nie jest sandbox odziedziczony po procesie `ksefpuck`. Tamten zamyka się
zanim cron dojdzie do następnej komendy i nie odsłania klucza ani `ssh`.
`filees-client` zakłada własny kaganiec tego samego rodzaju, na kopie z configu.
