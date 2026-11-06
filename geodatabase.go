// Package fgdb is a pure Go driver for Esri File Geodatabases (.gdb
// directories).
//
// The API is modeled on the Esri FileGDB API 1.5 (Geodatabase, Table, Row,
// EnumRows, FieldDef, ShapeBuffer, ...), adapted to Go conventions: methods
// return values and errors instead of out-parameters and fgdbError codes,
// and overloads taking a field name or a field number are merged into
// name-based methods plus index-based Value/SetValue helpers.
//
// The on-disk format follows the reverse-engineered specification at
// https://github.com/rouault/dump_gdbtable/wiki/FGDB-Spec.
package fgdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// Dataset type names used by GetChildDatasets and related methods.
const (
	DatasetTypeFeatureClass   = "Feature Class"
	DatasetTypeTable          = "Table"
	DatasetTypeFeatureDataset = "Feature Dataset"
	DatasetTypeWorkspace      = "Workspace"
	DatasetTypeFolder         = "Folder"
	DatasetTypeCodedDomain    = "Coded Value Domain"
	DatasetTypeRangeDomain    = "Range Domain"
)

// Well-known item type UUIDs of GDB_ItemTypes.
var (
	itemTypeFolder         = mustGuid("{F3783E6F-65CA-4514-8315-CE3985DAD3B1}")
	itemTypeWorkspace      = mustGuid("{C673FE0F-7280-404F-8532-20755DD8FC06}")
	itemTypeFeatureDataset = mustGuid("{74737149-DCB5-4257-8904-B9724E32A530}")
	itemTypeFeatureClass   = mustGuid("{70737809-852C-4A03-9E22-2CECEA5B9BFA}")
	itemTypeTable          = mustGuid("{CD06BC3B-789D-4C51-AAFA-A467912B8965}")
	itemTypeCodedDomain    = mustGuid("{8C368B12-A12E-4C7E-9638-C9C64E69E98F}")
	itemTypeRangeDomain    = mustGuid("{C29DA988-8C3E-45F7-8B5C-18E51EE7BEB4}")
)

func mustGuid(s string) Guid {
	g, err := ParseGuid(s)
	if err != nil {
		panic(err)
	}
	return g
}

// Geodatabase is an open File Geodatabase (FileGDBAPI::Geodatabase).
// Its methods are safe for concurrent use; Table and EnumRows values are
// not.
type Geodatabase struct {
	path     string
	writable bool

	mu      sync.Mutex
	closed  bool
	catalog []catalogEntry // GDB_SystemCatalog rows
	cat     *catalogData   // GDB_Items and friends (nil for FileGDB v9)
	tables  map[*Table]struct{}
}

type catalogEntry struct {
	id         int64 // ObjectID; the table file is a%08x.gdbtable
	name       string
	fileFormat int32
}

// OpenGeodatabase opens an existing geodatabase read-only.
func OpenGeodatabase(path string) (*Geodatabase, error) {
	return openGeodatabase(path, false)
}

// OpenGeodatabaseForUpdate opens an existing geodatabase for reading and
// writing.
func OpenGeodatabaseForUpdate(path string) (*Geodatabase, error) {
	return openGeodatabase(path, true)
}

func openGeodatabase(path string, writable bool) (*Geodatabase, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("fgdb: %s is not a directory", path)
	}
	g := &Geodatabase{path: path, writable: writable, tables: map[*Table]struct{}{}}
	if err := g.loadCatalog(); err != nil {
		return nil, err
	}
	return g, nil
}

// Path returns the directory of the geodatabase.
func (g *Geodatabase) Path() string { return g.path }

