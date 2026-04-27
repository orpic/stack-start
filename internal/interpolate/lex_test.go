package interpolate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenize_PlainText(t *testing.T) {
	tokens, err := Tokenize("hello world")
	require.NoError(t, err)
	require.Equal(t, []Token{{Literal: true, Value: "hello world"}}, tokens)
}

func TestTokenize_SingleRef(t *testing.T) {
	tokens, err := Tokenize("${cloudflared.url}")
	require.NoError(t, err)
	require.Equal(t, []Token{{Literal: false, Value: "cloudflared.url"}}, tokens)
}

func TestTokenize_RefInMiddle(t *testing.T) {
	tokens, err := Tokenize("https://${cloudflared.url}/path")
	require.NoError(t, err)
	require.Len(t, tokens, 3)
	require.Equal(t, Token{Literal: true, Value: "https://"}, tokens[0])
	require.Equal(t, Token{Literal: false, Value: "cloudflared.url"}, tokens[1])
	require.Equal(t, Token{Literal: true, Value: "/path"}, tokens[2])
}

func TestTokenize_MultipleRefs(t *testing.T) {
	tokens, err := Tokenize("${a.x} and ${b.y}")
	require.NoError(t, err)
	require.Len(t, tokens, 3)
	require.Equal(t, Token{Literal: false, Value: "a.x"}, tokens[0])
	require.Equal(t, Token{Literal: true, Value: " and "}, tokens[1])
	require.Equal(t, Token{Literal: false, Value: "b.y"}, tokens[2])
}

func TestTokenize_DollarEscape(t *testing.T) {
	tokens, err := Tokenize("$$50")
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	require.Equal(t, Token{Literal: true, Value: "$"}, tokens[0])
	require.Equal(t, Token{Literal: true, Value: "50"}, tokens[1])
}

func TestTokenize_DoubleDollarInMiddle(t *testing.T) {
	tokens, err := Tokenize("price is $$100 total")
	require.NoError(t, err)
	require.Len(t, tokens, 3)
	require.Equal(t, Token{Literal: true, Value: "price is "}, tokens[0])
	require.Equal(t, Token{Literal: true, Value: "$"}, tokens[1])
	require.Equal(t, Token{Literal: true, Value: "100 total"}, tokens[2])
}

func TestTokenize_UnclosedBrace(t *testing.T) {
	tokens, err := Tokenize("${unclosed")
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.True(t, tokens[0].Literal)
	require.Equal(t, "${unclosed", tokens[0].Value)
}

func TestTokenize_BareDollar(t *testing.T) {
	tokens, err := Tokenize("$notaref")
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	require.Equal(t, Token{Literal: true, Value: "$"}, tokens[0])
	require.Equal(t, Token{Literal: true, Value: "notaref"}, tokens[1])
}

func TestTokenize_Empty(t *testing.T) {
	tokens, err := Tokenize("")
	require.NoError(t, err)
	require.Empty(t, tokens)
}

func TestTokenize_EnvrcRef(t *testing.T) {
	tokens, err := Tokenize("${envrc.DATABASE_URL}")
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.Equal(t, Token{Literal: false, Value: "envrc.DATABASE_URL"}, tokens[0])
}
