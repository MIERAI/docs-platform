package contract

import "strings"

// Types of the factory family are the generator's internal type ids: int32, int64, decimal,
// float, text, bool, date, timestamp, json; a list is "list<id>", a list of groups "tuples".

// designTypeIDs maps the design language's type names to type ids.
var designTypeIDs = map[string]string{
	"布尔": "bool", "十进制": "decimal", "小数": "float", "整数32": "int32", "整数64": "int64",
	"文本": "text", "日期": "date", "时间": "timestamp", "邮编": "text", "JSON": "json", "json": "json",
}

// designType converts a design type ("整数32列表" → list<int32>); "" when unknown.
func designType(s string) string {
	s = strings.TrimSpace(s)
	if s == "组列表" {
		return "tuples"
	}
	if base, ok := strings.CutSuffix(s, "列表"); ok {
		if id := designTypeIDs[base]; id != "" {
			return "list<" + id + ">"
		}
		return ""
	}
	if id := designTypeIDs[s]; id != "" {
		return id
	}
	// the factory's internal ids are accepted too
	switch s {
	case "bool", "decimal", "float", "int32", "int64", "text", "date", "timestamp":
		return s
	}
	return ""
}

// sqlTypeIDs maps PostgreSQL type names to type ids.
var sqlTypeIDs = map[string]string{
	"int4": "int32", "integer": "int32", "int": "int32", "int2": "int32", "smallint": "int32", "serial": "int32",
	"int8": "int64", "bigint": "int64", "bigserial": "int64",
	"numeric": "decimal", "decimal": "decimal",
	"float8": "float", "float4": "float", "real": "float", "double precision": "float",
	"text": "text", "varchar": "text", "character varying": "text", "char": "text", "character": "text", "citext": "text",
	"bool": "bool", "boolean": "bool",
	"date":        "date",
	"timestamptz": "timestamp", "timestamp": "timestamp", "timestamp with time zone": "timestamp", "timestamp without time zone": "timestamp",
	"json": "json", "jsonb": "json",
}

// sqlType converts a PostgreSQL type ("int4[]" → list<int32>); unknown names are kept as
// "sql:<name>" so that a change is still visible.
func sqlType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if base, ok := strings.CutSuffix(s, "[]"); ok {
		return "list<" + sqlType(base) + ">"
	}
	if id := sqlTypeIDs[s]; id != "" {
		return id
	}
	return "sql:" + s
}

// filterUnits maps the design language's filter units to the operators the SQL uses.
var filterUnits = map[string]string{
	"等于": "eq", "不等于": "ne", "大于": "gt", "大于等于": "gte", "小于": "lt", "小于等于": "lte",
	"属于": "in", "前缀匹配": "prefix", "有效期包含": "valid_at", "组属于": "in_tuples",
	"eq": "eq", "ne": "ne", "gt": "gt", "gte": "gte", "lt": "lt", "lte": "lte", "in": "in", "prefix": "prefix",
}

// designFilter converts "col 单元" to the filter string the code side produces
// ("sku eq"; "valid_from lte,valid_to gte" for a validity range; "" when not comparable).
func designFilter(s string) string {
	f := strings.Fields(s)
	if len(f) != 2 {
		return ""
	}
	op := filterUnits[f[1]]
	switch op {
	case "":
		return ""
	case "valid_at":
		from, to, ok := strings.Cut(f[0], "~")
		if !ok {
			return ""
		}
		return from + " lte," + to + " gte"
	case "in_tuples":
		return ""
	}
	return f[0] + " " + op
}

// orderWords are the design language's sort directions.
var orderWords = map[string]string{
	"小到大": "asc", "旧到新": "asc", "升序": "asc", "asc": "asc",
	"大到小": "desc", "新到旧": "desc", "降序": "desc", "desc": "desc",
}

// cmpWords are the comparison operators of fixed conditions.
var cmpWords = map[string]string{"=": "eq", "<>": "ne", "!=": "ne", "<": "lt", "<=": "lte", ">": "gt", ">=": "gte"}
