package ast

import (
	"encoding/json"
	"fmt"
	"github.com/viant/parsly"
	"strings"
)

type ParameterHint struct {
	Parameter string
	Hint      string
}

type ParameterHints []*ParameterHint

func (p *ParameterHints) Index() map[string]*ParameterHint {
	var result = make(map[string]*ParameterHint)
	for i, item := range *p {
		result[item.Parameter] = (*p)[i]
	}
	return result
}

func RemoveParameterHints(text string, hints ParameterHints) string {
	var pairs = []string{}
	for _, v := range hints {
		pairs = append(pairs, v.Hint, "")
	}
	replacer := strings.NewReplacer(pairs...)
	return replacer.Replace(text)
}

func ExtractParameterHints(text string) ParameterHints {
	var hints = make([]*ParameterHint, 0)
outer:
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '$':
			selExpr := ExtractSelector(text[i:])
			if selExpr == "" {
				continue outer
			}
			candidate := text[i+len(selExpr):]
			if hint := ExtractHint(candidate); hint != "" {
				hints = append(hints, &ParameterHint{Parameter: selExpr[1:], Hint: hint})
			}
		}
	}
	return hints
}

func ExtractHint(text string) string {
	cursor := parsly.NewCursor("", []byte(text), 0)
	matched := cursor.MatchAfterOptional(whitespaceMatcher, commentBlockMatcher)
	if matched.Code == commentBlockToken {
		return matched.Text(cursor)
	}
	return ""
}

func UnmarshalHint(hint string, dest interface{}) (string, error) {
	hint = hint[3 : len(hint)-3]

	index := strings.LastIndex(hint, "}")
	result := ""
	if index != -1 {
		result = hint[index+1:]
		hint = hint[:index+1]
	} else {
		return hint, nil
	}

	err := json.Unmarshal([]byte(hint), dest)
	if err != nil {
		return "", fmt.Errorf("invalid %s, %w", hint, err)
	}
	return strings.TrimSpace(result), err
}