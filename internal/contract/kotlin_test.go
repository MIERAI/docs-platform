package contract

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const ktAPI = `package shop.shelf.service

import org.eclipse.microprofile.graphql.GraphQLApi
import org.eclipse.microprofile.graphql.Name
import org.eclipse.microprofile.graphql.Query

/** Shelves. A "quote" and a nested /* comment */ here. */
@GraphQLApi
class ShelfGraphQL(private val engine: Engine) {
    val label = "x \"y\" ${engine.name} {"

    @Query("shelves")
    fun findShelves(@Name("input") filter: ShelfInput, limit: Int = 10): List<ShelfDto> {
        return engine.find(filter).map { it.toDto() }
    }

    @Query
    fun getShelfCount(zone: Long?): Int = 0

    @Query
    @Name("clock")
    fun now(): Uni<ClockDto?> = TODO()

    @Mutation
    fun setShelfName(code: String, name: String): Boolean = true

    fun helper(x: Int): Int = x

    private fun toDto(): ShelfDto = TODO()
}

data class ShelfInput(
    val zone: Int,
    val codes: List<String>? = null,
    @Name("dims") val size: Size?,
)

data class Size(val width: Double, val depth: Double)

data class ShelfDto(
    val code: String,
    @Name("title") val name: String?,
    val rows: List<RowDto> = emptyList(),
    private val secret: String = "",
) {
    val computed: Int get() = 1
    private val hidden: Int = 0
}

data class RowDto(val position: Int, val shelf: ShelfDto?)

class ClockDto(val now: java.time.LocalDateTime)
`

func TestParseKotlin(t *testing.T) {
	kf := parseKotlin("shelf/src/main/kotlin/Shelf.kt", ktAPI)
	var names []string
	for _, c := range kf.Classes {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "ShelfGraphQL,ShelfInput,Size,ShelfDto,RowDto,ClockDto" {
		t.Errorf("classes %v", names)
	}
	var fns []string
	for _, f := range kf.Funs {
		fns = append(fns, f.Class+"."+f.Name+":"+f.Return)
	}
	want := "ShelfGraphQL.findShelves:List<ShelfDto>,ShelfGraphQL.getShelfCount:Int,ShelfGraphQL.now:Uni<ClockDto?>,ShelfGraphQL.setShelfName:Boolean,ShelfGraphQL.helper:Int,ShelfGraphQL.toDto:ShelfDto"
	if strings.Join(fns, ",") != want {
		t.Errorf("funs %v", fns)
	}
}

func TestKotlinGraphQL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "shelf/src/main/kotlin/Shelf.kt", ktAPI)
	writeFile(t, dir, "shelf/src/test/kotlin/ShelfTest.kt", "@GraphQLApi class T { @Query fun ghost(): Int = 0 }")
	src, err := OpenSource(context.Background(), "code", dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := KotlinGraphQL(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Operation{}
	for _, op := range s.Operations {
		got[op.Kind+":"+op.Name] = op
	}
	for _, k := range []string{"query:shelves", "query:shelfCount", "query:clock", "mutation:shelfName"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in %v", k, keys(got))
		}
	}
	if len(got) != 4 {
		t.Errorf("ops %v", keys(got))
	}
	sh := got["query:shelves"]
	if sh.Group != "shelf" {
		t.Errorf("group %q", sh.Group)
	}
	wantIn := []Field{
		{Name: "input", Type: "obj:ShelfInput", Presence: Required, Fields: []Field{
			{Name: "codes", Type: "[String]", Presence: Optional},
			{Name: "dims", Type: "obj:Size", Presence: Optional, Fields: []Field{{Name: "depth", Type: "Float", Presence: Required}, {Name: "width", Type: "Float", Presence: Required}}},
			{Name: "zone", Type: "Int", Presence: Required},
		}},
		{Name: "limit", Type: "Int", Presence: Optional},
	}
	if !reflect.DeepEqual(sh.Inputs, wantIn) {
		t.Errorf("inputs %+v", sh.Inputs)
	}
	if p := fieldPaths(sh.Outputs, ""); strings.Join(p, ",") != "code,computed,rows,rows.position,rows.shelf,title" {
		t.Errorf("outputs %v", p)
	}
	if c := got["query:shelfCount"]; c.Inputs[0].Type != "BigInteger" || c.Inputs[0].Presence != Optional {
		t.Errorf("Long? argument %+v", c.Inputs)
	}
	if c := got["query:clock"]; len(c.Outputs) != 1 || c.Outputs[0].Type != "DateTime" {
		t.Errorf("Uni<ClockDto?> outputs %+v", c.Outputs)
	}
}

func keys(m map[string]Operation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestKtGraphQLType(t *testing.T) {
	for in, want := range map[string]string{
		"Int": "Int", "Long?": "BigInteger", "List<String?>": "[String]", "Set<List<Int>>": "[[Int]]",
		"CompletionStage<List<Foo>>": "[obj:Foo]", "java.math.BigDecimal": "BigDecimal",
		"Map<String, Int?>": "obj:Map", "IntArray": "[Int]", "": "",
	} {
		if got := ktGraphQLType(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func FuzzParseKotlin(f *testing.F) {
	f.Add(ktAPI)
	f.Add("@GraphQLApi class A { @Query fun (")
	f.Add("class B(val a: Map<String, List<Pair<Int, (Int) -> Unit>>>, @Name(\"x\") val b: Int?)")
	f.Add("\"${\"${\"x\"}\"}\" /* /* */")
	f.Fuzz(func(_ *testing.T, s string) {
		kf := parseKotlin("a/src/main/kotlin/A.kt", s)
		r := ktResolver{classes: map[string][]ktClass{}}
		for _, c := range kf.Classes {
			r.classes[c.Name] = append(r.classes[c.Name], c)
		}
		for _, fn := range kf.Funs {
			_, _ = r.operation(fn, map[string]bool{fn.Class: true}, kf)
		}
	})
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
