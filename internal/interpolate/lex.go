package interpolate

// Token represents a segment of a string being interpolated.
type Token struct {
	Literal bool
	Value   string // literal text or reference key (e.g. "cloudflared.url" or "envrc.DATABASE_URL")
}

// Tokenize splits a string into literal and reference tokens.
// References use ${producer.name} syntax. $$ is an escape for a literal $.
func Tokenize(input string) ([]Token, error) {
	var tokens []Token
	i := 0
	n := len(input)

	for i < n {
		if input[i] == '$' {
			if i+1 < n && input[i+1] == '$' {
				tokens = append(tokens, Token{Literal: true, Value: "$"})
				i += 2
				continue
			}
			if i+1 < n && input[i+1] == '{' {
				end := -1
				for j := i + 2; j < n; j++ {
					if input[j] == '}' {
						end = j
						break
					}
				}
				if end == -1 {
					tokens = append(tokens, Token{Literal: true, Value: input[i:]})
					i = n
					continue
				}
				ref := input[i+2 : end]
				tokens = append(tokens, Token{Literal: false, Value: ref})
				i = end + 1
				continue
			}
			tokens = append(tokens, Token{Literal: true, Value: "$"})
			i++
			continue
		}

		start := i
		for i < n && input[i] != '$' {
			i++
		}
		tokens = append(tokens, Token{Literal: true, Value: input[start:i]})
	}

	return tokens, nil
}
