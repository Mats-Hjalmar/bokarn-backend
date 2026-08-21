package notify

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Render fills a template from fields.
//
// missingkey=error is the whole reason this is not a string replace: an
// operator who writes {{.Reference}} where the code supplies {{.Ref}} gets a
// failed delivery naming the field, which is fixable, rather than a guest
// receiving a confirmation with a blank booking number, which is not.
func Render(t Template, fields map[string]string) (Message, error) {
	subject, err := renderOne(t.Key+".subject", t.Subject, fields)
	if err != nil {
		return Message{}, err
	}
	body, err := renderOne(t.Key+".body", t.Body, fields)
	if err != nil {
		return Message{}, err
	}
	return Message{Subject: subject, Body: body}, nil
}

func renderOne(
	name, text string,
	fields map[string]string,
) (string, error) {
	tmpl, err := template.New(name).
		Option("missingkey=error").
		Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", name, err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, fields); err != nil {
		return "", asMissingField(name, err)
	}
	return out.String(), nil
}

// asMissingField turns text/template's message into something an operator can
// act on. The library reports a missing map key as a long internal string; the
// field name is the only part that helps.
func asMissingField(name string, err error) error {
	const marker = `map has no entry for key "`
	msg := err.Error()
	i := strings.Index(msg, marker)
	if i < 0 {
		return fmt.Errorf("render %s: %w", name, err)
	}
	rest := msg[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return fmt.Errorf("render %s: %w", name, err)
	}
	return MissingFieldError{Key: name, Field: rest[:j]}
}
