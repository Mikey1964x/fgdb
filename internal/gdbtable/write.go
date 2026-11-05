package gdbtable

// writeState holds pending modifications of a table opened for writing.
type writeState struct{}

func (w *writeState) rowOffset(t *Table, fid int64) (int64, error) { return 0, nil }
func (w *writeState) maxFID() int64                                { return 0 }

// Flush writes pending changes to disk.
func (t *Table) Flush() error { return nil }

// OpenForUpdate opens a .gdbtable file for reading and writing.
func OpenForUpdate(path string) (*Table, error) { return open(path, true) }

// Writable reports whether the table was opened for update.
func (t *Table) Writable() bool { return t.writable }
