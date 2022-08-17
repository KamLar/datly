package ast

import "github.com/viant/parsly"

func ExtractSelector(text string) string {
	cursor := parsly.NewCursor("", []byte(text), 0)
	for i := 0; i < len(text); i++ {
		match := cursor.MatchAfterOptional(whitespaceMatcher, selectorMatcher)
		result := match.Text(cursor)
		if match.Code == selectorToken {
			match = cursor.MatchAny(exprGroupMatcher, scopeBlockMatcher)
			switch match.Code {
			case exprGroupToken, scopeBlockToken:
				result += match.Text(cursor)
			}

			return result
		}
	}
	return ""
}
