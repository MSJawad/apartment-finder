// Package input reads what visitors submit: JSON forms, checked strictly,
// with an error message for each invalid field.
package input

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ErrMalformed means a submission couldn't be read at all, for example
// because it isn't JSON. Problems with single fields are FieldErrors.
var ErrMalformed = errors.New("malformed input")

// FieldErrors maps each invalid field, by its JSON name, to a message for
// the person who filled it in.
type FieldErrors map[string]string

func (e FieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for field := range e {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	for i, field := range fields {
		fields[i] = field + ": " + e[field]
	}
	return "invalid fields: " + strings.Join(fields, "; ")
}

// Decode decodes one JSON object from data into v. A misspelt field or
// trailing data is an error rather than silently ignored. A value of the
// wrong type, such as text for a number, is a FieldErrors for that field;
// anything else unreadable wraps ErrMalformed.
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && typeErr.Field != "" {
			return FieldErrors{typeErr.Field: "Must be a " + typeErr.Type.String() + "."}
		}
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: data after the JSON object", ErrMalformed)
	}
	return nil
}

// Text returns s with runs of whitespace collapsed to single spaces and
// none at either end.
func Text(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