// Close closes the geodatabase and all tables opened from it.
func (g *Geodatabase) Close() error {
	g.mu.Lock()
	tables := make([]*Table, 0, len(g.tables))
	for t := range g.tables {
		tables = append(tables, t)
	}
	g.closed = true
	g.mu.Unlock()
	var err error
	for _, t := range tables {
		t.refs = 1
		if e := t.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

func (g *Geodatabase) tableFile(id int64) string {
	return filepath.Join(g.path, fmt.Sprintf("a%08x.gdbtable", id))
}

func (g *Geodatabase) loadCatalog() error {
	t, err := gdbtable.Open(g.tableFile(1))
	if err != nil {
		return fmt.Errorf("fgdb: cannot open system catalog: %w", err)
	}
	defer t.Close()
	iName, iFormat := t.FieldIndex("Name"), t.FieldIndex("FileFormat")
	if iName < 0 {
		return fmt.Errorf("fgdb: system catalog has no Name field: %w", gdbtable.ErrCorrupt)
	}
	g.catalog = g.catalog[:0]
	for fid, err := t.NextFID(1); ; fid, err = t.NextFID(fid + 1) {
		if err != nil {
			return err
		}
		if fid == 0 {
			break
		}
		vals, err := t.ReadRow(fid)
		if err != nil {
			return err
		}
		e := catalogEntry{id: fid}
		e.name, _ = vals[iName].(string)
		if iFormat >= 0 {
			e.fileFormat, _ = vals[iFormat].(int32)
		}
		g.catalog = append(g.catalog, e)
	}
	cat, err := loadCatalogData(g)
	if err != nil {
		return err
	}
	g.cat = cat
	return nil
}

// findCatalog returns the system catalog entry for a table name.
func (g *Geodatabase) findCatalog(name string) (catalogEntry, bool) {
	for _, e := range g.catalog {
		if strings.EqualFold(e.name, name) {
			return e, true
		}
	}
	return catalogEntry{}, false
}

// openSystemTable opens a table of the catalog by name, read-only.
func (g *Geodatabase) openRawTable(name string) (*gdbtable.Table, error) {
	e, ok := g.findCatalog(name)
	if !ok {
		return nil, fmt.Errorf("%w: table %q", ErrNotFound, name)
	}
	return gdbtable.Open(g.tableFile(e.id))
}

// GetDatasetTypes returns the names of the item types known to the
// geodatabase, e.g. "Feature Class", "Table", "Feature Dataset".
func (g *Geodatabase) GetDatasetTypes() ([]string, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	if g.cat == nil {
		return []string{DatasetTypeTable, DatasetTypeFeatureClass}, nil
	}
	names := make([]string, 0, len(g.cat.itemTypes))
	for _, t := range g.cat.itemTypes {
		names = append(names, t.name)
	}
	sort.Strings(names)
	return names, nil
}

// GetDatasetRelationshipTypes returns the names of the item relationship
// types, e.g. "DatasetInFeatureDataset".
func (g *Geodatabase) GetDatasetRelationshipTypes() ([]string, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	if g.cat == nil {
		return nil, nil
	}
	names := make([]string, 0, len(g.cat.relTypes))
	for _, t := range g.cat.relTypes {
		names = append(names, t.name)
	}
	sort.Strings(names)
	return names, nil
}

// GetChildDatasets returns the paths of the datasets of type datasetType
// contained in parentPath (use `\` for the root). An empty datasetType
// matches all types.
func (g *Geodatabase) GetChildDatasets(parentPath, datasetType string) ([]string, error) {
	items, err := g.childItems(parentPath, datasetType)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.path
	}
	return paths, nil
}

// GetChildDatasetDefinitions returns the XML definitions of the datasets
// returned by GetChildDatasets.
func (g *Geodatabase) GetChildDatasetDefinitions(parentPath, datasetType string) ([]string, error) {
	items, err := g.childItems(parentPath, datasetType)
	if err != nil {
		return nil, err
	}
	defs := make([]string, len(items))
	for i, it := range items {
		defs[i] = it.definition
	}
	return defs, nil
}

func (g *Geodatabase) childItems(parentPath, datasetType string) ([]*item, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	if g.cat == nil {
		// FileGDB v9: no item catalog; list user tables at the root.
		if normPath(parentPath) != `\` {
			return nil, nil
		}
		var out []*item
		for _, e := range g.catalog {
			if e.id <= 36 || strings.HasPrefix(e.name, "GDB_") {
				continue
			}
			typ := DatasetTypeTable
			if t, err := gdbtable.Open(g.tableFile(e.id)); err == nil {
				if t.GeomField >= 0 {
					typ = DatasetTypeFeatureClass
				}
				t.Close()
			}
			if datasetType == "" || strings.EqualFold(datasetType, typ) {
				out = append(out, &item{name: e.name, path: `\` + e.name})
			}
		}
		return out, nil
	}
	parent := g.cat.itemByPath(parentPath)
	if parent == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, parentPath)
	}
	return g.cat.children(parent, datasetType), nil
}

// GetRelatedDatasets returns the paths of the datasets of type datasetType
// related to path through relationships of type relType (either direction).
// Empty relType or datasetType match all.
func (g *Geodatabase) GetRelatedDatasets(path, relType, datasetType string) ([]string, error) {
	items, err := g.relatedItems(path, relType, datasetType)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.path
	}
	return out, nil
}

// GetRelatedDatasetDefinitions returns the XML definitions of the datasets
// returned by GetRelatedDatasets.
func (g *Geodatabase) GetRelatedDatasetDefinitions(path, relType, datasetType string) ([]string, error) {
	items, err := g.relatedItems(path, relType, datasetType)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.definition
	}
	return out, nil
}

func (g *Geodatabase) relatedItems(path, relType, datasetType string) ([]*item, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	if g.cat == nil {
		return nil, nil
	}
	it := g.cat.itemByPath(path)
	if it == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, path)
	}
	return g.cat.related(it, relType, datasetType), nil
}

// GetDatasetDefinition returns the XML definition of a dataset.
func (g *Geodatabase) GetDatasetDefinition(path, datasetType string) (string, error) {
	it, err := g.lookupItem(path, datasetType)
	if err != nil {
		return "", err
	}
	return it.definition, nil
}

// GetDatasetDocumentation returns the XML metadata of a dataset.
func (g *Geodatabase) GetDatasetDocumentation(path, datasetType string) (string, error) {
	it, err := g.lookupItem(path, datasetType)
	if err != nil {
		return "", err
	}
	return it.documentation, nil
}

func (g *Geodatabase) lookupItem(path, datasetType string) (*item, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	if g.cat == nil {
		return nil, fmt.Errorf("%w: geodatabase has no item catalog", ErrNotSupported)
	}
	it := g.cat.itemByPath(path)
	if it == nil || (datasetType != "" && !strings.EqualFold(g.cat.typeName(it), datasetType)) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, path)
	}
	return it, nil
}

// GetDomains returns the names of the attribute domains.
func (g *Geodatabase) GetDomains() ([]string, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	if g.cat == nil {
		return nil, nil
	}
	var names []string
	for _, it := range g.cat.items {
		if it.typeID == itemTypeCodedDomain || it.typeID == itemTypeRangeDomain {
			names = append(names, it.name)
		}
	}
	return names, nil
}

// GetDomainDefinition returns the XML definition of an attribute domain.
func (g *Geodatabase) GetDomainDefinition(name string) (string, error) {
	if err := g.check(); err != nil {
		return "", err
	}
	if g.cat != nil {
		for _, it := range g.cat.items {
			if (it.typeID == itemTypeCodedDomain || it.typeID == itemTypeRangeDomain) && strings.EqualFold(it.name, name) {
				return it.definition, nil
			}
		}
	}
	return "", fmt.Errorf("%w: domain %q", ErrNotFound, name)
}

// GetQueryName returns the name to use in SQL statements for a dataset path.
func (g *Geodatabase) GetQueryName(path string) (string, error) {
	if err := g.check(); err != nil {
		return "", err
	}
	name, _, err := g.resolveTable(path)
	return name, err
}

// resolveTable maps a dataset path or table name to its catalog name and
// item (item may be nil).
func (g *Geodatabase) resolveTable(path string) (string, *item, error) {
	p := strings.TrimSpace(path)
	if g.cat != nil {
		if it := g.cat.itemByPath(p); it != nil && it.path != `\` {
			return it.name, it, nil
		}
	}
	name := p
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	if e, ok := g.findCatalog(name); ok {
		var it *item
		if g.cat != nil {
			it = g.cat.itemByName(e.name)
		}
		return e.name, it, nil
	}
	return "", nil, fmt.Errorf("%w: table %q", ErrNotFound, path)
}

// OpenTable opens a table or feature class by path (e.g. `\Cities`,
// `\Dataset\Roads`) or by name.
func (g *Geodatabase) OpenTable(path string) (*Table, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	name, it, err := g.resolveTable(path)
	if err != nil {
		return nil, err
	}
	e, _ := g.findCatalog(name)
	if e.fileFormat == 1 {
		return nil, fmt.Errorf("%w: table %q uses the compressed (CDF) format", ErrNotSupported, name)
	}
	if g.writable {
		// Tables opened for update are shared, so that all edits go
		// through one copy of the row index.
		g.mu.Lock()
		for t := range g.tables {
			if strings.EqualFold(t.name, name) {
				t.refs++
				g.mu.Unlock()
				return t, nil
			}
		}
		g.mu.Unlock()
	}
	fn := g.tableFile(e.id)
	if _, err := os.Stat(fn); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: table %q has no data file", ErrNotFound, name)
	}
	var raw *gdbtable.Table
	if g.writable {
		raw, err = gdbtable.OpenForUpdate(fn)
	} else {
		raw, err = gdbtable.Open(fn)
	}
	if err != nil {
		return nil, err
	}
	t := newTable(g, name, raw, it)
	t.refs = 1
	g.mu.Lock()
	g.tables[t] = struct{}{}
	g.mu.Unlock()
	return t, nil
}

// CloseTable closes a table (equivalent to t.Close()).
func (g *Geodatabase) CloseTable(t *Table) error { return t.Close() }

// release drops one reference to t and reports whether others remain.
func (g *Geodatabase) release(t *Table) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t.refs--; t.refs > 0 {
		return true
	}
	delete(g.tables, t)
	return false
}

func (g *Geodatabase) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrClosed
	}
	return nil
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == `\` {
		return `\`
	}
	if !strings.HasPrefix(p, `\`) {
		p = `\` + p
	}
	return strings.TrimSuffix(p, `\`)
}
