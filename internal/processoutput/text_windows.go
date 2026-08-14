//go:build windows

package processoutput

import (
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Windows command-line programs do not share one universal output encoding.
// Modern tools often write UTF-8, while Subversion built for the Windows GUI
// locale writes redirected human-readable output in the system ANSI code
// page (GetACP; CP1250 on the Polish test machine). Text has already ruled
// out UTF-8 before calling this fallback.
func platformText(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	const mbErrInvalidChars = 0x00000008
	codePage := windows.GetACP()
	wanted, err := windows.MultiByteToWideChar(codePage, mbErrInvalidChars, &data[0], int32(len(data)), nil, 0)
	if err != nil || wanted <= 0 {
		return replacementText(data)
	}
	wide := make([]uint16, wanted)
	written, err := windows.MultiByteToWideChar(codePage, mbErrInvalidChars, &data[0], int32(len(data)), &wide[0], int32(len(wide)))
	if err != nil || written != wanted {
		return replacementText(data)
	}
	return string(utf16.Decode(wide))
}
