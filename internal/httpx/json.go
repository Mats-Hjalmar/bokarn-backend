package httpx

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/joakimcarlsson/minmux/router"
)

// maxBodyBytes caps a request body. Every payload this API accepts is a few
// kilobytes; the limit is a guard against an oversized body, not a business
// rule.
const maxBodyBytes = 1 << 20

// decodeJSON reads a JSON request body, rejecting unknown fields so a
// misspelled key is an error rather than a silently ignored instruction.
func decodeJSON(c *router.Context, into any) error {
	dec := json.NewDecoder(io.LimitReader(c.Request.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errors.New("malformed JSON body: " + err.Error())
	}
	return nil
}
