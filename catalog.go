package fgdb

import (
	"strings"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// item is a row of GDB_Items.
type item struct {
	fid           int64
	uuid          Guid
	typeID        Guid
	name          string
	physicalName  string
	path          string
	definition    string
	documentation string
}

type itemType struct {
	uuid, parent Guid
	name         string
}

type relType struct {
	uuid          Guid
	name          string
	isContainment bool
}

type rel struct {
	fid          int64
	uuid         Guid
	origin, dest Guid
	typeID       Guid
}

// catalogData holds the FileGDB 10 item catalog (GDB_Items,
// GDB_ItemTypes, GDB_ItemRelationships, GDB_ItemRelationshipTypes).
type catalogData struct {
	items     []*item
	itemTypes map[Guid]*itemType
	relTypes  map[Guid]*relType
	rels      []*rel
}

func loadCatalogData(g *Geodatabase) (*catalogData, error) {
	if _, ok := g.findCatalog("GDB_Items"); !ok {
		return nil, nil // FileGDB v9
	}
	c := &catalogData{itemTypes: map[Guid]*itemType{}, relTypes: map[Guid]*relType{}}
	err := scanRaw(g, "GDB_Items", func(fid int64, r rawRow) {
		c.items = append(c.items, &item{
			fid:           fid,
			uuid:          r.guid("UUID"),
			typeID:        r.guid("Type"),
			name:          r.str("Name"),
			physicalName:  r.str("PhysicalName"),
			path:          r.str("Path"),
			definition:    r.str("Definition"),
			documentation: r.str("Documentation"),
		})
	})
	if err != nil {
		return nil, err
	}
	_ = scanRaw(g, "GDB_ItemTypes", func(_ int64, r rawRow) {
		t := &itemType{uuid: r.guid("UUID"), parent: r.guid("ParentTypeID"), name: r.str("Name")}
		c.itemTypes[t.uuid] = t
	})
	_ = scanRaw(g, "GDB_ItemRelationshipTypes", func(_ int64, r rawRow) {
		t := &relType{uuid: r.guid("UUID"), name: r.str("Name"), isContainment: r.int("IsContainment") != 0}
		c.relTypes[t.uuid] = t
	})
	_ = scanRaw(g, "GDB_ItemRelationships", func(fid int64, r rawRow) {
		c.rels = append(c.rels, &rel{fid: fid, uuid: r.guid("UUID"), origin: r.guid("OriginID"), dest: r.guid("DestID"), typeID: r.guid("Type")})
	})
	// Fill in well-known types if GDB_ItemTypes could not be read.
	for id, name := range map[Guid]string{
		itemTypeFolder: DatasetTypeFolder, itemTypeWorkspace: DatasetTypeWorkspace,
		itemTypeFeatureDataset: DatasetTypeFeatureDataset, itemTypeFeatureClass: DatasetTypeFeatureClass,
		itemTypeTable: DatasetTypeTable, itemTypeCodedDomain: DatasetTypeCodedDomain,
		itemTypeRangeDomain: DatasetTypeRangeDomain,
	} {
		if c.itemTypes[id] == nil {
			c.itemTypes[id] = &itemType{uuid: id, name: name}
		}
	}
	return c, nil
}

// rawRow gives typed access to a decoded system table row.
type rawRow struct {
	t    *gdbtable.Table
	vals []any
}

func (r rawRow) get(name string) any {
	i := r.t.FieldIndex(name)
	if i < 0 {
		return nil
	}
	return r.vals[i]
}

func (r rawRow) str(name string) string { s, _ := r.get(name).(string); return s }

func (r rawRow) guid(name string) Guid { g, _ := r.get(name).(gdbtable.GUID); return Guid(g) }

func (r rawRow) int(name string) int64 {
	switch v := r.get(name).(type) {
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func scanRaw(g *Geodatabase, table string, fn func(fid int64, r rawRow)) error {
	t, err := g.openRawTable(table)
	if err != nil {
		return err
	}
	defer t.Close()
	for fid, err := t.NextFID(1); ; fid, err = t.NextFID(fid + 1) {
		if err != nil {
			return err
		}
		if fid == 0 {
			return nil
		}
		vals, err := t.ReadRow(fid)
		if err != nil {
			return err
		}
		fn(fid, rawRow{t, vals})
	}
}

func (c *catalogData) itemByPath(p string) *item {
	p = normPath(p)
	for _, it := range c.items {
		if it.path != "" && strings.EqualFold(normPath(it.path), p) {
			return it
		}
	}
	return nil
}

func (c *catalogData) itemByName(name string) *item {
	for _, it := range c.items {
		if strings.EqualFold(it.name, name) && it.path != "" {
			return it
		}
	}
	for _, it := range c.items {
		if strings.EqualFold(it.name, name) {
			return it
		}
	}
	return nil
}

func (c *catalogData) itemByUUID(id Guid) *item {
	for _, it := range c.items {
		if it.uuid == id {
			return it
		}
	}
	return nil
}

func (c *catalogData) typeName(it *item) string {
	if t := c.itemTypes[it.typeID]; t != nil {
		return t.name
	}
	return ""
}

// isOfType reports whether item type id is typeName or derives from it.
func (c *catalogData) isOfType(id Guid, typeName string) bool {
	if typeName == "" {
		return true
	}
	for i := 0; i < 16; i++ {
		t := c.itemTypes[id]
		if t == nil {
			return false
		}
		if strings.EqualFold(t.name, typeName) {
			return true
		}
		id = t.parent
	}
	return false
}

func (c *catalogData) children(parent *item, datasetType string) []*item {
	var out []*item
	seen := map[*item]bool{}
	for _, r := range c.rels {
		rt := c.relTypes[r.typeID]
		if r.origin != parent.uuid || rt == nil || !rt.isContainment {
			continue
		}
		if it := c.itemByUUID(r.dest); it != nil && !seen[it] && c.isOfType(it.typeID, datasetType) {
			seen[it] = true
			out = append(out, it)
		}
	}
	if len(out) > 0 || len(c.rels) > 0 {
		return out
	}
	// No relationships recorded: fall back to the item paths.
	pp := normPath(parent.path)
	for _, it := range c.items {
		if it == parent || it.path == "" {
			continue
		}
		ip := normPath(it.path)
		i := strings.LastIndex(ip, `\`)
		dir := ip[:i]
		if dir == "" {
			dir = `\`
		}
		if strings.EqualFold(dir, pp) && c.isOfType(it.typeID, datasetType) {
			out = append(out, it)
		}
	}
	return out
}

func (c *catalogData) related(it *item, relTypeName, datasetType string) []*item {
	var out []*item
	seen := map[*item]bool{}
	for _, r := range c.rels {
		if relTypeName != "" {
			rt := c.relTypes[r.typeID]
			if rt == nil || !strings.EqualFold(rt.name, relTypeName) {
				continue
			}
		}
		var other Guid
		switch it.uuid {
		case r.origin:
			other = r.dest
		case r.dest:
			other = r.origin
		default:
			continue
		}
		if o := c.itemByUUID(other); o != nil && !seen[o] && c.isOfType(o.typeID, datasetType) {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out
}
