// Command manual-man-pages renders docs/man into the HTML manual.
//
//	go run ./tools/manual-man-pages
//
// Each mdoc page becomes manual/assets/man/<name>.<section>.html, rendered by
// mandoc(1) — the formatter OpenBSD itself uses — inside the manual's own
// page frame, plus an index. The generated pages record the SHA-256 of their
// source, so packaging/man_html_test.go notices a page that was edited
// without regenerating, without needing mandoc on the test host.
//
// New files need svn:keywords Rev like every other manual page:
//
//	svn propset svn:keywords Rev manual/assets/man/*.html
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Marker identifies a file this program owns; stale ones are removed.
const Marker = `<meta name="filees-generator" content="tools/manual-man-pages">`

type page struct {
	Name, Section, Source, Description, Digest string
}

func (p page) file() string { return p.Name + "." + p.Section + ".html" }

func main() {
	src := flag.String("src", filepath.Join("docs", "man"), "mdoc sources")
	out := flag.String("out", filepath.Join("manual", "assets", "man"), "generated HTML")
	mandoc := flag.String("mandoc", "mandoc", "mandoc command")
	flag.Parse()
	if err := run(*src, *out, *mandoc); err != nil {
		fmt.Fprintln(os.Stderr, "manual-man-pages:", err)
		os.Exit(1)
	}
}

var ndLine = regexp.MustCompile(`(?m)^\.Nd (.+)$`)

// Load lists the mdoc pages under src, sorted by section and name.
func Load(src string) ([]page, error) {
	files, err := filepath.Glob(filepath.Join(src, "man*", "*.*"))
	if err != nil {
		return nil, err
	}
	var pages []page
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(file)
		dot := strings.LastIndex(base, ".")
		nd := ndLine.FindSubmatch(data)
		if nd == nil {
			return nil, fmt.Errorf("%s has no .Nd line", file)
		}
		sum := sha256.Sum256(data)
		pages = append(pages, page{
			Name: base[:dot], Section: base[dot+1:], Source: filepath.ToSlash(file),
			Description: string(nd[1]), Digest: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].Section != pages[j].Section {
			return pages[i].Section < pages[j].Section
		}
		return pages[i].Name < pages[j].Name
	})
	if len(pages) == 0 {
		return nil, errors.New("no manual pages in " + src)
	}
	return pages, nil
}

// mandoc 1.14.6 writes <a class="Xr" href="…">; OpenBSD's newer mandoc adds
// attributes after href (aria-label), which must not turn an outside page back
// into a dead link.
var xrLink = regexp.MustCompile(`<a class="Xr" href="([^"]+)"[^>]*>([^<]*)</a>`)

func run(src, out, mandoc string) error {
	pages, err := Load(src)
	if err != nil {
		return err
	}
	ours := map[string]bool{}
	for _, p := range pages {
		ours[p.file()] = true
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	written := map[string]bool{"index.html": true}
	for _, p := range pages {
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(mandoc, "-T", "html", "-O", "fragment,man=%N.%S.html", p.Source)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("mandoc %s: %w: %s", p.Source, err, stderr.String())
		}
		// A page outside this set (sshd(8), svn(1)) has no HTML here; keep
		// its name as text rather than a link that leads nowhere.
		body := xrLink.ReplaceAllStringFunc(stdout.String(), func(link string) string {
			match := xrLink.FindStringSubmatch(link)
			if ours[match[1]] {
				return link
			}
			return `<span class="Xr">` + match[2] + `</span>`
		})
		title := p.Name + "(" + p.Section + ")"
		content := `<div class="man-page-head"><a href="index.html">All manual pages</a></div>
<div class="mandoc">
` + strings.TrimSpace(body) + `
</div>`
		if err := write(filepath.Join(out, p.file()), frame(title+" — FileES manual pages", p, content)); err != nil {
			return err
		}
		written[p.file()] = true
	}
	if err := write(filepath.Join(out, "index.html"), frame("FileES manual pages", page{Source: src, Digest: indexDigest(pages)}, index(pages))); err != nil {
		return err
	}
	// Remove what this program generated earlier for a page that is gone.
	existing, err := filepath.Glob(filepath.Join(out, "*.html"))
	if err != nil {
		return err
	}
	for _, file := range existing {
		if written[filepath.Base(file)] {
			continue
		}
		data, err := os.ReadFile(file)
		if err == nil && bytes.Contains(data, []byte(Marker)) {
			if err := os.Remove(file); err != nil {
				return err
			}
			fmt.Println("removed", file)
		}
	}
	return nil
}

