package postgrescompat

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// StringArray scans PostgreSQL text arrays into a Go string slice.
type StringArray []string

// Array returns a database/sql value for a PostgreSQL scalar slice and a scanner
// for a one-dimensional string slice.
func Array(value any) interface {
	driver.Valuer
	sql.Scanner
} {
	return arrayValue{target: value}
}

// Value implements driver.Valuer for StringArray.
func (a StringArray) Value() (driver.Value, error) {
	return arrayValue{target: []string(a)}.Value()
}

// Scan implements sql.Scanner for StringArray.
func (a *StringArray) Scan(src any) error {
	return scanStringArray(src, a)
}

type arrayValue struct {
	target any
}

func (a arrayValue) Value() (driver.Value, error) {
	value, err := indirectValue(a.target)
	if err != nil {
		return nil, err
	}
	if !value.IsValid() {
		return nil, nil
	}
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return nil, fmt.Errorf("postgres array value must be a slice or array, got %T", a.target)
	}
	if value.Kind() == reflect.Slice && value.IsNil() {
		return nil, nil
	}

	encodeValue := value.Interface()
	if uuidValues, ok := uuidArrayStrings(value); ok {
		encodeValue = uuidValues
	}
	encoded, err := pgtype.NewMap().Encode(0, pgtype.TextFormatCode, encodeValue, nil)
	if err != nil {
		return nil, fmt.Errorf("encode PostgreSQL text array: %w", err)
	}
	return string(encoded), nil
}

func uuidArrayStrings(value reflect.Value) ([]string, bool) {
	if value.Type().Elem() != reflect.TypeOf(uuid.UUID{}) {
		return nil, false
	}

	values := make([]string, value.Len())
	for i := range values {
		values[i] = value.Index(i).Interface().(uuid.UUID).String()
	}
	return values, true
}

func (a arrayValue) Scan(src any) error {
	var values StringArray
	if err := scanStringArray(src, &values); err != nil {
		return err
	}

	target := reflect.ValueOf(a.target)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("postgres array scan target must be a non-nil pointer")
	}
	destination := target.Elem()
	if destination.Kind() != reflect.Slice || destination.Type().Elem().Kind() != reflect.String {
		return fmt.Errorf("postgres array scan target must be a pointer to a string slice, got %T", a.target)
	}

	result := reflect.MakeSlice(destination.Type(), len(values), len(values))
	for i, value := range values {
		result.Index(i).SetString(value)
	}
	destination.Set(result)
	return nil
}

func scanStringArray(src any, destination *StringArray) error {
	if src == nil {
		*destination = nil
		return nil
	}

	var raw []byte
	switch value := src.(type) {
	case string:
		raw = []byte(value)
	case []byte:
		raw = value
	default:
		return fmt.Errorf("cannot scan %T into PostgreSQL string array", src)
	}

	var decoded pgtype.Array[string]
	if err := pgtype.NewMap().Scan(pgtype.TextArrayOID, pgtype.TextFormatCode, raw, &decoded); err != nil {
		return fmt.Errorf("decode PostgreSQL string array: %w", err)
	}
	*destination = StringArray(decoded.Elements)
	return nil
}

func indirectValue(value any) (reflect.Value, error) {
	result := reflect.ValueOf(value)
	for result.IsValid() && result.Kind() == reflect.Pointer {
		if result.IsNil() {
			return reflect.Value{}, nil
		}
		result = result.Elem()
	}
	return result, nil
}

// QuoteIdentifier quotes a PostgreSQL identifier for trusted, dynamically generated SQL.
func QuoteIdentifier(value string) string {
	if index := strings.IndexByte(value, 0); index >= 0 {
		value = value[:index]
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// QuoteLiteral quotes a PostgreSQL literal for trusted, dynamically generated SQL.
func QuoteLiteral(value string) string {
	value = strings.ReplaceAll(value, `'`, `''`)
	if strings.ContainsRune(value, '\\') {
		value = strings.ReplaceAll(value, `\`, `\\`)
		return ` E'` + value + `'`
	}
	return `'` + value + `'`
}
