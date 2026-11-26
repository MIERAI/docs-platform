package judgev1

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// maxParseText bounds the text the tolerant parser looks at.
const maxParseText = 1 << 20

// maxTailTries bounds how many shorter tails ending in "}" the tolerant parser tries.
const maxTailTries = 64

var (
	reThink      = regexp.MustCompile(`(?s)<think>.*?</think>`)
	reFence      = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")
	reTripleStop = regexp.MustCompile(`\}\s*\}\s*\}(\s*,)`)
)

// ParseObject reads one JSON object from a model's reply. structured is the provider's
// schema-enforced output, if any (used as is when it is an object: ParseSchema). Otherwise
// text is searched: a fenced block, then the span from the first "{" to the last "}", then
// shorter tails ending in "}" (ParseFallback); last, stray closing braces are removed
// (ParseRepaired). No object: nil, ParseFailed.
func ParseObject(structured json.RawMessage, text string) (json.RawMessage, string) {
	if isObject(structured) {
		return compact(structured), ParseSchema
	}
	if len(text) > maxParseText {
		text = text[:maxParseText]
	}
	text = reThink.ReplaceAllString(text, "")
	var cands []string
	if m := reFence.FindStringSubmatch(text); m != nil {
		cands = append(cands, m[1])
	}
	i := strings.Index(text, "{")
	if i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			cands = append(cands, text[i:j+1])
		}
	}
	for _, c := range cands {
		if o := firstObject(c); o != nil {
			return o, ParseFallback
		}
	}
	if i < 0 {
		return nil, ParseFailed
	}
	if o := repair(text[i:]); o != nil {
		return o, ParseRepaired
	}
	return nil, ParseFailed
}

// firstObject tries c, then progressively shorter prefixes of c that end in "}".
func firstObject(c string) json.RawMessage {
	if isObject(json.RawMessage(c)) {
		return compact(json.RawMessage(c))
	}
	end := len(c) - 1
	for range maxTailTries {
		j := strings.LastIndex(c[:end], "}")
		if j < 0 {
			return nil
		}
		if s := json.RawMessage(c[:j+1]); isObject(s) {
			return compact(s)
		}
		end = j
	}
	return nil
}

// repair drops stray closing braces ("}}}," becomes "}},"), a mistake models make in deeply
// nested answers, up to four times.
func repair(s string) json.RawMessage {
	for range 4 {
		loc := reTripleStop.FindStringSubmatchIndex(s)
		if loc == nil {
			return nil
		}
		s = s[:loc[0]] + "}}" + s[loc[2]:]
		if j := strings.LastIndex(s, "}"); j >= 0 {
			if o := json.RawMessage(s[:j+1]); isObject(o) {
				return compact(o)
			}
		}
	}
	return nil
}

func isObject(b json.RawMessage) bool {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return false
	}
	return true
}

func compact(b json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	if err := json.Compact(&out, bytes.TrimSpace(b)); err != nil {
		return nil
	}
	return out.Bytes()
}

// NewStructuredResponse fills the response of a structured task (repo_drift, claim_facts,
// doc_facts): the verdict stays unknown (the core derives it from the answer), the answer is
// passed through, and an answer that could not be parsed becomes a bad_reply failure.
func NewStructuredResponse(req *Request, answer json.RawMessage, parse string, m Model, u Usage) *Response {
	if parse == ParseFailed || len(answer) == 0 {
		r := Failure(req, m, ErrCodeBadReply, "no answer object in model reply")
		r.AnswerParse = ParseFailed
		r.Usage = u
		return r
	}
	return &Response{
		SchemaVersion: SchemaVersion, ID: req.ID, Verdict: Unknown, Model: m, PromptHash: req.Prompt.Hash,
		Usage: u, Answer: answer, AnswerParse: parse,
	}
}