// IndexDigest covers every source, so the index is stale when any page is.
func indexDigest(pages []page) string {
	h := sha256.New()
	for _, p := range pages {
		fmt.Fprintf(h, "%s %s %s\n", p.file(), p.Digest, p.Description)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func index(pages []page) string {
	sections := map[string]string{"5": "File formats", "7": "Overview", "8": "System administration"}
	var b strings.Builder
	b.WriteString(`<h1>FileES manual pages</h1>
<p>The OpenBSD manual pages of the FileES server, rendered by <code>mandoc</code> from <code>docs/man</code>. The same pages are installed with the server and are upgraded with it by <code>filees-install</code>: <code>man filees</code> on the server shows what this page shows. English is canonical. A shorter extract with examples is in the Tech chapter: <a href="../en/man-pages.html">EN</a> · <a href="../pl/man-pages.html">PL</a>.</p>
`)
	current := ""
	for _, p := range pages {
		if p.Section != current {
			if current != "" {
				b.WriteString("</dl>\n")
			}
			current = p.Section
			name := sections[p.Section]
			if name == "" {
				name = "Section " + p.Section
			}
			fmt.Fprintf(&b, "<h2>%s (%s)</h2>\n<dl class=\"man-index\">\n", html.EscapeString(name), p.Section)
		}
		fmt.Fprintf(&b, "<dt><a href=\"%s\"><code>%s(%s)</code></a></dt><dd>%s</dd>\n",
			p.file(), html.EscapeString(p.Name), p.Section, html.EscapeString(p.Description))
	}
	b.WriteString("</dl>")
	return b.String()
}

func frame(title string, p page, content string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta name="filees-source-revision" content="$Rev$">
` + Marker + `
<meta name="filees-mandoc-source" content="` + html.EscapeString(p.Source) + ` sha256:` + p.Digest + `">
<meta charset="utf-8"/>
<meta content="width=device-width, initial-scale=1.0" name="viewport"/>
<title>` + html.EscapeString(title) + `</title>
<link rel="stylesheet" href="../css/manual.css"/>
<link rel="stylesheet" href="../css/mandoc.css"/>
<link rel="icon" href="../../favicon.ico"/>
</head>
<body>
<main class="sheet">
<header class="masthead">
<div class="brand-lockup">
<div aria-hidden="true" class="brand-mark">f<span class="colon">:</span>s</div>
<div class="brand-copy">
<strong class="brand-name">filees<span class="colon">:</span>space</strong>
<span class="brand-subtitle">FileES system documentation</span>
</div>
</div>
<div class="masthead-meta">mandoc · docs/man · no network dependencies</div>
<nav class="lang-switch" aria-label="Language">
<a href="../../index.html" class="lang-home">filees:space</a>
<a href="../en/man-pages.html" lang="en" hreflang="en">EN</a>
<a href="../pl/man-pages.html" lang="pl" hreflang="pl">PL</a>
</nav>
</header>
<div class="manual-content man-content">
` + content + `
</div>
<footer class="manual-footer"><span>filees:space visual system · navy / orange / paper</span><span>Generated by mandoc from docs/man</span></footer>
</main></body></html>
`
}

func write(path, text string) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == text {
		return nil
	}
	fmt.Println("wrote", path)
	return os.WriteFile(path, []byte(text), 0o644)
}
