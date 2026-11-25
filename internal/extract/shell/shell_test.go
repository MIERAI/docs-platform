package shell

import (
	"reflect"
	"testing"

	"github.com/MIERAI/docs-platform/internal/extract/extracttest"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name, path, src string
		want            []string
	}{
		{"inputs and flags", "scripts/deploy.sh", `#!/bin/bash
# uses $NOT_FROM_COMMENT
TARGET="${TARGET:-kind}"
LOCAL=1
echo "$LOCAL $API_URL $HOME ${GITHUB_SHA}"
while getopts "vf:" o; do :; done
case "$1" in
  --dry-run|-n) DRY=1 ;;
  --only=*) ONLY="${1#*=}" ;;
esac
for ITEM in a b; do echo "$ITEM"; done
read -r ANSWER
echo "$ANSWER $DRY $ONLY"
`, []string{
			"cli_flag --dry-run [deploy] public", "cli_flag --only [deploy] public", "cli_flag -f [deploy] public",
			"cli_flag -n [deploy] public", "cli_flag -v [deploy] public",
			"env_var API_URL [deploy] public", "env_var TARGET [deploy] public", "program deploy",
		}},
		{"extension-less without shebang is skipped", "bin/data", "TARGET=${X:-1}\n", []string{}},
		{"extension-less with shebang", "bin/run", "#!/usr/bin/env sh\necho $REGION\n", []string{"env_var REGION [run] public", "program run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extracttest.Symbols(t, Extractor{}, c.path, c.src)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("symbols:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}
