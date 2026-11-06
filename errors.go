package fgdb

import "errors"

// Errors returned by the package. They correspond to the FGDB_E_* codes of
// the Esri FileGDB API; use errors.Is to test for them.
var (
	// ErrNotFound: the dataset, table, item or domain does not exist
	// (FGDB_E_TABLE_NOT_FOUND, FGDB_E_ITEM_NOT_FOUND, FGDB_E_DOMAIN_NOT_FOUND).
	ErrNotFound = errors.New("fgdb: not found")
	// ErrFieldNotFound: no field has the given name (FGDB_E_FIELD_NOT_FOUND).
	ErrFieldNotFound = errors.New("fgdb: field not found")
	// ErrRowNotFound: the row does not exist (FGDB_E_ROW_NOT_FOUND).
	ErrRowNotFound = errors.New("fgdb: row not found")
	// ErrNullValue: the field value is NULL.
	ErrNullValue = errors.New("fgdb: value is null")
	// ErrTypeMismatch: the field type does not match the accessor used
	// (FGDB_E_FIELD_INVALID_TYPE / FGDB_E_FEATURE_VALUE_TYPE_MISMATCH).
	ErrTypeMismatch = errors.New("fgdb: field type mismatch")
	// ErrNotNullable: a NULL was assigned to a non-nullable field
	// (FGDB_E_FIELD_NOT_NULLABLE).
	ErrNotNullable = errors.New("fgdb: field is not nullable")
	// ErrInvalidSQL: the SQL statement or where clause could not be parsed
	// (FGDB_E_INVALID_SQL).
	ErrInvalidSQL = errors.New("fgdb: invalid SQL")
	// ErrReadOnly: the geodatabase or table was opened read-only
	// (FGDB_E_WORKSPACE_READONLY).
	ErrReadOnly = errors.New("fgdb: geodatabase is read-only")
	// ErrNotSupported: the operation or data is not supported by this
	// implementation (FGDB_E_NOT_SUPPORTED).
	ErrNotSupported = errors.New("fgdb: not supported")
	// ErrAlreadyExists: a dataset, field or index with that name exists
	// (FGDB_E_TABLE_ALREADY_EXISTS, FGDB_E_FIELD_ALREADY_EXISTS, ...).
	ErrAlreadyExists = errors.New("fgdb: already exists")
	// ErrInvalidArgument: an argument is out of range or malformed
	// (FGDB_E_INVALIDARG).
	ErrInvalidArgument = errors.New("fgdb: invalid argument")
	// ErrClosed: the object has been closed.
	ErrClosed = errors.New("fgdb: closed")
)
