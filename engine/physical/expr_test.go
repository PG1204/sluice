package physical

import (
	"testing"

	"github.com/PG1204/sluice/engine/ast"
	"github.com/PG1204/sluice/engine/parser"
	"github.com/PG1204/sluice/engine/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLayout/testBatch provide one row with a column of each type plus a NULL
// integer column, for evaluating compiled expressions.
func testLayout() layout {
	return layout{cols: []colInfo{
		{name: "i", typ: storage.TypeInt64},
		{name: "f", typ: storage.TypeFloat64},
		{name: "s", typ: storage.TypeString},
		{name: "b", typ: storage.TypeBool},
		{name: "n", typ: storage.TypeInt64},
	}}
}

func testBatch() *storage.Batch {
	schema := testLayout().schema()
	return buildBatch(schema, 1, func(col, _ int) Value {
		switch col {
		case 0:
			return Int(10)
		case 1:
			return Float(2.5)
		case 2:
			return Str("hi")
		case 3:
			return Bool(true)
		default:
			return Null() // column "n"
		}
	})
}

// compile parses "<src>" as a SELECT expression and compiles it against the
// test layout.
func compile(t *testing.T, src string) evalExpr {
	t.Helper()
	stmt, err := parser.Parse("SELECT " + src + " FROM t")
	require.NoError(t, err)
	e := stmt.(*ast.SelectStatement).Columns[0].Expr
	ce, err := compileExpr(e, testLayout())
	require.NoError(t, err)
	return ce
}

func evalSrc(t *testing.T, src string) Value {
	return compile(t, src).eval(testBatch(), 0)
}

func TestEval_LiteralsAndColumns(t *testing.T) {
	assert.Equal(t, Int(42), evalSrc(t, "42"))
	assert.Equal(t, Float(3.5), evalSrc(t, "3.5"))
	assert.Equal(t, Str("x"), evalSrc(t, "'x'"))
	assert.Equal(t, Bool(true), evalSrc(t, "TRUE"))
	assert.True(t, evalSrc(t, "NULL").IsNull())

	assert.Equal(t, Int(10), evalSrc(t, "i"))
	assert.Equal(t, Float(2.5), evalSrc(t, "f"))
	assert.True(t, evalSrc(t, "n").IsNull())
}

func TestEval_Arithmetic(t *testing.T) {
	assert.Equal(t, Int(13), evalSrc(t, "i + 3"))
	assert.Equal(t, Int(7), evalSrc(t, "i - 3"))
	assert.Equal(t, Int(20), evalSrc(t, "i * 2"))
	assert.Equal(t, Int(5), evalSrc(t, "i / 2"))      // integer division
	assert.Equal(t, Float(12.5), evalSrc(t, "i + f")) // int+float widens to float
	assert.Equal(t, Float(25), evalSrc(t, "i * f"))   // 10 * 2.5
}

func TestEval_DivisionByZeroIsNull(t *testing.T) {
	assert.True(t, evalSrc(t, "i / 0").IsNull())
	assert.True(t, evalSrc(t, "f / 0").IsNull())
}

func TestEval_NullPropagation(t *testing.T) {
	assert.True(t, evalSrc(t, "n + 1").IsNull())
	assert.True(t, evalSrc(t, "n > 1").IsNull())
	assert.True(t, evalSrc(t, "-n").IsNull())
}

func TestEval_Comparisons(t *testing.T) {
	assert.Equal(t, Bool(true), evalSrc(t, "i > 5"))
	assert.Equal(t, Bool(false), evalSrc(t, "i < 5"))
	assert.Equal(t, Bool(true), evalSrc(t, "i = 10"))
	assert.Equal(t, Bool(true), evalSrc(t, "i >= f")) // 10 >= 2.5 across types
	assert.Equal(t, Bool(true), evalSrc(t, "s = 'hi'"))
	assert.Equal(t, Bool(true), evalSrc(t, "s < 'hz'"))
}

func TestEval_ThreeValuedLogic(t *testing.T) {
	// FALSE AND NULL = FALSE; TRUE OR NULL = TRUE; TRUE AND NULL = NULL.
	assert.Equal(t, Bool(false), evalSrc(t, "i < 0 AND n = 1"))
	assert.Equal(t, Bool(true), evalSrc(t, "i > 0 OR n = 1"))
	assert.True(t, evalSrc(t, "i > 0 AND n = 1").IsNull())
	assert.True(t, evalSrc(t, "i < 0 OR n = 1").IsNull())
}

func TestEval_NotAndNeg(t *testing.T) {
	assert.Equal(t, Bool(false), evalSrc(t, "NOT b"))
	assert.Equal(t, Bool(true), evalSrc(t, "NOT (i < 0)"))
	assert.Equal(t, Int(-10), evalSrc(t, "-i"))
}

func TestCompile_UnknownColumn(t *testing.T) {
	stmt, err := parser.Parse("SELECT missing FROM t")
	require.NoError(t, err)
	_, err = compileExpr(stmt.(*ast.SelectStatement).Columns[0].Expr, testLayout())
	assert.Error(t, err)
}

func TestLayout_AmbiguousColumn(t *testing.T) {
	l := layout{cols: []colInfo{
		{qualifier: "a", name: "id", typ: storage.TypeInt64},
		{qualifier: "b", name: "id", typ: storage.TypeInt64},
	}}
	_, _, err := l.indexOf("", "id")
	assert.ErrorContains(t, err, "ambiguous")

	// Qualifying resolves it.
	idx, _, err := l.indexOf("b", "id")
	require.NoError(t, err)
	assert.Equal(t, 1, idx)
}
