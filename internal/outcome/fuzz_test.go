package outcome

import (
	"strings"
	"testing"
)

// FuzzRead: a stream comes back from a cache or an artifact; a broken one is an error, never
// a panic.
func FuzzRead(f *testing.F) {
	f.Add(`{"schema_version":"1.0","at":"t","repo":"r","path":"a.md","line":1,"kind":"reported"}` + "\n\n")
	f.Add(`{"schema_version":"2.0"}`)
	f.Add("{")
	f.Fuzz(func(_ *testing.T, s string) {
		_, _ = Read(strings.NewReader(s))
	})
}

// FuzzLabelsFromCSV: the audit sheet is edited by hand and in spreadsheets.
func FuzzLabelsFromCSV(f *testing.F) {
	f.Add("claim_id,repo,human_verdict,path,line,doc_text\nc,r,tp,a.md,3,'=x\n")
	f.Add("\ufeffclaim_id,repo,human_verdict,path,line\nc,r,maybe,a.md,x\n")
	f.Add("\"unterminated")
	f.Fuzz(func(_ *testing.T, s string) {
		_, _ = LabelsFromCSV(strings.NewReader(s), now)
	})
}
