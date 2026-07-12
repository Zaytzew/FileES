package talk

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Level int32

const (
	Silent Level = iota
	Error
	Warn
	Info
	Debug
	Trace
)

var (
	curLevel atomic.Int32
	outMu    sync.Mutex
	out      io.Writer = os.Stderr
	prefix             = "" // globalny prefix, np. nazwa programu
)

func init() {
	curLevel.Store(int32(Info)) // domyślnie „małomówny”
	if lvl := os.Getenv("FILEES_LOG"); lvl != "" {
		SetLevelString(lvl)
	}
	if p := os.Getenv("FILEES_LOG_PREFIX"); p != "" {
		prefix = p
	}
}

func SetLevel(l Level)           { curLevel.Store(int32(l)) }
func SetOutput(w io.Writer)      { outMu.Lock(); out = w; outMu.Unlock() }
func SetLevelString(s string) {
	switch strings.ToLower(s) {
	case "silent", "off", "0":
		SetLevel(Silent)
	case "error", "err", "1":
		SetLevel(Error)
	case "warn", "warning", "2":
		SetLevel(Warn)
	case "info", "3":
		SetLevel(Info)
	case "debug", "4":
		SetLevel(Debug)
	case "trace", "5":
		SetLevel(Trace)
	default:
		SetLevel(Info)
	}
}

type Logger struct {
	scope string            // np. repoID albo „client/svn”
	tags  map[string]string // opcjonalne tagi statyczne
}

func With(scope string, tags ...[2]string) Logger {
	m := map[string]string{}
	for _, kv := range tags {
		m[kv[0]] = kv[1]
	}
	return Logger{scope: scope, tags: m}
}

func (l Logger) logf(lv Level, emoji, format string, args ...any) {
	if lv > Level(curLevel.Load()) { return }
	ts := time.Now().UTC().Format("15:04:05")
	tagStr := ""
	if len(l.tags) > 0 {
		first := true
		for k, v := range l.tags {
			if first { tagStr += " " ; first = false }
			tagStr += k + "=" + v + " "
		}
		tagStr = strings.TrimSpace(tagStr)
		if tagStr != "" { tagStr = " [" + tagStr + "]" }
	}
	scope := ""
	if l.scope != "" { scope = " " + l.scope }
	pfx := ""
	if prefix != "" { pfx = prefix + " " }
	line := fmt.Sprintf("%s %s%s%s: %s\n", ts, pfx, emoji, scope, tagStr+" "+fmt.Sprintf(format, args...))
	outMu.Lock()
	fmt.Fprint(out, line)
	outMu.Unlock()
}

func (l Logger) Errorf(f string, a ...any) { l.logf(Error, "❌", f, a...) }
func (l Logger) Warnf(f string, a ...any)  { l.logf(Warn,  "⚠️", f, a...) }
func (l Logger) Infof(f string, a ...any)  { l.logf(Info,  "ℹ️", f, a...) }
func (l Logger) Debugf(f string, a ...any) { l.logf(Debug, "🐞", f, a...) }
func (l Logger) Tracef(f string, a ...any) { l.logf(Trace, "🔬", f, a...) }

// Wygodne funkcje globalne (dla szybkich podmian bez wstrzykiwania loggera)
var root = With("")

func Errorf(f string, a ...any) { root.Errorf(f, a...) }
func Warnf(f string, a ...any)  { root.Warnf(f, a...) }
func Infof(f string, a ...any)  { root.Infof(f, a...) }
func Debugf(f string, a ...any) { root.Debugf(f, a...) }
func Tracef(f string, a ...any) { root.Tracef(f, a...) }
