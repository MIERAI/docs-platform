package contract

import "strings"

// tableTypes maps table → column → type id, read from CREATE TABLE statements.
type tableTypes map[string]map[string]string

var columnStop = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "REFERENCES": true, "CHECK": true,
	"GENERATED": true, "UNIQUE": true, "CONSTRAINT": true, "COLLATE": true,
}

var tableConstraint = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "CHECK": true, "FOREIGN": true, "EXCLUDE": true, "LIKE": true,
}

// parseSchema reads the column types of every CREATE TABLE in src into tt.
func parseSchema(src string, tt tableTypes) {
	toks := lexSQL(src)
	setDepths(toks)
	for i := 0; i+2 < len(toks); i++ {
		if toks[i].Up != "CREATE" || toks[i+1].Up != "TABLE" {
			continue
		}
		j := i + 2
		if j+2 < len(toks) && toks[j].Up == "IF" && toks[j+1].Up == "NOT" && toks[j+2].Up == "EXISTS" {
			j += 3
		}
		if j+3 < len(toks) && toks[j+1].Text == "." {
			j += 2 // schema.table
		}
		if j+1 >= len(toks) || toks[j+1].Text != "(" {
			continue
		}
		cols := tt[toks[j].Text]
		if cols == nil {
			cols = map[string]string{}
			tt[toks[j].Text] = cols
		}
		i = tableColumns(toks, j+1, cols)
	}
}

// tableColumns reads "(col type ..., ...)" starting at the opening parenthesis; it returns
// the index of the closing one.
func tableColumns(toks []sqlTok, open int, cols map[string]string) int {
	d := toks[open].Depth
	start := open + 1
	for k := open + 1; k < len(toks); k++ {
		t := toks[k]
		end := t.Depth == d && t.Text == ")"
		if (t.Depth == d+1 && t.Text == ",") || end {
			addColumn(toks[start:k], cols)
			start = k + 1
		}
		if end {
			return k
		}
	}
	return len(toks) - 1
}

func addColumn(item []sqlTok, cols map[string]string) {
	if len(item) < 2 || tableConstraint[item[0].Up] || (item[0].Kind != tkIdent && item[0].Kind != tkQIdent) {
		return
	}
	var parts []string
	for _, t := range item[1:] {
		if t.Kind != tkIdent || columnStop[t.Up] {
			break
		}
		parts = append(parts, strings.ToLower(t.Text))
	}
	suffix := ""
	for k := 1; k+1 < len(item); k++ {
		if item[k].Text == "[" && item[k+1].Text == "]" {
			suffix = "[]"
			break
		}
	}
	cols[item[0].Text] = sqlType(strings.Join(parts, " ") + suffix)
}
