package servertool

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"filees/pkg/serverconfig"
	"filees/public-shares/channel"
)

// shareSummary is what an operator may see about a published link.
//
// The password hash and the recipient tokens are deliberately absent. The
// question this answers is "what does this server publish, and how is it
// gated" - never "what is the secret". An operator holding the disk could read
// the record file directly, so this is not a security boundary; it is a
// statement about what the tool is for, and it keeps the credential out of
// terminal scrollback, shell history and pasted bug reports.
type shareSummary struct {
	Address   string `json:"address"`
	ChannelID string `json:"channel_id"`
	State     string `json:"state"`
	Gate      string `json:"gate"`
	Revision  string `json:"revision"`
	Objects   int    `json:"objects"`
	UpdatedAt string `json:"updated_at"`
}

func summariseShare(record channel.Record) shareSummary {
	summary := shareSummary{
		Address:   record.Alias + "/" + record.Slug,
		ChannelID: record.ChannelID,
		State:     record.State,
		Gate:      "otwarte",
		Revision:  "śledzi HEAD",
		UpdatedAt: record.UpdatedAt.UTC().Format("2006-01-02 15:04"),
	}
	// A withdrawn record has had its manifest cleared, so everything below is
	// genuinely unknown rather than empty. Saying "-" is the honest answer; a
	// zero object count would read as "published nothing".
	if record.Manifest == nil {
		summary.Gate, summary.Revision = "-", "-"
		return summary
	}
	switch {
	case len(record.Manifest.Recipients) > 0:
		summary.Gate = fmt.Sprintf("mail+OTP (%d)", len(record.Manifest.Recipients))
	case record.Manifest.Password != "":
		summary.Gate = "hasło"
	}
	if record.Manifest.DoNotFollow != nil {
		summary.Revision = fmt.Sprintf("przypięte r%d", *record.Manifest.DoNotFollow)
	}
	summary.Objects = len(record.Manifest.Objects)
	return summary
}

func writeShareTable(out io.Writer, summaries []shareSummary) {
	if len(summaries) == 0 {
		fmt.Fprintln(out, "ten serwer nie publikuje żadnych udostępnień")
		return
	}
	width := len("ADRES")
	for _, s := range summaries {
		if n := len([]rune(s.Address)); n > width {
			width = n
		}
	}
	fmt.Fprintf(out, "%-*s  %-8s  %-14s  %-14s  %5s  %s\n", width, "ADRES", "STAN", "BRAMKA", "REWIZJA", "PLIKI", "ZMIENIONO")
	for _, s := range summaries {
		objects := fmt.Sprintf("%d", s.Objects)
		if s.Revision == "-" {
			objects = "-"
		}
		fmt.Fprintf(out, "%-*s  %-8s  %-14s  %-14s  %5s  %s\n", width, s.Address, s.State, s.Gate, s.Revision, objects, s.UpdatedAt)
	}
	fmt.Fprintf(out, "\nrazem: %d\n", len(summaries))
}

func publicShareStore(config serverconfig.Config) (*channel.Store, error) {
	if !config.PublicShares.Enabled {
		return nil, fmt.Errorf("public shares are not enabled in this server's configuration")
	}
	return &channel.Store{Root: config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)}, nil
}

func runShareList(path string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("share list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit the listing as JSON instead of a table")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
		return adminUsage(stderr, flags, "")
	}
	_, config, err := openFiles(path, toolAccess{name: "filees-admin/share-list", needPublicShareState: true})
	if err != nil {
		report(stderr, "filees-admin config", err)
		return ExitConfig
	}
	store, err := publicShareStore(config)
	if err != nil {
		report(stderr, "filees-admin share list", err)
		return ExitConfig
	}
	records, err := store.ListAll()
	if err != nil {
		return adminError(stderr, err)
	}
	summaries := make([]shareSummary, 0, len(records))
	for _, record := range records {
		summaries = append(summaries, summariseShare(record))
	}
	if *asJSON {
		if err := writeJSON(stdout, summaries); err != nil {
			return ExitSoftware
		}
		return ExitOK
	}
	writeShareTable(stdout, summaries)
	return ExitOK
}

func runShareDelete(path string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("share delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	channelID := flags.String("channel-id", "", "channel UUID, as printed by share list")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || strings.TrimSpace(*channelID) == "" {
		return adminUsage(stderr, flags, "--channel-id UUID")
	}
	// Writable, unlike the listing. The two commands differ by one field here
	// and that is the point of splitting the flag.
	_, config, err := openFiles(path, toolAccess{name: "filees-admin/share-delete", needPublicShareState: true, publicShareStateWrite: true})
	if err != nil {
		report(stderr, "filees-admin config", err)
		return ExitConfig
	}
	store, err := publicShareStore(config)
	if err != nil {
		report(stderr, "filees-admin share delete", err)
		return ExitConfig
	}
	record, err := store.DeleteAsOperator(strings.TrimSpace(*channelID))
	if err != nil {
		return adminError(stderr, err)
	}
	// Naming the address rather than only the UUID, because the operator picked
	// the UUID off a listing and the address is the thing they can recognise as
	// the one they meant to take down.
	fmt.Fprintf(stdout, "wycofano %s/%s (%s)\n", record.Alias, record.Slug, record.ChannelID)
	fmt.Fprintln(stdout, "adres pozostaje zajęty, żeby stary link nigdy nie wskazał na nową treść")
	return ExitOK
}
