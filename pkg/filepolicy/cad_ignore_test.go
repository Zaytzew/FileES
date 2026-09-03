package filepolicy

import "testing"

// FileES is for teams working on binary files, so CAD is the flagship case -
// and this list had nothing for it until 2026-09-03, only office and developer
// clutter. Measured in a live project: an open drawing kept dwl and dwl2
// cycling through the watcher for hours, and one path that vanished between
// being seen and being committed failed the whole batch, holding 32 MB of real
// work behind it.
func TestAutoCADChurnIsIgnored(t *testing.T) {
	for _, rel := range []string{
		// Lock files: they exist only while a drawing is open and name whoever
		// opened it - a cruder answer to the question reservations answer.
		"LLW_zestawienie-stolarki.dwl",
		"LLW_zestawienie-stolarki.dwl2",
		"01_WYDANIE/rysunek.dwl",
		// Autosave and temporary drawings, transient by design.
		"projekt.sv$",
		"projekt.ac$",
	} {
		if !IsBuiltinIgnored(rel) {
			t.Fatalf("%q is CAD churn and must never reach a commit batch", rel)
		}
	}
}

// The drawings themselves are the whole point of the product and must never be
// caught by a pattern meant for the noise around them.
func TestDrawingsThemselvesAreNeverIgnored(t *testing.T) {
	for _, rel := range []string{
		"LLW_zestawienie-stolarki.dwg",
		"01_WYDANIE/LLW_zestawienie-stolarki-WITRYNY-OKIENNE.pdf",
		"Zegrze-3d.udatasmith",
		"model.dxf",
	} {
		if IsBuiltinIgnored(rel) {
			t.Fatalf("%q is the work itself and must be versioned", rel)
		}
	}
}

// Office writes the same kind of churn as CAD does, and the owner files were
// already covered by ~$* while the backups and autorecovery were not.
func TestOfficeChurnIsIgnored(t *testing.T) {
	for _, rel := range []string{
		"~$W_Kiwerska_Opis_Techniczny.docx",
		"01_EDITABLES/Kiwerska_Opis.wbk",
		"Kiwerska_Opis.asd",
		"zestawienie.xlk",
		"baza.laccdb",
		"~WRD0001.tmp",
	} {
		if !IsBuiltinIgnored(rel) {
			t.Fatalf("%q is application churn and must never reach a commit batch", rel)
		}
	}
}

// The documents themselves, and anything that merely looks adjacent to the
// patterns, must survive. Excel's extensionless temporaries are not matched on
// purpose: no pattern for them exists that would not also swallow real files.
func TestDocumentsAndLookalikesSurvive(t *testing.T) {
	for _, rel := range []string{
		"W_Kiwerska_Opis_Techniczny.docx",
		"zestawienie.xlsx",
		"prezentacja.pptx",
		"opis.asdf",
		"A1B2C3D4",
	} {
		if IsBuiltinIgnored(rel) {
			t.Fatalf("%q is the work itself and must be versioned", rel)
		}
	}
}
